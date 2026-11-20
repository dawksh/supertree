package core

import (
	"context"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/workspace"
)

type ProcessList struct {
	Processes []workspace.Process `json:"processes"`
}

func registerProcess[A, R any](c *Core, r *command.Registry, name, description string, effects []string, handler func(context.Context, A) (R, error)) error {
	sensitive := []string{}
	if name == "process.start" {
		sensitive = append(sensitive, "environment")
	}
	return command.Register(r, command.Definition{Name: name, Version: 1, Description: description, Effects: effects, Repeatable: len(effects) == 1 && effects[0] == "read", SensitiveArguments: sensitive}, func(ctx context.Context, args A) (R, error) {
		mutating := !(len(effects) == 1 && effects[0] == "read")
		long := name == "agent.send" || name == "agent.interrupt" || name == "question.answer"
		if mutating && !long {
			c.refresh.Lock()
			defer c.refresh.Unlock()
		}
		result, err := handler(ctx, args)
		if mutating {
			var refreshErr error
			if long {
				refreshErr = c.Refresh(ctx, "", command.Caller(ctx))
			} else {
				refreshErr = c.refreshState(ctx, "", command.Caller(ctx))
			}
			if err == nil {
				err = refreshErr
			}
		}
		return result, err
	})
}

func (c *Core) RegisterProcesses(r *command.Registry) error {
	if err := registerProcess(c, r, "process.list", "List named processes, optionally in one workspace.", []string{"read"}, func(ctx context.Context, args InspectArgs) (ProcessList, error) {
		if err := c.Refresh(ctx, "", command.Caller(ctx)); err != nil {
			return ProcessList{}, err
		}
		s, err := c.Snapshot(args.WorkspaceID)
		return ProcessList{s.Processes}, err
	}); err != nil {
		return err
	}
	if err := registerProcess(c, r, "process.start", "Start a named program in a background tmux window using an argument array.", []string{"process.start"}, c.processes.Start); err != nil {
		return err
	}
	if err := registerProcess(c, r, "process.register", "Explicitly register an existing pane in its workspace; never relaunch it.", []string{"process.register"}, c.processes.Register); err != nil {
		return err
	}
	if err := registerProcess(c, r, "process.stop", "Close only this process's tagged terminal; other processes keep running.", []string{"process.stop"}, c.processes.Stop); err != nil {
		return err
	}
	if err := registerProcess(c, r, "process.restart", "Explicitly restart a stopped, exited or missing owned process.", []string{"process.start"}, c.processes.Restart); err != nil {
		return err
	}
	if err := registerProcess(c, r, "process.move", "Join this terminal beside a target process, or detach into its own window.", []string{"view.move"}, c.processes.Move); err != nil {
		return err
	}
	if err := registerProcess(c, r, "process.attach", "Return attachment coordinates without changing any client's focus.", []string{"read"}, c.processes.Attach); err != nil {
		return err
	}
	return registerProcess(c, r, "process.output", "Capture bounded terminal output without sending input to a program.", []string{"read"}, c.processes.Output)
}
