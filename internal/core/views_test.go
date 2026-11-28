package core_test

import (
	"context"
	"encoding/json"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/workspace"
	"testing"
)

func TestSemanticViewSelectionBusyInputsAndStaleRevisions(t *testing.T) {
	options, _ := fixture(t)
	ctx := context.Background()
	c, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r, _ := core.Registry(func(context.Context, core.InspectArgs) (workspace.Snapshot, error) { return c.Snapshot("") })
	if err := c.RegisterViews(r); err != nil {
		t.Fatal(err)
	}
	call := func(name string, args any) (any, error) {
		raw, _ := json.Marshal(args)
		return r.Invoke(ctx, name, raw)
	}
	s, _ := c.Snapshot("")
	ws := s.Workspaces[0].ID
	value, err := call("view.open", core.ViewOpen{WorkspaceID: ws})
	if err != nil {
		t.Fatal(err)
	}
	view := value.(workspace.View)
	value, err = call("view.select", core.ViewSelect{ViewID: view.ID, WorkspaceID: ws, ExpectedRevision: view.Revision})
	if err != nil {
		t.Fatal(err)
	}
	view = value.(workspace.View)
	if _, err := call("view.select", core.ViewSelect{ViewID: view.ID, WorkspaceID: ws, ExpectedRevision: 1}); err == nil {
		t.Fatal("stale agent selection accepted")
	}
	value, err = call("view.present", core.ViewPresent{ViewID: view.ID, WorkspaceID: ws, Mode: "form", Panel: "processes", Input: "work in progress", ExpectedRevision: view.Revision})
	if err != nil {
		t.Fatal(err)
	}
	view = value.(workspace.View)
	if _, err := call("view.select", core.ViewSelect{ViewID: view.ID, WorkspaceID: ws}); err == nil {
		t.Fatal("agent overwrote active human input")
	}
	state, _ := c.Snapshot(ws)
	if len(state.Views) != 1 || state.Views[0].Input != "work in progress" {
		t.Fatal("view inspection missing pending input")
	}
	if _, err := call("view.close", core.ViewTarget{ViewID: view.ID}); err != nil {
		t.Fatal(err)
	}
	state, _ = c.Snapshot(ws)
	if len(state.Views) != 0 || len(state.Workspaces) != 1 {
		t.Fatal("closing view affected its workspace")
	}
}
