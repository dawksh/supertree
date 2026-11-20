package process_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestExistingWorkspaceTerminalsAreObservedWithoutTakingOwnership(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	ctx := context.Background()
	base := workspace.CanonicalPath(t.TempDir())
	if out, err := exec.Command("git", "init", "-q", "-b", "main", base).CombinedOutput(); err != nil {
		t.Fatalf("git: %s %v", out, err)
	}
	sockets, err := os.MkdirTemp("/tmp", "st-observe-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	client := tmux.Client{Socket: filepath.Join(sockets, "tmux.sock")}
	defer client.Run(ctx, "kill-server")
	run := func(args ...string) string {
		t.Helper()
		out, err := client.Run(ctx, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	agentPane := run("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "repo/main", "-n", "codex", "-c", base, "exec sleep 120")
	run("set-option", "-t", "=repo/main:", "@supertree_label", "repo/main")
	run("set-option", "-t", "=repo/main:", "@st_tree_dir", base)
	run("set-option", "-t", "=repo/main:", "@st_agent_window", "codex")
	editor := run("new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=repo/main", "-n", "nvim", "-c", base, "exec sleep 120")
	shell := run("new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=repo/main", "-n", "shell", "-c", base, "exec sleep 120")
	sidebar := run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", shell, "exec sleep 120")
	run("set-option", "-p", "-t", sidebar, "@st_sidebar", "1")
	outsider := run("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "unrelated", "-c", base, "exec sleep 120")
	before := run("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}")
	options := workspace.Options{StateDir: filepath.Join(base, "state"), WorktreeRoot: filepath.Join(base, "trees"), Directory: base, TmuxSocket: client.Socket}
	c, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Close() }()
	snapshot, _ := c.Snapshot("")
	if len(snapshot.Processes) != 3 {
		t.Fatalf("expected agent/editor/shell only: %#v", snapshot.Processes)
	}
	ids := map[string]string{}
	for _, p := range snapshot.Processes {
		ids[p.PaneID] = p.ID
		if p.Owned || p.Source != "observed" {
			t.Fatalf("observation took ownership: %#v", p)
		}
		if run("show-options", "-pqv", "-t", p.PaneID, "@st_next_process") != "" {
			t.Fatal("observation tagged an existing pane")
		}
	}
	if ids[agentPane] == "" || ids[editor] == "" || ids[shell] == "" || ids[sidebar] != "" || ids[outsider] != "" {
		t.Fatal("incorrect workspace pane selection")
	}
	r, _ := core.Registry(func(context.Context, core.InspectArgs) (workspace.Snapshot, error) { return c.Snapshot("") })
	if err := c.RegisterProcesses(r); err != nil {
		t.Fatal(err)
	}
	invoke := func(name string, args any) (any, error) {
		raw, _ := json.Marshal(args)
		return r.Invoke(ctx, name, raw)
	}
	for _, name := range []string{"process.attach", "process.output"} {
		args := any(process.Target{ProcessID: ids[shell]})
		if name == "process.output" {
			args = process.OutputArgs{ProcessID: ids[shell]}
		}
		if _, err := invoke(name, args); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"process.stop", "process.restart", "process.move"} {
		args := any(process.Target{ProcessID: ids[shell]})
		if name == "process.move" {
			args = process.MoveArgs{ProcessID: ids[shell]}
		}
		if _, err := invoke(name, args); err == nil {
			t.Fatalf("observed terminal allowed %s", name)
		}
	}
	if run("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}") != before {
		t.Fatal("inspection changed running terminals")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ = c.Snapshot("")
	for _, p := range snapshot.Processes {
		if ids[p.PaneID] != p.ID {
			t.Fatal("daemon restart changed observed identity")
		}
	}
	r, _ = core.Registry(func(context.Context, core.InspectArgs) (workspace.Snapshot, error) { return c.Snapshot("") })
	c.RegisterProcesses(r)
	registered, err := invoke("process.register", process.RegisterArgs{WorkspaceID: snapshot.Workspaces[0].ID, Name: "human shell", Kind: "shell", PaneID: shell})
	if err != nil {
		t.Fatal(err)
	}
	if registered.(workspace.Process).ID != ids[shell] {
		t.Fatal("explicit registration changed pane identity")
	}
	if _, err := invoke("process.stop", process.Target{ProcessID: ids[shell]}); err != nil {
		t.Fatal(err)
	}
	for _, pane := range []string{agentPane, editor, outsider} {
		run("display-message", "-p", "-t", pane, "#{pane_pid}")
	}
	// Read-only observation must not reserve managed process names, including
	// names retained by older observations after their terminal has disappeared.
	if _, err := invoke("process.start", process.StartArgs{WorkspaceID: snapshot.Workspaces[0].ID, Name: "nvim", Kind: "tests", Command: []string{"/bin/sh", "-c", "exit 0"}}); err != nil {
		t.Fatalf("observed names changed launch behavior: %v", err)
	}
}
