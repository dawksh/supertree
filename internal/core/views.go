package core

import (
	"context"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/workspace"
	"reflect"
	"slices"
	"sort"
	"time"
)

type viewLease struct {
	View workspace.View
	Seen time.Time
}
type ViewOpen struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
}
type ViewTarget struct {
	ViewID string `json:"view_id"`
}
type ViewSelect struct {
	ViewID           string `json:"view_id"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
	ProcessID        string `json:"process_id,omitempty"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}
type ViewPresent struct {
	ViewID           string `json:"view_id"`
	WorkspaceID      string `json:"workspace_id"`
	ProcessID        string `json:"process_id"`
	Panel            string `json:"panel"`
	Mode             string `json:"mode"`
	Command          string `json:"command"`
	Input            string `json:"input"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type Views struct {
	Views []workspace.View `json:"views"`
}

func (c *Core) RegisterViews(r *command.Registry) error {
	if err := registerProcess(c, r, "view.open", "Register a connected TUI client; returns an inspectable semantic view.", []string{"view.open"}, func(ctx context.Context, args ViewOpen) (workspace.View, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if err := c.validateSelection(args.WorkspaceID, ""); err != nil {
			return workspace.View{}, err
		}
		view := workspace.View{ID: process.ID("view"), WorkspaceID: args.WorkspaceID, Panel: "processes", Mode: "browse", Revision: 1, Actions: []string{"view.select", "process.start", "process.stop", "process.restart", "process.move", "process.attach", "command.list", "task.create", "task.update", "context.capture", "context.refresh", "context.export", "question.ask", "question.answer", "agent.start", "agent.send", "agent.interrupt", "delivery.inspect"}}
		c.views[view.ID] = viewLease{view, time.Now()}
		return view, nil
	}); err != nil {
		return err
	}
	if err := registerProcess(c, r, "view.list", "Inspect live TUI selection, pending input, and available command names.", []string{"read"}, func(context.Context, struct{}) (Views, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return Views{c.viewSnapshot()}, nil
	}); err != nil {
		return err
	}
	if err := registerProcess(c, r, "view.select", "Select workspace/process by semantic ID without changing terminal focus.", []string{"view.select"}, func(ctx context.Context, args ViewSelect) (workspace.View, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		lease, ok := c.views[args.ViewID]
		if !ok {
			return workspace.View{}, &command.Error{Code: "not_found", Message: "view is disconnected"}
		}
		if lease.View.Mode != "browse" {
			return lease.View, &command.Error{Code: "busy", Message: "human is entering a command; use the workspace commands directly"}
		}
		if args.ExpectedRevision != 0 && args.ExpectedRevision != lease.View.Revision {
			return lease.View, &command.Error{Code: "conflict", Message: "view changed; inspect before retrying"}
		}
		if err := c.validateSelection(args.WorkspaceID, args.ProcessID); err != nil {
			return lease.View, err
		}
		lease.View.WorkspaceID, lease.View.ProcessID = args.WorkspaceID, args.ProcessID
		lease.View.Revision++
		lease.Seen = time.Now()
		c.views[args.ViewID] = lease
		return lease.View, nil
	}); err != nil {
		return err
	}
	if err := registerProcess(c, r, "view.present", "Publish the TUI's actual selection and input state; stale presentations cannot overwrite agent selections.", []string{"view.present"}, func(ctx context.Context, args ViewPresent) (workspace.View, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		lease, ok := c.views[args.ViewID]
		if !ok {
			return workspace.View{}, &command.Error{Code: "not_found", Message: "view is disconnected"}
		}
		if lease.View.Revision != args.ExpectedRevision {
			return lease.View, &command.Error{Code: "conflict", Message: "view selection changed; inspect before presenting"}
		}
		if err := c.validateSelection(args.WorkspaceID, args.ProcessID); err != nil {
			return lease.View, err
		}
		if len(args.Input) > 16<<10 {
			return lease.View, &command.Error{Code: "invalid_arguments", Message: "view input exceeds 16 KiB"}
		}
		next := lease.View
		next.WorkspaceID, next.ProcessID, next.Panel, next.Mode, next.Command, next.Input = args.WorkspaceID, args.ProcessID, args.Panel, args.Mode, args.Command, args.Input
		if !reflect.DeepEqual(next, lease.View) {
			next.Revision++
		}
		lease.View, lease.Seen = next, time.Now()
		c.views[args.ViewID] = lease
		return next, nil
	}); err != nil {
		return err
	}
	return registerProcess(c, r, "view.close", "Disconnect a TUI view without stopping its processes.", []string{"view.close"}, func(ctx context.Context, args ViewTarget) (struct{}, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.views, args.ViewID)
		return struct{}{}, nil
	})
}

// validateSelection and viewSnapshot run while mu is held.
func (c *Core) validateSelection(workspaceID, processID string) error {
	if workspaceID != "" {
		found := false
		for _, w := range c.state.Workspaces {
			if w.ID == workspaceID {
				found = true
			}
		}
		if !found {
			return &command.Error{Code: "not_found", Message: "workspace not found"}
		}
	}
	if processID != "" {
		for _, p := range c.state.Processes {
			if p.ID == processID && p.WorkspaceID == workspaceID {
				return nil
			}
		}
		return &command.Error{Code: "not_found", Message: "process is not in the selected workspace"}
	}
	return nil
}
func (c *Core) viewSnapshot() []workspace.View {
	result := []workspace.View{}
	for id, lease := range c.views {
		if time.Since(lease.Seen) > 30*time.Second {
			delete(c.views, id)
			continue
		}
		result = append(result, lease.View)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func filterViews(views []workspace.View, id string) []workspace.View {
	return slices.DeleteFunc(slices.Clone(views), func(v workspace.View) bool { return v.WorkspaceID != id })
}
