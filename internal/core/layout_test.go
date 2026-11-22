package core_test

import (
	"context"
	"encoding/json"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/tmux"
	"os/exec"
	"testing"

	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestDesiredLayoutPersistsPerWorkspace(t *testing.T) {
	options, repo := fixture(t)
	git(t, repo, "worktree", "add", "-q", "-b", "feature", options.WorktreeRoot+"/feature")
	ctx := context.Background()
	c, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	setup := func() func(string, any) (any, error) {
		r, err := core.Registry(func(context.Context, core.InspectArgs) (workspace.Snapshot, error) { return c.Snapshot("") })
		if err != nil {
			t.Fatal(err)
		}
		if err := c.RegisterLayouts(r); err != nil {
			t.Fatal(err)
		}
		return func(name string, v any) (any, error) { b, _ := json.Marshal(v); return r.Invoke(ctx, name, b) }
	}
	call := setup()
	snap, _ := c.Snapshot("")
	if len(snap.Workspaces) != 2 {
		t.Fatalf("workspaces: %+v", snap.Workspaces)
	}
	a, b := snap.Workspaces[0].ID, snap.Workspaces[1].ID
	for _, id := range []string{a, b} {
		if _, err := call("layout.save", map[string]any{"workspace_id": id, "expected_revision": 0}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call("layout.save", map[string]any{"workspace_id": a, "expected_revision": 0}); err == nil {
		t.Fatal("stale save accepted")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	call = setup()
	for _, id := range []string{a, b} {
		v, err := call("layout.inspect", map[string]any{"workspace_id": id})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(v)
		var got struct {
			WorkspaceID string `json:"workspace_id"`
			Revision    int64  `json:"revision"`
		}
		if err = json.Unmarshal(data, &got); err != nil || got.WorkspaceID != id || got.Revision != 1 {
			t.Fatalf("layout %s: %s %v", id, data, err)
		}
	}
}

func TestLayoutSavesRegisteredProcessesAndRejectsInvalidTrees(t *testing.T) {
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
	if err = c.RegisterProcesses(r); err != nil {
		t.Fatal(err)
	}
	if err = c.RegisterLayouts(r); err != nil {
		t.Fatal(err)
	}
	call := func(name string, args any) (any, error) {
		raw, _ := json.Marshal(args)
		return r.Invoke(ctx, name, raw)
	}
	snap, _ := c.Snapshot("")
	id := snap.Workspaces[0].ID
	start := func(name string) string {
		v, err := call("process.start", process.StartArgs{WorkspaceID: id, Name: name, Command: []string{"sleep", "120"}})
		if err != nil {
			t.Fatal(err)
		}
		return v.(workspace.Process).ID
	}
	a, b := start("editor"), start("shell")
	pane := func(id string) map[string]any { return map[string]any{"type": "pane", "resource_id": id} }
	tree := func(x, y string) map[string]any {
		return map[string]any{"type": "split", "direction": "horizontal", "ratio": 0.6, "children": []any{pane(x), pane(y)}}
	}
	for _, root := range []map[string]any{tree(a, a), tree(a, "unknown"), {"type": "split", "direction": "horizontal", "ratio": 1.0, "children": []any{pane(a), pane(b)}}} {
		if _, err := call("layout.save", map[string]any{"workspace_id": id, "expected_revision": 0, "root": root}); err == nil {
			t.Fatalf("invalid tree accepted: %v", root)
		}
	}
	root := tree(a, b)
	if _, err := call("layout.save", map[string]any{"workspace_id": id, "expected_revision": 0, "root": root}); err != nil {
		t.Fatal(err)
	}
	v, err := call("layout.inspect", map[string]any{"workspace_id": id})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(v)
	var got struct {
		Root map[string]any `json:"root"`
	}
	json.Unmarshal(data, &got)
	if got.Root["type"] != "split" {
		t.Fatalf("saved root lost: %s", data)
	}
}
