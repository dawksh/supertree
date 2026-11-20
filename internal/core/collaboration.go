package core

import (
	"context"
	"github.com/dawksh/supertree/internal/agent"
	"github.com/dawksh/supertree/internal/collab"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/workspace"
	"strings"
)

type Tasks struct {
	Tasks []workspace.Task `json:"tasks"`
}
type Contexts struct {
	Contexts []workspace.ContextItem `json:"contexts"`
}
type Questions struct {
	Questions []workspace.Question `json:"questions"`
}
type Agents struct {
	Agents []workspace.Agent `json:"agents"`
}

func (c *Core) RegisterCollaboration(r *command.Registry) error {
	registrations := []func() error{
		func() error {
			return registerProcess(c, r, "agent.bind", "Bind a foreground worker to this worktree's existing agent tab; never replace a running CLI.", []string{"agent.bind"}, c.agents.Bind)
		},
		func() error {
			return registerProcess(c, r, "agent.primary", "Inspect the native agent hosted in the normal agent tab.", []string{"read"}, func(ctx context.Context, args agent.PrimaryArgs) (workspace.Agent, error) {
				if err := c.Refresh(ctx, "", command.Caller(ctx)); err != nil {
					return workspace.Agent{}, err
				}
				return c.agents.Primary(ctx, args)
			})
		},
		func() error {
			return registerProcess(c, r, "task.create", "Create a persistent shared goal without sending it to an agent.", []string{"task.write"}, c.collaboration.CreateTask)
		},
		func() error {
			return registerProcess(c, r, "task.update", "Update shared goal/notes/status with a revision check.", []string{"task.write"}, c.collaboration.UpdateTask)
		},
		func() error {
			return registerProcess(c, r, "task.list", "Inspect persistent shared goals.", []string{"read"}, func(ctx context.Context, args InspectArgs) (Tasks, error) {
				s, err := c.Snapshot(args.WorkspaceID)
				return Tasks{s.Tasks}, err
			})
		},
		func() error {
			return registerProcess(c, r, "context.capture", "Capture a note, file range, tracked diff, or process output; never auto-deliver.", []string{"context.write"}, c.collaboration.Capture)
		},
		func() error {
			return registerProcess(c, r, "context.refresh", "Recapture a source into the same context identity with a new revision.", []string{"context.write"}, c.collaboration.RefreshContext)
		},
		func() error {
			return registerProcess(c, r, "context.list", "Inspect captured context and source freshness.", []string{"read"}, func(ctx context.Context, args InspectArgs) (Contexts, error) {
				if err := c.Refresh(ctx, "", command.Caller(ctx)); err != nil {
					return Contexts{}, err
				}
				s, err := c.Snapshot(args.WorkspaceID)
				return Contexts{s.Contexts}, err
			})
		},
		func() error {
			return registerProcess(c, r, "context.export", "Export explicitly selected context; reject stale sources unless allowed.", []string{"read"}, c.collaboration.Export)
		},
		func() error {
			return registerProcess(c, r, "question.ask", "Ask a shared persistent question; any participant can inspect it.", []string{"question.write"}, c.collaboration.Ask)
		},
		func() error {
			return registerProcess(c, r, "question.answer", "Answer a shared/provider question exactly once with a revision check.", []string{"question.write"}, func(ctx context.Context, args collab.AnswerArgs) (workspace.Question, error) {
				if strings.HasPrefix(args.QuestionID, "provider_") {
					return c.agents.Answer(ctx, args)
				}
				return c.collaboration.Answer(ctx, args)
			})
		},
		func() error {
			return registerProcess(c, r, "question.list", "Inspect shared questions and pending native provider input.", []string{"read"}, func(ctx context.Context, args InspectArgs) (Questions, error) {
				if err := c.Refresh(ctx, "", command.Caller(ctx)); err != nil {
					return Questions{}, err
				}
				s, err := c.Snapshot(args.WorkspaceID)
				return Questions{s.Questions}, err
			})
		},
		func() error {
			return registerProcess(c, r, "agent.start", "Start a native Codex worker in a persistent named terminal; resume only an explicit thread identity.", []string{"agent.start"}, c.agents.Start)
		},
		func() error {
			return registerProcess(c, r, "agent.list", "Inspect native thread identity, conversation, status, capabilities and questions.", []string{"read"}, func(ctx context.Context, args InspectArgs) (Agents, error) {
				if err := c.Refresh(ctx, "", command.Caller(ctx)); err != nil {
					return Agents{}, err
				}
				s, err := c.Snapshot(args.WorkspaceID)
				return Agents{s.Agents}, err
			})
		},
		func() error {
			return registerProcess(c, r, "agent.send", "Deliver a message plus explicitly selected fresh context; returns a durable delivery receipt.", []string{"agent.send"}, c.agents.Send)
		},
		func() error {
			return registerProcess(c, r, "agent.interrupt", "Interrupt this native agent's exact active turn without closing other terminals.", []string{"agent.interrupt"}, c.agents.Interrupt)
		},
		func() error {
			return registerProcess(c, r, "delivery.inspect", "Inspect the exact text/context sent and its accepted/unknown/failed receipt.", []string{"read"}, c.agents.Delivery)
		},
		func() error {
			return registerProcess(c, r, "delivery.list", "List recent delivery receipts without large text payloads; inspect by ID for exact content.", []string{"read"}, c.agents.Deliveries)
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
