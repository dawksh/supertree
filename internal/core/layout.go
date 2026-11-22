package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/dawksh/supertree/internal/command"
)

// Layout is desired state only; saving it does not change terminal panes.
type Layout struct {
	WorkspaceID string         `json:"workspace_id"`
	Revision    int64          `json:"revision"`
	Root        map[string]any `json:"root,omitempty"`
}
type LayoutSave struct {
	WorkspaceID      string         `json:"workspace_id"`
	ExpectedRevision int64          `json:"expected_revision"`
	Root             map[string]any `json:"root,omitempty"`
}
type layoutNode struct {
	Type       string       `json:"type"`
	ResourceID string       `json:"resource_id,omitempty"`
	Direction  string       `json:"direction,omitempty"`
	Ratio      float64      `json:"ratio,omitempty"`
	Children   []layoutNode `json:"children,omitempty"`
}

// UnmarshalJSON enforces exact field names at every level. The standard decoder
// otherwise matches names case-insensitively even with DisallowUnknownFields.
func (n *layoutNode) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name := range fields {
		switch name {
		case "type", "resource_id", "direction", "ratio", "children":
		default:
			return fmt.Errorf("unknown layout field %q", name)
		}
	}
	type node layoutNode
	return json.Unmarshal(data, (*node)(n))
}

func (c *Core) RegisterLayouts(r *command.Registry) error {
	if err := command.Register(r, command.Definition{Name: "layout.inspect", Version: 1, Description: "Inspect the saved desired layout (not the live tmux layout).", Effects: []string{"read"}, Repeatable: true}, func(ctx context.Context, args InspectArgs) (Layout, error) {
		if args.WorkspaceID == "" {
			return Layout{}, &command.Error{Code: "invalid_arguments", Message: "workspace_id is required"}
		}
		if _, err := c.workspace(args.WorkspaceID); err != nil {
			return Layout{}, err
		}
		docs, err := c.store.Documents(ctx, "layout")
		if err != nil {
			return Layout{}, err
		}
		for _, doc := range docs {
			if doc.ID == args.WorkspaceID {
				var root map[string]any
				if err := json.Unmarshal(doc.Data, &root); err != nil {
					return Layout{}, err
				}
				return Layout{args.WorkspaceID, doc.Revision, root}, nil
			}
		}
		return Layout{WorkspaceID: args.WorkspaceID}, nil
	}); err != nil {
		return err
	}
	return command.Register(r, command.Definition{Name: "layout.save", Version: 1, Description: "Save a desired layout; does not alter tmux.", Effects: []string{"layout.save"}}, func(ctx context.Context, args LayoutSave) (Layout, error) {
		c.refresh.Lock()
		defer c.refresh.Unlock()
		if args.WorkspaceID == "" {
			return Layout{}, &command.Error{Code: "invalid_arguments", Message: "workspace_id is required"}
		}
		if _, err := c.workspace(args.WorkspaceID); err != nil {
			return Layout{}, err
		}
		snapshot, err := c.Snapshot(args.WorkspaceID)
		if err != nil {
			return Layout{}, err
		}
		resources := map[string]bool{}
		for _, p := range snapshot.Processes {
			if p.WorkspaceID == args.WorkspaceID {
				resources[p.ID] = true
			}
		}
		if err := validateLayout(args.Root, resources); err != nil {
			return Layout{}, &command.Error{Code: "invalid_arguments", Message: err.Error()}
		}
		rev, err := c.store.PutDocument(ctx, "layout", args.WorkspaceID, args.Root, args.ExpectedRevision)
		if err != nil {
			return Layout{}, err
		}
		return Layout{args.WorkspaceID, rev, args.Root}, nil
	})
}

func validateLayout(root map[string]any, resources map[string]bool) error {
	if root == nil {
		return nil
	}
	data, err := json.Marshal(root)
	if err != nil {
		return err
	}
	if len(data) > 64<<10 {
		return fmt.Errorf("layout exceeds 64 KiB")
	}
	var node layoutNode
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&node); err != nil {
		return fmt.Errorf("invalid layout: %w", err)
	}
	seen := map[string]bool{}
	count := 0
	var visit func(layoutNode, int) error
	visit = func(n layoutNode, depth int) error {
		count++
		if depth > 32 || count > 255 {
			return fmt.Errorf("layout is too complex")
		}
		switch n.Type {
		case "pane":
			if n.ResourceID == "" || !resources[n.ResourceID] {
				return fmt.Errorf("pane references an unknown resource in this workspace")
			}
			if seen[n.ResourceID] {
				return fmt.Errorf("resource appears more than once")
			}
			seen[n.ResourceID] = true
			if n.Direction != "" || n.Ratio != 0 || len(n.Children) != 0 {
				return fmt.Errorf("pane contains split fields")
			}
		case "split":
			if n.ResourceID != "" || (n.Direction != "horizontal" && n.Direction != "vertical") || n.Ratio < 0.05 || n.Ratio > 0.95 || len(n.Children) != 2 {
				return fmt.Errorf("invalid split")
			}
			for _, child := range n.Children {
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unknown layout node type")
		}
		return nil
	}
	return visit(node, 1)
}
