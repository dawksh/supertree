package main

import (
	"context"
	"flag"
	"github.com/dawksh/supertree/internal/agent"
	"github.com/dawksh/supertree/internal/command"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func agentWorkerCommand(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP)
	defer stop()
	flags := flag.NewFlagSet("agent worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options agent.WorkerOptions
	flags.StringVar(&options.ID, "id", "", "agent ID")
	flags.StringVar(&options.WorkspaceID, "workspace", "", "workspace ID")
	flags.StringVar(&options.TaskID, "task", "", "task ID")
	flags.StringVar(&options.Directory, "cwd", "", "checkout directory")
	flags.StringVar(&options.Socket, "socket", "", "control socket")
	flags.StringVar(&options.StateFile, "state", "", "checkpoint file")
	flags.StringVar(&options.Model, "model", "", "provider model")
	flags.StringVar(&options.ThreadID, "thread", "", "exact resume identity")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if options.ID == "" || options.WorkspaceID == "" || options.Directory == "" || options.Socket == "" || options.StateFile == "" || flags.NArg() != 0 {
		return &command.Error{Code: "invalid_arguments", Message: "agent worker needs explicit identity, directory, socket and checkpoint file"}
	}
	provider, err := exec.LookPath("codex")
	if err != nil {
		return err
	}
	options.Executable = provider
	return agent.RunWorker(ctx, options, os.Stdin, os.Stdout, os.Stderr)
}
