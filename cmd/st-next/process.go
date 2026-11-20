package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
	"golang.org/x/term"
)

const processUsage = `Usage:
  st next process list [--workspace <id>]
  st next process start --name <name> [--workspace <id>] [--kind shell|agent|tests|editor|server|terminal] [--cwd <directory>] [--env KEY=VALUE] -- <executable> [args...]
  st next process register --workspace <id> --name <name> --pane <pane-id>
  st next process stop <process-id>
  st next process restart <process-id>
  st next process attach <process-id> [--json]
  st next process output <process-id> [--lines 200]
  st next process move --process <id> [--target <id>] [--direction horizontal|vertical]

Process commands require a running daemon. Start it with st next daemon start.
Results are JSON; attach changes only the invoking human client's focus.
`

type envFlags map[string]string

func (e *envFlags) String() string { return "KEY=VALUE" }
func (e *envFlags) Set(value string) error {
	key, val, ok := strings.Cut(value, "=")
	if !ok {
		return fmt.Errorf("environment must use KEY=VALUE")
	}
	if *e == nil {
		*e = envFlags{}
	}
	(*e)[key] = val
	return nil
}

func currentWorkspace(ctx context.Context, options workspace.Options) (string, error) {
	data, err := rpc.Call(ctx, daemonSocket(options), "workspace.inspect", nil, clientMeta(options))
	if err != nil {
		return "", err
	}
	var snapshot workspace.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return "", err
	}
	dir := workspace.CanonicalPath(options.Directory)
	best := ""
	length := 0
	for _, w := range snapshot.Workspaces {
		rel, e := filepath.Rel(w.Path, dir)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && len(w.Path) > length {
			best, length = w.ID, len(w.Path)
		}
	}
	if best == "" {
		return "", &command.Error{Code: "invalid_arguments", Message: "select --workspace or run from a discovered checkout"}
	}
	return best, nil
}

func processCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := fmt.Fprint(stdout, processUsage)
		return err
	}
	options, err := environment()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("process "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "return attachment as data without focusing")
	var input any
	var start process.StartArgs
	var registration process.RegisterArgs
	var target process.Target
	var move process.MoveArgs
	var output process.OutputArgs
	var list inspectArgs
	var env envFlags
	remaining := args[1:]
	switch args[0] {
	case "start":
		flags.StringVar(&start.Name, "name", "", "process name")
		flags.StringVar(&start.WorkspaceID, "workspace", "", "workspace")
		flags.StringVar(&start.Kind, "kind", "terminal", "process kind")
		flags.StringVar(&start.Directory, "cwd", "", "start directory")
		flags.Var(&env, "env", "environment override")
	case "register":
		flags.StringVar(&registration.Name, "name", "", "process name")
		flags.StringVar(&registration.WorkspaceID, "workspace", "", "workspace")
		flags.StringVar(&registration.PaneID, "pane", "", "existing pane")
	case "list":
		flags.StringVar(&list.WorkspaceID, "workspace", "", "workspace")
	case "move":
		flags.StringVar(&move.ProcessID, "process", "", "source process")
		flags.StringVar(&move.TargetID, "target", "", "target process; omit to detach")
		flags.StringVar(&move.Direction, "direction", "horizontal", "split direction")
	case "stop", "restart", "attach", "output":
		if len(remaining) == 0 {
			return &command.Error{Code: "invalid_arguments", Message: "process ID is required"}
		}
		target.ProcessID = remaining[0]
		output.ProcessID = remaining[0]
		remaining = remaining[1:]
		if args[0] == "output" {
			flags.IntVar(&output.Lines, "lines", 200, "output lines")
		}
	default:
		return &command.Error{Code: "unknown_command", Message: "unknown process action: " + args[0]}
	}
	if err := flags.Parse(remaining); err != nil {
		return &command.Error{Code: "invalid_arguments", Message: err.Error()}
	}
	if args[0] != "start" && flags.NArg() != 0 {
		return &command.Error{Code: "invalid_arguments", Message: "unexpected process arguments"}
	}
	switch args[0] {
	case "start":
		start.Command, start.Environment = flags.Args(), env
		if start.WorkspaceID == "" {
			start.WorkspaceID, err = currentWorkspace(ctx, options)
			if err != nil {
				return err
			}
		}
		input = start
	case "register":
		if registration.WorkspaceID == "" {
			registration.WorkspaceID, err = currentWorkspace(ctx, options)
			if err != nil {
				return err
			}
		}
		input = registration
	case "list":
		input = list
	case "move":
		input = move
	case "output":
		input = output
	default:
		input = target
	}
	params, _ := json.Marshal(input)
	data, err := rpc.Call(ctx, daemonSocket(options), "process."+args[0], params, clientMeta(options))
	if err != nil {
		return err
	}
	if args[0] == "attach" && !*asJSON {
		if file, ok := stdout.(*os.File); ok && file == os.Stdout {
			var attachment process.Attachment
			if err := json.Unmarshal(data, &attachment); err != nil {
				return err
			}
			return attachTerminal(ctx, attachment)
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

func attachTerminal(ctx context.Context, a process.Attachment) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return &command.Error{Code: "terminal_required", Message: "attach needs an interactive terminal; use --json for attachment data"}
	}
	client := tmux.Client{Socket: a.Socket}
	// Selection is a local, explicit human action. The RPC merely returns the
	// target so agent callers can inspect it without stealing keyboard focus.
	if _, err := client.Run(ctx, "select-window", "-t", a.WindowID); err != nil {
		return err
	}
	if _, err := client.Run(ctx, "select-pane", "-t", a.PaneID); err != nil {
		return err
	}
	args := []string{"attach-session", "-t", a.SessionID}
	if current := os.Getenv("TMUX"); current != "" {
		socket, _, _ := strings.Cut(current, ",")
		if a.Socket == "" || workspace.CanonicalPath(socket) == workspace.CanonicalPath(a.Socket) {
			args = []string{"switch-client", "-t", a.SessionID}
		}
	}
	cmd := exec.CommandContext(ctx, "tmux", client.Args(args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
