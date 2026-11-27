// st-next is the opt-in Go workspace client and local daemon.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/uitheme"
	"github.com/dawksh/supertree/internal/workspace"
	"golang.org/x/term"
)

type inspectArgs = core.InspectArgs
type commandList = core.CommandList

// Set by release builds; source and dev builds keep these defaults.
var version = "dev"
var commit = "unknown"

func registry(options workspace.Options) (*command.Registry, error) {
	return core.Registry(func(ctx context.Context, args core.InspectArgs) (workspace.Snapshot, error) {
		snapshot, err := workspace.Discover(ctx, options)
		if err != nil {
			return snapshot, err
		}
		return core.Select(snapshot, args.WorkspaceID)
	})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `st next — experimental workspace core

  st next --version
  st next inspect [--workspace <id>] [--json]
  st next commands [--json]
  st next invoke <command> [<JSON object>]
  st next daemon <start|serve|status|stop> [--json]
  st next terminal host <start|serve|status|stop> --state <private-dir>
  st next terminal <ensure|inventory|inspect|screen|stop|restart> --state <private-dir>
  st next events [--workspace <id>] [--epoch <epoch> --after <sequence>] [--json]
  st next process <list|start|register|stop|restart|attach|move|output> [args]
  st next tui [--workspace <id>]
  st next harness <codex|claude> -- [harness arguments]
  st next mcp [--harness <name>]
  st next <task|context|agent|question> <action> [args]
  st next pair <run|status|send|interrupt> [args]
  st next share [--file <path>|--diff|--output|--context <id>] --message <text>

Start the daemon explicitly for durable IDs and shared state. Without a daemon,
inspect/commands/invoke use read-only discovery. Prefix with --offline to force it.
ST_NEXT_SOCKET overrides the daemon socket and requires a running daemon.

Uses ST_STATE, ST_WORKTREE_ROOT, and the current checkout. ST_NEXT_TMUX_SOCKET
selects a specific tmux socket; otherwise tmux uses its normal environment.
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(stdout, "st-next %s (%s)\n", version, commit)
		return 0
	}
	offline := len(args) > 0 && args[0] == "--offline"
	if offline {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	jsonOutput := args[0] == "invoke" || args[0] == "events"
	for _, arg := range args[1:] {
		if arg == "--json" || arg == "--json=true" {
			jsonOutput = true
		}
	}
	fail := func(err error) int {
		var coded *command.Error
		if !errors.As(err, &coded) {
			coded = &command.Error{Code: "internal_error", Message: err.Error()}
		}
		if jsonOutput {
			_ = json.NewEncoder(stderr).Encode(struct {
				SchemaVersion int            `json:"schema_version"`
				Error         *command.Error `json:"error"`
			}{command.SchemaVersion, coded})
		} else {
			if file, ok := stderr.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
				theme := uitheme.New()
				fmt.Fprintln(stderr, theme.Row("error", "failed", coded.Message))
				fmt.Fprintln(stderr, theme.Row("info", "try", "st pair status · st control · st help"))
			} else {
				fmt.Fprintf(stderr, "st next: %s\n", coded.Message)
			}
		}
		return 1
	}
	if args[0] == "_terminal-host" || args[0] == "terminal" {
		if offline {
			return fail(&command.Error{Code: "invalid_arguments", Message: "native terminal operations require their explicit host namespace"})
		}
		var err error
		if args[0] == "_terminal-host" {
			err = terminalHostRole(ctx, args[1:])
		} else {
			err = terminalCommand(ctx, args[1:], os.Stdin, stdout)
		}
		if err != nil {
			return fail(err)
		}
		return 0
	}
	if args[0] == "_agent" {
		if err := agentWorkerCommand(ctx, args[1:]); err != nil {
			return fail(err)
		}
		return 0
	}
	if args[0] == "mcp" || args[0] == "harness" {
		if offline {
			return fail(&command.Error{Code: "invalid_arguments", Message: "harness integration requires the local core"})
		}
		var err error
		if args[0] == "mcp" {
			err = mcpCommand(ctx, args[1:], stdout)
		} else {
			err = harnessCommand(ctx, args[1:], stdout, stderr)
		}
		if err != nil {
			return fail(err)
		}
		return 0
	}
	if args[0] == "pair" {
		if offline {
			return fail(&command.Error{Code: "invalid_arguments", Message: "pairing requires the local core"})
		}
		if err := pairCommand(ctx, args[1:], stdout); err != nil {
			return fail(err)
		}
		return 0
	}
	if args[0] == "share" {
		if offline {
			return fail(&command.Error{Code: "invalid_arguments", Message: "sharing requires the local core"})
		}
		if err := shareCommand(ctx, args[1:], stdout); err != nil {
			return fail(err)
		}
		return 0
	}
	if args[0] == "task" || args[0] == "context" || args[0] == "agent" || args[0] == "question" {
		if offline {
			return fail(&command.Error{Code: "invalid_arguments", Message: "collaboration commands require a daemon"})
		}
		if err := collaborationCommand(ctx, args, stdout); err != nil {
			return fail(err)
		}
		return 0
	}
	if args[0] == "daemon" || args[0] == "events" {
		if offline {
			return fail(&command.Error{Code: "invalid_arguments", Message: "daemon and events require an online connection"})
		}
		if err := runtimeCommand(ctx, args, stdout, jsonOutput); err != nil {
			return fail(err)
		}
		return 0
	}
	if args[0] == "process" {
		if offline {
			return fail(&command.Error{Code: "invalid_arguments", Message: "process commands require a daemon"})
		}
		if err := processCommand(ctx, args[1:], stdout); err != nil {
			return fail(err)
		}
		return 0
	}
	if args[0] == "tui" {
		if offline {
			return fail(&command.Error{Code: "invalid_arguments", Message: "tui requires a daemon"})
		}
		if err := tuiCommand(ctx, args[1:], stdout); err != nil {
			return fail(err)
		}
		return 0
	}
	var name string
	var raw json.RawMessage
	switch args[0] {
	case "inspect", "commands":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		asJSON := flags.Bool("json", false, "print structured output")
		flags.BoolVar(&offline, "offline", offline, "inspect without the daemon")
		var id string
		if args[0] == "inspect" {
			flags.StringVar(&id, "workspace", "", "inspect one workspace ID")
		}
		if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		} else if err != nil {
			return fail(&command.Error{Code: "invalid_arguments", Message: err.Error()})
		}
		if flags.NArg() != 0 {
			return fail(&command.Error{Code: "invalid_arguments", Message: "unexpected positional arguments"})
		}
		jsonOutput = *asJSON
		name = "command.list"
		raw = json.RawMessage(`{}`)
		if args[0] == "inspect" {
			name = "workspace.inspect"
			raw, _ = json.Marshal(inspectArgs{WorkspaceID: id})
		}
	case "invoke":
		if len(args) < 2 || len(args) > 3 {
			return fail(&command.Error{Code: "invalid_arguments", Message: "usage: st next invoke <command> [<JSON object>]"})
		}
		name = args[1]
		if len(args) == 3 {
			raw = json.RawMessage(args[2])
		}
	default:
		return fail(&command.Error{Code: "unknown_command", Message: "unknown subcommand: " + args[0]})
	}
	if len(raw) != 0 && !json.Valid(raw) {
		return fail(&command.Error{Code: "invalid_arguments", Message: "command arguments must be valid JSON"})
	}
	options, err := environment()
	if err != nil {
		return fail(err)
	}
	var result any
	socket := daemonSocket(options)
	_, socketErr := os.Lstat(socket)
	online := !offline && (socketErr == nil || !os.IsNotExist(socketErr) || os.Getenv("ST_NEXT_SOCKET") != "")
	if online {
		if name == "event.subscribe" {
			err := rpc.Stream(ctx, socket, raw, clientMeta(options), func(data json.RawMessage) error { return json.NewEncoder(stdout).Encode(data) })
			if err != nil && !errors.Is(err, context.Canceled) {
				return fail(err)
			}
			return 0
		}
		data, err := rpc.Call(ctx, socket, name, raw, clientMeta(options))
		if err != nil {
			return fail(err)
		}
		switch name {
		case "workspace.inspect":
			var value workspace.Snapshot
			if err := json.Unmarshal(data, &value); err != nil {
				return fail(err)
			}
			result = value
		case "command.list":
			var value commandList
			if err := json.Unmarshal(data, &value); err != nil {
				return fail(err)
			}
			result = value
		default:
			result = data
		}
	} else {
		r, err := registry(options)
		if err != nil {
			return fail(err)
		}
		result, err = r.Invoke(ctx, name, raw)
		if err != nil {
			return fail(err)
		}
	}
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return fail(err)
		}
		return 0
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	switch value := result.(type) {
	case commandList:
		for _, d := range value.Commands {
			fmt.Fprintf(w, "%s\tv%d\t%s\n", d.Name, d.Version, d.Description)
		}
	case workspace.Snapshot:
		fmt.Fprintln(w, "ID\tWORKSPACE\tSTATE\tSESSIONS\tPATH")
		for _, item := range value.Workspaces {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", item.ID, item.Label, item.State, strings.Join(item.SessionIDs, ","), item.Path)
		}
		if len(value.Workspaces) == 0 {
			fmt.Fprintln(w, "No workspaces found.")
		}
		fmt.Fprintf(w, "\ntmux: %s (%d sessions)\n", value.TmuxStatus, len(value.Sessions))
		for _, warning := range value.Warnings {
			fmt.Fprintf(stderr, "st next: %s: %s: %s\n", warning.Code, warning.Path, warning.Message)
		}
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	return 0
}

func environment() (workspace.Options, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return workspace.Options{}, err
	}
	directory, err := os.Getwd()
	if err != nil {
		return workspace.Options{}, err
	}
	state := os.Getenv("ST_STATE")
	if state == "" {
		state = filepath.Join(homeDir, ".local", "state", "supertree")
	}
	root := os.Getenv("ST_WORKTREE_ROOT")
	if root == "" {
		root = filepath.Join(homeDir, "projects", ".worktrees")
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return workspace.Options{}, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return workspace.Options{}, err
	}
	state, root = workspace.CanonicalPath(state), workspace.CanonicalPath(root)
	return workspace.Options{StateDir: state, WorktreeRoot: root, Directory: directory, TmuxSocket: os.Getenv("ST_NEXT_TMUX_SOCKET")}, nil
}
