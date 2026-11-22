package core_test

import (
	"context"
	"encoding/json"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/tmux"
	"os/exec"
	"strings"
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

func TestCaptureLiveLayoutAndRejectForeignPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	options, repo := fixture(t)
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
	v, err := call("process.start", process.StartArgs{WorkspaceID: id, Name: "one", Command: []string{"sleep", "120"}})
	if err != nil {
		t.Fatal(err)
	}
	p := v.(workspace.Process)
	if _, err := call("layout.capture", map[string]any{"workspace_id": id, "process_id": p.ID}); err == nil {
		t.Fatal("capture accepted missing revision")
	}
	args := map[string]any{"workspace_id": id, "process_id": p.ID, "expected_revision": 0}
	before, err := client.Run(ctx, "display-message", "-p", "-t", p.PaneID, "#{pane_pid} #{window_layout}")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := client.Run(ctx, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", p.SessionID, "-c", repo, "exec sleep 120")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Run(ctx, "set-option", "-p", "-t", duplicate, "@st_next_process", p.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.Refresh(ctx, repo, core.Origin{Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err = call("layout.capture", args); err == nil {
		t.Fatal("capture accepted duplicate process tag in another window")
	}
	inspected, err := call("layout.inspect", map[string]any{"workspace_id": id})
	if err != nil || inspected.(core.Layout).Revision != 0 {
		t.Fatalf("rejected capture modified saved layout: %v %v", inspected, err)
	}
	if _, err = client.Run(ctx, "kill-pane", "-t", duplicate); err != nil {
		t.Fatal(err)
	}
	if err = c.Refresh(ctx, repo, core.Origin{Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	got, err := call("layout.capture", args)
	if err != nil {
		t.Fatal(err)
	}
	layout := got.(core.Layout)
	if layout.Revision != 1 || layout.Root["resource_id"] != p.ID {
		t.Fatalf("capture: %+v", layout)
	}
	after, _ := client.Run(ctx, "display-message", "-p", "-t", p.PaneID, "#{pane_pid} #{window_layout}")
	if before != after {
		t.Fatal("capture changed pane")
	}
	if _, err = call("layout.capture", args); err == nil {
		t.Fatal("stale revision accepted")
	}
	foreign, err := client.Run(ctx, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", p.PaneID, "-c", repo, "exec sleep 120")
	if err != nil {
		t.Fatal(err)
	}
	args["expected_revision"] = 1
	if _, err = call("layout.capture", args); err == nil {
		t.Fatal("foreign pane accepted")
	}
	v, err = call("process.register", process.RegisterArgs{WorkspaceID: id, Name: "second", PaneID: foreign})
	if err != nil {
		t.Fatal(err)
	}
	second := v.(workspace.Process)
	third, err := client.Run(ctx, "split-window", "-d", "-v", "-P", "-F", "#{pane_id}", "-t", foreign, "-c", repo, "exec sleep 120")
	if err != nil {
		t.Fatal(err)
	}
	v, err = call("process.register", process.RegisterArgs{WorkspaceID: id, Name: "third", PaneID: third})
	if err != nil {
		t.Fatal(err)
	}
	thirdProcess := v.(workspace.Process)
	duplicate, err = client.Run(ctx, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", p.SessionID, "-c", repo, "exec sleep 120")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Run(ctx, "set-option", "-p", "-t", duplicate, "@st_next_process", second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = call("layout.capture", args); err == nil {
		t.Fatal("capture accepted duplicate non-anchor process tag in another window")
	}
	if _, err = client.Run(ctx, "kill-pane", "-t", duplicate); err != nil {
		t.Fatal(err)
	}
	got, err = call("layout.capture", args)
	if err != nil {
		t.Fatal(err)
	}
	layout = got.(core.Layout)
	if layout.Revision != 2 {
		t.Fatalf("revision: %+v", layout)
	}
	data, _ := json.Marshal(layout.Root)
	for _, want := range []string{p.ID, second.ID, thirdProcess.ID} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s in %s", want, data)
		}
	}
	if _, err = client.Run(ctx, "resize-pane", "-Z", "-t", p.PaneID); err != nil {
		t.Fatal(err)
	}
	args["expected_revision"] = 2
	if _, err = call("layout.capture", args); err == nil {
		t.Fatal("zoomed layout accepted")
	}
	if _, err = client.Run(ctx, "resize-pane", "-Z", "-t", p.PaneID); err != nil {
		t.Fatal(err)
	}
	got, err = call("layout.inspect", map[string]any{"workspace_id": id})
	if err != nil || got.(core.Layout).Revision != 2 {
		t.Fatalf("inspect: %+v %v", got, err)
	}
}
