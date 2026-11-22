package core_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestRestoreSavedTwoPaneLayoutWithoutRestartingProcesses(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	options, _ := fixture(t)
	ctx := context.Background()
	client := tmux.Client{Socket: options.TmuxSocket}
	defer client.Run(ctx, "kill-server")
	c, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r, _ := core.Registry(func(context.Context, core.InspectArgs) (workspace.Snapshot, error) { return c.Snapshot("") })
	if err := c.RegisterProcesses(r); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterLayouts(r); err != nil {
		t.Fatal(err)
	}
	call := func(name string, args any) (any, error) {
		raw, _ := json.Marshal(args)
		return r.Invoke(ctx, name, raw)
	}
	snap, _ := c.Snapshot("")
	ws := snap.Workspaces[0].ID
	start := func(name string) workspace.Process {
		v, e := call("process.start", process.StartArgs{WorkspaceID: ws, Name: name, Command: []string{"sleep", "120"}})
		if e != nil {
			t.Fatal(e)
		}
		return v.(workspace.Process)
	}
	a, b := start("a"), start("b")
	if _, err := call("process.move", process.MoveArgs{ProcessID: b.ID, TargetID: a.ID, Direction: "horizontal"}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		s, e := client.Run(ctx, args...)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	beforeA := run("display-message", "-p", "-t", a.PaneID, "#{pane_pid}")
	beforeB := run("display-message", "-p", "-t", b.PaneID, "#{pane_pid}")
	root := map[string]any{"type": "split", "direction": "vertical", "ratio": 0.6, "children": []any{map[string]any{"type": "pane", "resource_id": a.ID}, map[string]any{"type": "pane", "resource_id": b.ID}}}
	if _, err := call("layout.save", map[string]any{"workspace_id": ws, "expected_revision": 0, "root": root}); err != nil {
		t.Fatal(err)
	}
	live := run("display-message", "-p", "-t", a.PaneID, "#{window_layout}")
	if _, err := call("layout.restore", map[string]any{"workspace_id": ws, "expected_revision": 1, "expected_layout": live}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if beforeA != run("display-message", "-p", "-t", a.PaneID, "#{pane_pid}") || beforeB != run("display-message", "-p", "-t", b.PaneID, "#{pane_pid}") {
		t.Fatal("restore restarted a process")
	}
	layout := run("display-message", "-p", "-t", a.PaneID, "#{window_layout}")
	if !strings.Contains(layout, "[") {
		t.Fatalf("expected vertical split, got %s", layout)
	}
	topA := run("display-message", "-p", "-t", a.PaneID, "#{pane_top}")
	topB := run("display-message", "-p", "-t", b.PaneID, "#{pane_top}")
	if topA != "0" || topB == "0" {
		t.Fatalf("wrong pane order: %s %s", topA, topB)
	}
	// An out-of-date view must not overwrite an external resize.
	if _, err := call("layout.restore", map[string]any{"workspace_id": ws, "expected_revision": 1, "expected_layout": live}); err == nil {
		t.Fatal("stale live layout accepted")
	}
	if got := run("display-message", "-p", "-t", a.PaneID, "#{window_layout}"); got != layout {
		t.Fatal("stale restore changed tmux layout")
	}

	third := start("third")
	if _, err := call("process.move", process.MoveArgs{ProcessID: third.ID, TargetID: b.ID, Direction: "horizontal"}); err != nil {
		t.Fatal(err)
	}
	nested := map[string]any{"type": "split", "direction": "horizontal", "ratio": 0.5, "children": []any{map[string]any{"type": "pane", "resource_id": a.ID}, map[string]any{"type": "split", "direction": "vertical", "ratio": 0.5, "children": []any{map[string]any{"type": "pane", "resource_id": b.ID}, map[string]any{"type": "pane", "resource_id": third.ID}}}}}
	if _, err := call("layout.save", map[string]any{"workspace_id": ws, "expected_revision": 1, "root": nested}); err != nil {
		t.Fatal(err)
	}
	live = run("display-message", "-p", "-t", a.PaneID, "#{window_layout}")
	if _, err := call("layout.restore", map[string]any{"workspace_id": ws, "expected_revision": 2, "expected_layout": live}); err != nil {
		t.Fatalf("nested restore: %v", err)
	}
	if run("display-message", "-p", "-t", a.PaneID, "#{pane_left}") != "0" || run("display-message", "-p", "-t", b.PaneID, "#{pane_top}") != "0" || run("display-message", "-p", "-t", third.PaneID, "#{pane_top}") == "0" {
		t.Fatal("nested geometry incorrect")
	}

	current := run("display-message", "-p", "-t", a.PaneID, "#{window_layout}")
	if _, err := call("layout.restore", map[string]any{"workspace_id": ws, "expected_revision": 1, "expected_layout": current}); err == nil {
		t.Fatal("stale revision accepted")
	}
	outsider := run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", a.PaneID, "exec sleep 120")
	pid := run("display-message", "-p", "-t", outsider, "#{pane_pid}")
	current = run("display-message", "-p", "-t", outsider, "#{window_layout}")
	if _, err := call("layout.restore", map[string]any{"workspace_id": ws, "expected_revision": 2, "expected_layout": current}); err == nil {
		t.Fatal("unrelated pane accepted")
	}
	if run("display-message", "-p", "-t", outsider, "#{window_layout}") != current || run("display-message", "-p", "-t", outsider, "#{pane_pid}") != pid {
		t.Fatal("unrelated pane changed")
	}

	run("kill-pane", "-t", outsider)
	nested["children"].([]any)[1].(map[string]any)["ratio"] = 0.95
	if _, err := call("layout.save", map[string]any{"workspace_id": ws, "expected_revision": 2, "root": nested}); err != nil {
		t.Fatal(err)
	}
	run("resize-window", "-t", a.PaneID, "-x", "20", "-y", "5")
	current = run("display-message", "-p", "-t", a.PaneID, "#{window_layout}")
	if _, err := call("layout.restore", map[string]any{"workspace_id": ws, "expected_revision": 3, "expected_layout": current}); err == nil {
		t.Fatal("layout that cannot fit was accepted")
	}
	if run("display-message", "-p", "-t", a.PaneID, "#{window_layout}") != current {
		t.Fatal("unfittable restore changed layout")
	}

}
