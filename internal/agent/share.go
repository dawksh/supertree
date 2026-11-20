package agent

import (
	"context"
	"slices"
	"strings"

	"github.com/dawksh/supertree/internal/collab"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/workspace"
)

type ShareArgs struct {
	WorkspaceID string       `json:"workspace_id"`
	Message     string       `json:"message,omitempty"`
	ContextIDs  []string     `json:"context_ids,omitempty"`
	Source      *ShareSource `json:"source,omitempty"`
	PaneID      string       `json:"pane_id,omitempty"`
	TaskID      string       `json:"task_id,omitempty"`
	Preview     bool         `json:"preview,omitempty"`
}
type ShareSource struct {
	Kind      string `json:"kind"`
	Title     string `json:"title,omitempty"`
	Path      string `json:"path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	ProcessID string `json:"process_id,omitempty"`
	Text      string `json:"text,omitempty"`
}
type ShareResult struct {
	Status     string              `json:"status"`
	AgentID    string              `json:"agent_id,omitempty"`
	ContextIDs []string            `json:"context_ids"`
	Text       string              `json:"text,omitempty"`
	Delivery   *workspace.Delivery `json:"delivery,omitempty"`
}

// Share is the single action used by the shell, editor, and structured clients.
// Source capture and delivery stay explicit, with freshness checked at delivery.
func (m *Manager) Share(ctx context.Context, args ShareArgs) (ShareResult, error) {
	if (!args.Preview && strings.TrimSpace(args.Message) == "") || len(args.Message) > 32<<10 {
		return ShareResult{}, &command.Error{Code: "invalid_arguments", Message: "provide a message up to 32 KiB, or use --preview to capture without sending"}
	}
	fingerprint := argumentHash(struct {
		Command string
		Args    ShareArgs
	}{"context.share", args})
	if !args.Preview && requestKey(ctx) != "" {
		records, err := load[deliveryRecord](ctx, m.store, "delivery")
		if err != nil {
			return ShareResult{}, err
		}
		for _, r := range records {
			if r.RequestKey == requestKey(ctx) {
				if r.Hash != fingerprint {
					return ShareResult{}, &command.Error{Code: "conflict", Message: "request ID was already used for different sharing content"}
				}
				d, err := m.Delivery(ctx, DeliveryTarget{DeliveryID: r.Delivery.ID})
				return shareReceipt(d), err
			}
		}
	}
	primary, err := m.Primary(ctx, PrimaryArgs{WorkspaceID: args.WorkspaceID})
	if err != nil && !args.Preview {
		return ShareResult{}, err
	}
	if !args.Preview && primary.State != "idle" && primary.State != "interrupted" && !(primary.State == "failed" && slices.Contains(primary.Capabilities, "send")) {
		return ShareResult{}, &command.Error{Code: "busy", Message: "the paired agent is not idle; inspect or interrupt its active turn before sharing"}
	}
	if args.TaskID == "" {
		task, err := m.collab.ActiveTask(ctx, args.WorkspaceID)
		if err != nil {
			return ShareResult{}, err
		}
		args.TaskID = task.ID
	}
	ids := append([]string{}, args.ContextIDs...)
	if args.Source != nil {
		s := *args.Source
		source := collab.CaptureArgs{WorkspaceID: args.WorkspaceID, TaskID: args.TaskID, Kind: s.Kind, Title: s.Title, Path: s.Path, StartLine: s.StartLine, EndLine: s.EndLine, ProcessID: s.ProcessID, Text: s.Text}
		if source.Kind == "output" && source.ProcessID == "" {
			processes, err := m.processes.Snapshot(ctx)
			if err != nil {
				return ShareResult{}, err
			}
			for _, p := range processes {
				if p.WorkspaceID == args.WorkspaceID && p.PaneID == args.PaneID {
					source.ProcessID = p.ID
					break
				}
			}
			if source.ProcessID == "" {
				return ShareResult{}, &command.Error{Code: "invalid_arguments", Message: "--output needs a shell in this worktree, or an explicit --process ID"}
			}
		}
		item, err := m.collab.Capture(ctx, source)
		if err != nil {
			return ShareResult{}, err
		}
		ids = append(ids, item.ID)
	}
	if args.Preview {
		exported, err := m.collab.Export(ctx, collab.ExportArgs{WorkspaceID: args.WorkspaceID, TaskID: args.TaskID, ContextIDs: ids})
		if err != nil {
			return ShareResult{}, err
		}
		return ShareResult{Status: "preview", AgentID: primary.ID, ContextIDs: exported.ContextIDs, Text: strings.TrimSpace(args.Message + "\n\n" + exported.Text)}, nil
	}
	d, err := m.send(ctx, SendArgs{AgentID: primary.ID, Message: args.Message, ContextIDs: ids, TaskID: args.TaskID}, fingerprint)
	return shareReceipt(d), err
}
func shareReceipt(d workspace.Delivery) ShareResult {
	return ShareResult{Status: d.Status, AgentID: d.AgentID, ContextIDs: d.ContextIDs, Delivery: &d}
}
