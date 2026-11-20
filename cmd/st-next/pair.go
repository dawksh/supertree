package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/dawksh/supertree/internal/agent"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/workspace"
	"golang.org/x/term"
)

const pairUsage = `Pair in the usual worktree agent tab:
  st pair run [--thread <exact-Codex-thread-id>] [--task <id>] [--model <name>]
  st pair status
  st pair send --message <text> [--context <id> ...]
  st pair interrupt

New paired-mode tabs start automatically. To migrate a running terminal CLI,
exit it first, then run st pair run in that same tab with its exact thread ID.
No running programs are replaced. /interrupt stops a turn; /quit returns to shell.
`

func pairCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := fmt.Fprint(stdout, pairUsage)
		return err
	}
	options, err := environment()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet("pair "+args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	ws := f.String("workspace", "", "workspace ID")
	f.Bool("json", true, "JSON result")
	var bind agent.BindArgs
	var send agent.SendArgs
	var contexts stringFlags
	switch args[0] {
	case "run":
		f.StringVar(&bind.ThreadID, "thread", "", "exact conversation to resume")
		f.StringVar(&bind.TaskID, "task", "", "shared task")
		f.StringVar(&bind.Model, "model", "", "provider model")
	case "send":
		f.StringVar(&send.Message, "message", "", "message")
		f.Var(&contexts, "context", "explicit context ID (repeatable)")
	case "status", "interrupt":
	default:
		return &command.Error{Code: "unknown_command", Message: "unknown pair action; run st pair help"}
	}
	if err := f.Parse(args[1:]); err != nil {
		return &command.Error{Code: "invalid_arguments", Message: err.Error()}
	}
	if f.NArg() != 0 {
		return &command.Error{Code: "invalid_arguments", Message: "unexpected positional arguments"}
	}
	if args[0] == "run" {
		file, ok := stdout.(*os.File)
		if !ok || !term.IsTerminal(int(file.Fd())) || !term.IsTerminal(int(os.Stdin.Fd())) || os.Getenv("TMUX_PANE") == "" {
			return &command.Error{Code: "terminal_required", Message: "run st pair run in the worktree's agent tab after exiting its CLI"}
		}
		if _, err := startDaemon(ctx, options, daemonSocket(options)); err != nil {
			return err
		}
	}
	if *ws == "" {
		*ws, err = currentWorkspace(ctx, options)
		if err != nil {
			return err
		}
	}
	call := func(name string, args any) (json.RawMessage, error) {
		raw, _ := json.Marshal(args)
		return rpc.Call(ctx, daemonSocket(options), name, raw, clientMeta(options))
	}
	if args[0] == "run" {
		bind.WorkspaceID, bind.PaneID = *ws, os.Getenv("TMUX_PANE")
		data, err := call("agent.bind", bind)
		deadline := time.Now().Add(5 * time.Second)
		for err != nil && time.Now().Before(deadline) {
			var coded *command.Error
			if !errors.As(err, &coded) || coded.Code != "busy" {
				break
			}
			if err := pause(ctx); err != nil {
				return err
			}
			data, err = call("agent.bind", bind)
		}
		if err != nil {
			return coreFeatureError(err)
		}
		var binding agent.Binding
		if err := json.Unmarshal(data, &binding); err != nil {
			return err
		}
		provider, err := exec.LookPath("codex")
		if err != nil {
			return err
		}
		// Provider subprocesses reach this core with an agent attribution label.
		os.Setenv("ST_NEXT_ACTOR", "agent:"+binding.Agent.ID)
		os.Setenv("ST_NEXT_SOCKET", daemonSocket(options))
		os.Setenv("ST_NEXT_TMUX_SOCKET", options.TmuxSocket)
		workerCtx, stop := signal.NotifyContext(ctx, syscall.SIGHUP)
		defer stop()
		return agent.RunWorker(workerCtx, agent.WorkerOptions{ID: binding.Agent.ID, WorkspaceID: *ws, TaskID: binding.Agent.TaskID, Directory: binding.Directory, Socket: binding.Socket, StateFile: binding.StateFile, Model: binding.Agent.Model, ThreadID: binding.Agent.ThreadID, Executable: provider}, os.Stdin, stdout, os.Stderr)
	}
	data, err := call("agent.primary", agent.PrimaryArgs{WorkspaceID: *ws})
	if err != nil {
		return err
	}
	var primary workspace.Agent
	if err := json.Unmarshal(data, &primary); err != nil {
		return err
	}
	switch args[0] {
	case "send":
		send.AgentID, send.ContextIDs = primary.ID, contexts
		data, err = call("agent.send", send)
	case "interrupt":
		data, err = call("agent.interrupt", agent.Target{AgentID: primary.ID, ExpectedTurnID: primary.TurnID})
	}
	if err != nil {
		return err
	}
	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func coreFeatureError(err error) error {
	var coded *command.Error
	if errors.As(err, &coded) && coded.Code == "unknown_command" {
		return &command.Error{Code: "upgrade_required", Message: "restart the local core to load pairing/sharing: st next daemon stop; st next daemon start (terminal programs keep running)"}
	}
	return err
}
