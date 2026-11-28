package process_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestBorrowedTerminalsKeepSourceOwnership(t *testing.T) {
	for _, mode := range []string{"borrow before observation", "borrow after observation", "repair older observations"} {
		t.Run(mode, func(t *testing.T) {
			w, client := recoveryWorkspace(t)
			ctx := context.Background()
			run := func(args ...string) string {
				t.Helper()
				out, err := client.Run(ctx, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			git := func(args ...string) {
				t.Helper()
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatalf("git: %s %v", out, err)
				}
			}
			repo := filepath.Join(w.Path, "repo # 'quote")
			feature := filepath.Join(w.Path, "trees", "feature")
			git("init", "-q", "-b", "main", repo)
			git("-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-q", "--allow-empty", "-m", "initial")
			git("-C", repo, "worktree", "add", "-q", "-b", "feature", feature)
			source := run("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "repo/main", "-n", "codex", "-c", repo+"/", "printf 'source-program\\n'; exec sleep 120")
			dest := run("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "repo/feature", "-n", "shell", "-c", feature+"/", "exec sleep 120")
			for session, path := range map[string]string{"repo/main": repo, "repo/feature": feature} {
				run("set-option", "-t", "="+session+":", "@supertree_label", session)
				run("set-option", "-t", "="+session+":", "@st_tree_dir", path)
				run("set-option", "-t", "="+session+":", "@st_agent_window", "codex")
			}
			before := run("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}")
			options := workspace.Options{StateDir: filepath.Join(w.Path, "core"), WorktreeRoot: filepath.Dir(feature), Directory: repo, TmuxSocket: client.Socket}
			var c *core.Core
			t.Cleanup(func() {
				if c != nil {
					c.Close()
				}
			})
			open := func() {
				t.Helper()
				var err error
				c, err = core.Open(ctx, options)
				if err != nil {
					t.Fatal(err)
				}
			}
			oldID := ""
			if mode != "borrow before observation" {
				open()
				snapshot, _ := c.Snapshot("")
				for _, p := range snapshot.Processes {
					if p.PaneID == source {
						oldID = p.ID
					}
				}
			}
			root, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			// Exercise the real released borrow/return functions. Its holder runs
			// the actual st command, on the isolated socket inherited from tmux.
			shell := `task_root=$1
task_socket=$4
tmux() { command tmux -S "$task_socket" "$@"; }
st_register_command() { :; }
st_self() { printf '%s' "$task_root/bin/st"; }
. "$task_root/bin/st-lib/50-tmux.sh"
borrow_pane "$2" -h repo/main codex "$3"`
			if out, err := exec.Command("bash", "-eu", "-c", shell, "test", root, dest, source, client.Socket).CombinedOutput(); err != nil {
				t.Fatalf("borrow: %s %v", out, err)
			}
			holder := run("show-options", "-pqv", "-t", source, "@st_home_pane")
			if holder == "" {
				t.Fatal("borrow did not leave a source placeholder")
			}
			if c == nil {
				open()
			} else if err := c.Refresh(ctx, "", core.Origin{Actor: "test"}); err != nil {
				t.Fatal(err)
			}
			if mode == "repair older observations" {
				seedOlderBorrowObservations(t, options.StateDir, client, source, holder, c)
				if err := c.Refresh(ctx, "", core.Origin{Actor: "test"}); err != nil {
					t.Fatal(err)
				}
			}
			check := func() workspace.Process {
				t.Helper()
				snapshot, err := c.Snapshot("")
				if err != nil || len(snapshot.Processes) != 2 {
					t.Fatalf("holders must not be programs: %#v %v", snapshot.Processes, err)
				}
				paths := map[string]string{}
				for _, tree := range snapshot.Workspaces {
					paths[tree.ID] = tree.Path
				}
				for _, p := range snapshot.Processes {
					if p.PaneID == holder {
						t.Fatal("source holder observed as a terminal")
					}
					if p.PaneID != source {
						continue
					}
					if paths[p.WorkspaceID] != repo || p.Name != "codex" || p.Kind != "agent" || p.Source != "observed" || p.Owned {
						t.Fatalf("borrow changed source identity or ownership: %#v", p)
					}
					if oldID != "" && oldID != p.ID {
						t.Fatal("moving the terminal changed its durable ID")
					}
					return p
				}
				t.Fatal("source program is missing")
				return workspace.Process{}
			}
			p := check()
			oldID = p.ID
			c.Close()
			open()
			check()
			r, err := core.Registry(func(context.Context, core.InspectArgs) (workspace.Snapshot, error) { return c.Snapshot("") })
			if err != nil {
				t.Fatal(err)
			}
			if err := c.RegisterProcesses(r); err != nil {
				t.Fatal(err)
			}
			attachment, err := r.Invoke(ctx, "process.attach", mustJSON(process.Target{ProcessID: p.ID}))
			if err != nil || attachment.(process.Attachment).PaneID != source {
				t.Fatalf("attachment did not follow source program: %#v %v", attachment, err)
			}
			output, err := r.Invoke(ctx, "process.output", mustJSON(process.OutputArgs{ProcessID: p.ID}))
			if err != nil || !strings.Contains(output.(process.Output).Text, "source-program") {
				t.Fatalf("output did not follow source program: %#v %v", output, err)
			}
			if _, err := r.Invoke(ctx, "process.stop", mustJSON(process.Target{ProcessID: p.ID})); err == nil {
				t.Fatal("borrowing granted management of a human terminal")
			}
			// Enter in the real holder must return the existing program, not a copy.
			run("send-keys", "-t", holder, "Enter")
			deadline := time.Now().Add(3 * time.Second)
			for run("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}") != before {
				if time.Now().After(deadline) {
					t.Fatal("holder did not return the program on Enter")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if after := run("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}"); after != before {
				t.Fatalf("borrow/return replaced running programs: %s -> %s", before, after)
			}
			if err := c.Refresh(ctx, "", core.Origin{Actor: "test"}); err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}

// Simulate observations persisted by an older core: a wrong destination owner
// and a holder counted as a program. Keep the real persisted pane identities.
func seedOlderBorrowObservations(t *testing.T, state string, client tmux.Client, source, holder string, c *core.Core) {
	t.Helper()
	ctx := context.Background()
	snapshot, _ := c.Snapshot("")
	destWorkspace := ""
	for _, p := range snapshot.Processes {
		if p.PaneID != source && p.PaneID != holder {
			destWorkspace = p.WorkspaceID
		}
	}
	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	docs, err := s.Documents(ctx, "process")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		var data map[string]json.RawMessage
		if err := json.Unmarshal(doc.Data, &data); err != nil {
			t.Fatal(err)
		}
		var p workspace.Process
		if err := json.Unmarshal(data["process"], &p); err != nil {
			t.Fatal(err)
		}
		if p.PaneID != source {
			continue
		}
		p.WorkspaceID, p.Name, p.Kind = destWorkspace, "shell", "shell"
		data["process"] = mustJSON(p)
		if _, err := s.PutDocument(ctx, "process", doc.ID, data, doc.Revision); err != nil {
			t.Fatal(err)
		}
	}
	panes, err := client.Panes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pane := range panes {
		if pane.ID != holder {
			continue
		}
		identity := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{client.Socket, pane.ServerPID, pane.ID, pane.PID}, "\x00"))))
		p := workspace.Process{ID: "observed_" + identity[:32], WorkspaceID: destWorkspace, PaneID: holder, Name: "holder", Source: "observed", State: "running"}
		if _, err := s.PutDocument(ctx, "process", p.ID, map[string]any{"process": p, "pane_identity": identity}, 0); err != nil {
			t.Fatal(err)
		}
	}
}
