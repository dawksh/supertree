package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dawksh/supertree/internal/workspace"
)

func git(t *testing.T, path string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func tmuxSocket(t *testing.T) string {
	t.Helper()
	// macOS's normal test temp path can already exceed the Unix socket limit.
	dir, err := os.MkdirTemp("/tmp", "st-next-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "sock")
}

func TestDiscoverRealWorktreesWithoutMutation(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo with spaces")
	root := filepath.Join(base, "trees")
	state := filepath.Join(base, "state")
	for _, path := range []string{repo, root, state} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-q", "--allow-empty", "-m", "initial")
	linked := filepath.Join(root, "feature\t雪")
	git(t, repo, "worktree", "add", "-q", "-b", "feature/slash", linked)
	git(t, repo, "worktree", "lock", "--reason", "keep this", linked)
	outside := filepath.Join(base, "outside\ncheckout")
	git(t, repo, "worktree", "add", "-q", "--detach", outside)
	missing := filepath.Join(root, "missing")
	git(t, repo, "worktree", "add", "-q", "-b", "missing", missing)
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "repo-alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	registryText := repo + "\n" + alias + "\n" + filepath.Join(base, "gone") + "\n"
	write(t, filepath.Join(state, "repos"), registryText)
	// Read-only discovery must never source repository configuration.
	write(t, filepath.Join(repo, ".supertree"), "touch '"+filepath.Join(base, "hook-ran")+"'\n")
	before := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain", "-z")
	beforeData, err := before.Output()
	if err != nil {
		t.Fatal(err)
	}
	options := workspace.Options{StateDir: state, WorktreeRoot: root, Directory: linked, TmuxSocket: tmuxSocket(t)}
	snapshot, err := workspace.Discover(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workspaces) != 4 {
		t.Fatalf("expected deduplicated main + 3 worktrees, got %#v", snapshot)
	}
	byBranch := map[string]workspace.Workspace{}
	for _, w := range snapshot.Workspaces {
		byBranch[w.Branch] = w
		if w.SessionIDs == nil {
			t.Fatal("session IDs must encode as an array")
		}
	}
	if w := byBranch["main"]; w.Kind != "main" || !w.Managed || w.State != "available" {
		t.Fatalf("main: %#v", w)
	}
	if w := byBranch["feature/slash"]; !w.Locked || !w.Managed || filepath.Base(w.Path) != filepath.Base(linked) {
		t.Fatalf("linked path or lock lost: %#v", w)
	}
	if w := byBranch[""]; !w.Detached || w.Managed || w.State != "available" || filepath.Base(w.Path) != filepath.Base(outside) {
		t.Fatalf("external detached worktree: %#v", w)
	}
	if w := byBranch["missing"]; w.State != "missing" || !w.Prunable {
		t.Fatalf("missing worktree was hidden: %#v", w)
	}
	if len(snapshot.Warnings) != 1 || snapshot.Warnings[0].Code != "repository_unavailable" {
		t.Fatalf("missing registered repository not reported: %#v", snapshot.Warnings)
	}
	if _, err := os.Stat(filepath.Join(base, "hook-ran")); !os.IsNotExist(err) {
		t.Fatal("repository hook executed")
	}
	registryAfter, _ := os.ReadFile(filepath.Join(state, "repos"))
	if string(registryAfter) != registryText {
		t.Fatal("registry changed")
	}
	afterData, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain", "-z").Output()
	if err != nil || string(beforeData) != string(afterData) {
		t.Fatal("discovery changed worktree metadata")
	}
	// Branch changes must not change the path-based workspace identity.
	git(t, linked, "checkout", "-q", "-b", "renamed")
	again, err := workspace.Discover(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range again.Workspaces {
		if w.Branch == "renamed" && w.ID != byBranch["feature/slash"].ID {
			t.Fatal("branch change changed workspace identity")
		}
	}
	if !reflect.DeepEqual(snapshot.Sessions, again.Sessions) {
		t.Fatal("discovery unexpectedly changed sessions")
	}
}

func TestDiscoverCurrentUnregisteredRepoAndEmptyState(t *testing.T) {
	base := t.TempDir()
	options := workspace.Options{StateDir: filepath.Join(base, "state"), WorktreeRoot: filepath.Join(base, "trees"), Directory: base, TmuxSocket: tmuxSocket(t)}
	snapshot, err := workspace.Discover(context.Background(), options)
	if err != nil || len(snapshot.Workspaces) != 0 || len(snapshot.Warnings) != 0 {
		t.Fatalf("empty discovery: %#v, %v", snapshot, err)
	}
	git(t, base, "init", "-q", "-b", "main")
	snapshot, err = workspace.Discover(context.Background(), options)
	if err != nil || len(snapshot.Workspaces) != 1 || snapshot.Workspaces[0].Branch != "main" {
		t.Fatalf("current unborn checkout: %#v, %v", snapshot, err)
	}
	if _, err := os.Stat(options.StateDir); !os.IsNotExist(err) {
		t.Fatal("read-only discovery created state directory")
	}
}

func TestDiscoverMapsLiveTmuxSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	base := t.TempDir()
	git(t, base, "init", "-q", "-b", "main")
	socket := tmuxSocket(t)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", socket, "kill-server").Run()
	})
	cmd := exec.Command("tmux", "-S", socket, "-f", "/dev/null", "new-session", "-d", "-s", "test-session", "-c", base, "sleep", "60")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated tmux: %v\n%s", err, out)
	}
	options := workspace.Options{StateDir: filepath.Join(base, "state"), WorktreeRoot: filepath.Join(base, "trees"), Directory: base, TmuxSocket: socket}
	snapshot, err := workspace.Discover(context.Background(), options)
	if err != nil || snapshot.TmuxStatus != "running" || len(snapshot.Sessions) != 1 || len(snapshot.Workspaces) != 1 {
		t.Fatalf("live snapshot: %#v, %v", snapshot, err)
	}
	if ids := snapshot.Workspaces[0].SessionIDs; len(ids) != 1 || ids[0] != snapshot.Sessions[0].ID {
		t.Fatalf("session association: %#v", snapshot.Workspaces)
	}
	if err := exec.Command("tmux", "-S", socket, "has-session", "-t", "=test-session").Run(); err != nil {
		t.Fatal("inspection removed the session")
	}
}

func TestSameNamedRepositoriesHaveDistinctIdentities(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(base, "a", "demo"), filepath.Join(base, "b", "demo")}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		git(t, path, "init", "-q", "-b", "main")
	}
	write(t, filepath.Join(state, "repos"), paths[0]+"\n"+paths[1]+"\n")
	snapshot, err := workspace.Discover(context.Background(), workspace.Options{
		StateDir: state, WorktreeRoot: filepath.Join(base, "trees"), Directory: base, TmuxSocket: tmuxSocket(t),
	})
	if err != nil || len(snapshot.Workspaces) != 2 {
		t.Fatalf("snapshot: %#v, %v", snapshot, err)
	}
	a, b := snapshot.Workspaces[0], snapshot.Workspaces[1]
	if a.ID == b.ID || a.RepositoryID == b.RepositoryID {
		t.Fatal("same-named repositories share identity")
	}
}
