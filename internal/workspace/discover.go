// Package workspace discovers existing Supertree state. Discovery never adopts,
// registers, prunes, renames, or launches anything.
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/tmux"
)

type Options struct {
	StateDir     string
	WorktreeRoot string
	Directory    string
	TmuxSocket   string
	// Repositories supplies paths remembered by the daemon, without modifying
	// the legacy repository registry. Discover itself remains read-only.
	Repositories []string
}

type Workspace struct {
	ID             string   `json:"id"`
	RepositoryID   string   `json:"repository_id"`
	RepositoryPath string   `json:"repository_path"`
	Path           string   `json:"path"`
	Label          string   `json:"label"`
	Branch         string   `json:"branch"`
	HEAD           string   `json:"head"`
	Kind           string   `json:"kind"`
	State          string   `json:"state"`
	Managed        bool     `json:"managed"`
	Detached       bool     `json:"detached"`
	Locked         bool     `json:"locked"`
	Prunable       bool     `json:"prunable"`
	SessionIDs     []string `json:"session_ids"`
	IdentityKey    string   `json:"-"`
	RepositoryKey  string   `json:"-"`
}

type Warning struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Snapshot struct {
	SchemaVersion int            `json:"schema_version"`
	CapturedAt    string         `json:"captured_at"`
	StateDir      string         `json:"state_dir"`
	WorktreeRoot  string         `json:"worktree_root"`
	Workspaces    []Workspace    `json:"workspaces"`
	Sessions      []tmux.Session `json:"sessions"`
	TmuxStatus    string         `json:"tmux_status"`
	Warnings      []Warning      `json:"warnings"`
	Epoch         string         `json:"epoch,omitempty"`
	Sequence      int64          `json:"sequence"`
}

// Discover includes registered repositories and the current checkout. All Git
// worktrees are visible; Managed distinguishes linked trees under Supertree's root.
// IDs are deterministic canonical-path hashes until durable identity is introduced.
func Discover(ctx context.Context, o Options) (Snapshot, error) {
	snapshot := Snapshot{SchemaVersion: command.SchemaVersion, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		StateDir: canonical(o.StateDir), WorktreeRoot: canonical(o.WorktreeRoot),
		Workspaces: []Workspace{}, Sessions: []tmux.Session{}, Warnings: []Warning{}}
	if _, err := exec.LookPath("git"); err != nil {
		return snapshot, &command.Error{Code: "dependency_missing", Message: "workspace discovery requires git on PATH"}
	}
	data, err := os.ReadFile(filepath.Join(o.StateDir, "repos"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return snapshot, fmt.Errorf("read repository registry: %w", err)
	}
	seenRepos := make(map[string]bool)
	seenPaths := make(map[string]bool)
	paths := append(strings.Split(string(data), "\n"), o.Repositories...)
	// Current directory discovery is ephemeral: it never updates the registry.
	paths = append(paths, o.Directory)
	for i, path := range paths {
		current := i == len(paths)-1
		if path == "" {
			continue
		}
		path = canonical(path)
		if seenPaths[path] {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			if !current {
				snapshot.Warnings = append(snapshot.Warnings, Warning{"repository_unavailable", path, err.Error()})
			}
			continue
		}
		gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(gitCtx, "git", "-C", path, "worktree", "list", "--porcelain", "-z")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		cancel()
		if ctx.Err() != nil {
			return snapshot, ctx.Err()
		}
		if err != nil {
			if current && strings.Contains(stderr.String(), "not a git repository") {
				continue
			}
			snapshot.Warnings = append(snapshot.Warnings, Warning{"repository_scan_failed", path, strings.TrimSpace(stderr.String()) + " (" + err.Error() + ")"})
			continue
		}
		trees, err := parseWorktrees(out)
		if err != nil {
			snapshot.Warnings = append(snapshot.Warnings, Warning{"repository_scan_failed", path, err.Error()})
			continue
		}
		repoPath := canonical(trees[0].path)
		if seenRepos[repoPath] {
			continue
		}
		seenRepos[repoPath] = true
		repoDir := gitDirectory(repoPath, trees[0].bare)
		for j, tree := range trees {
			tree.path = canonical(tree.path)
			seenPaths[tree.path] = true
			kind := "worktree"
			if j == 0 {
				kind = "main"
			}
			if tree.bare {
				kind = "bare"
			}
			state := "available"
			if info, err := os.Stat(tree.path); errors.Is(err, os.ErrNotExist) {
				state = "missing"
			} else if err != nil || !info.IsDir() {
				state = "unavailable"
			}
			branchLabel := tree.branch
			if tree.detached {
				branchLabel = "(detached)"
			}
			if tree.bare {
				branchLabel = "(bare)"
			}
			snapshot.Workspaces = append(snapshot.Workspaces, Workspace{
				ID: pathID("ws_", tree.path), RepositoryID: pathID("repo_", repoPath),
				RepositoryPath: repoPath, Path: tree.path, Label: filepath.Base(repoPath) + "/" + branchLabel,
				Branch: tree.branch, HEAD: tree.head, Kind: kind, State: state,
				Managed:  kind == "main" || (kind == "worktree" && contains(snapshot.WorktreeRoot, tree.path)),
				Detached: tree.detached, Locked: tree.locked, Prunable: tree.prunable, SessionIDs: []string{},
				IdentityKey: fileIdentity(gitDirectory(tree.path, tree.bare)), RepositoryKey: fileIdentity(repoDir),
			})
		}
	}
	tmuxCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	snapshot.Sessions, snapshot.TmuxStatus, err = tmux.Inspect(tmuxCtx, o.TmuxSocket)
	cancel()
	if ctx.Err() != nil {
		return snapshot, ctx.Err()
	}
	if err != nil {
		snapshot.Warnings = append(snapshot.Warnings, Warning{"tmux_inspection_failed", o.TmuxSocket, err.Error()})
	}
	for _, session := range snapshot.Sessions {
		// Match the session's initial working directory, preferring the deepest
		// checkout. Session names and labels are display text, not identity.
		best := -1
		path := canonical(session.Path)
		for i, w := range snapshot.Workspaces {
			if contains(w.Path, path) && (best == -1 || len(w.Path) > len(snapshot.Workspaces[best].Path)) {
				best = i
			}
		}
		if best >= 0 {
			snapshot.Workspaces[best].SessionIDs = append(snapshot.Workspaces[best].SessionIDs, session.ID)
		}
	}
	sort.Slice(snapshot.Workspaces, func(i, j int) bool { return snapshot.Workspaces[i].Path < snapshot.Workspaces[j].Path })
	sort.Slice(snapshot.Sessions, func(i, j int) bool { return snapshot.Sessions[i].ID < snapshot.Sessions[j].ID })
	return snapshot, nil
}

// Git's administrative directory survives branch switches and worktree moves.
// Its filesystem identity also survives a main checkout's rename on the same
// filesystem. Copies have a distinct identity; no marker is written into Git.
func gitDirectory(path string, bare bool) string {
	if bare {
		return path
	}
	dot := filepath.Join(path, ".git")
	if info, err := os.Stat(dot); err == nil && info.IsDir() {
		return dot
	}
	data, err := os.ReadFile(dot)
	if err != nil || !strings.HasPrefix(string(data), "gitdir: ") {
		return ""
	}
	dir := strings.TrimSuffix(strings.TrimPrefix(string(data), "gitdir: "), "\n")
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(path, dir)
	}
	return dir
}

func fileIdentity(path string) string {
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}

func canonical(path string) string {
	return CanonicalPath(path)
}

// CanonicalPath resolves existing ancestors too, so a not-yet-created state
// directory has the same identity before and after its first daemon launch.
func CanonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	probe := abs
	for {
		if resolved, err := filepath.EvalSymlinks(probe); err == nil {
			rel, _ := filepath.Rel(probe, abs)
			return filepath.Join(resolved, rel)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	return abs
}

func pathID(prefix, path string) string {
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%s%x", prefix, sum[:16])
}

func contains(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type worktree struct {
	path, branch, head               string
	detached, bare, locked, prunable bool
}

// -z makes paths literal, including spaces, tabs, newlines and non-ASCII names.
func parseWorktrees(data []byte) ([]worktree, error) {
	trees := []worktree{}
	var tree worktree
	for _, field := range strings.Split(string(data), "\x00") {
		if field == "" {
			if tree.path != "" {
				trees = append(trees, tree)
				tree = worktree{}
			}
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		switch key {
		case "worktree":
			tree.path = value
		case "branch":
			tree.branch = strings.TrimPrefix(value, "refs/heads/")
		case "HEAD":
			tree.head = value
		case "detached":
			tree.detached = true
		case "bare":
			tree.bare = true
		case "locked":
			tree.locked = true
		case "prunable":
			tree.prunable = true
		}
	}
	if tree.path != "" {
		trees = append(trees, tree)
	}
	if len(trees) == 0 {
		return nil, fmt.Errorf("git returned no worktree records")
	}
	return trees, nil
}
