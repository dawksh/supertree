// st-next is the opt-in, read-only Go workspace client.
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
	"github.com/dawksh/supertree/internal/workspace"
)

type inspectArgs struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type commandList struct {
	SchemaVersion int                  `json:"schema_version"`
	Commands      []command.Definition `json:"commands"`
}

func registry(options workspace.Options) (*command.Registry, error) {
	r := command.New()
	err := command.Register(r, command.Definition{Name: "command.list", Version: 1,
		Description: "Discover workspace commands and their argument and result schemas.",
		Effects:     []string{"read"}, Repeatable: true}, func(_ context.Context, _ struct{}) (commandList, error) {
		return commandList{SchemaVersion: command.SchemaVersion, Commands: r.Definitions()}, nil
	})
	if err != nil {
		return nil, err
	}
	err = command.Register(r, command.Definition{Name: "workspace.inspect", Version: 1,
		Description: "Take a read-only snapshot of registered repositories, the current checkout, and tmux sessions.",
		Effects:     []string{"read"}, Repeatable: true}, func(ctx context.Context, args inspectArgs) (workspace.Snapshot, error) {
		snapshot, err := workspace.Discover(ctx, options)
		if err != nil || args.WorkspaceID == "" {
			return snapshot, err
		}
		for _, w := range snapshot.Workspaces {
			if w.ID == args.WorkspaceID {
				snapshot.Workspaces = []workspace.Workspace{w}
				sessions := snapshot.Sessions[:0]
				for _, s := range snapshot.Sessions {
					for _, id := range w.SessionIDs {
						if s.ID == id {
							sessions = append(sessions, s)
							break
						}
					}
				}
				snapshot.Sessions = sessions
				return snapshot, nil
			}
		}
		return snapshot, &command.Error{Code: "not_found", Message: "workspace not found: " + args.WorkspaceID}
	})
	return r, err
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `st next — experimental read-only workspace commands

  st next inspect [--workspace <id>] [--json]
  st next commands [--json]
  st next invoke <command> [<JSON object>]

Uses ST_STATE, ST_WORKTREE_ROOT, and the current checkout. ST_NEXT_TMUX_SOCKET
selects a specific tmux socket; otherwise tmux uses its normal environment.
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	jsonOutput := args[0] == "invoke"
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
			fmt.Fprintf(stderr, "st next: %s\n", coded.Message)
		}
		return 1
	}
	var name string
	var raw json.RawMessage
	switch args[0] {
	case "inspect", "commands":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		asJSON := flags.Bool("json", false, "print structured output")
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
	options, err := environment()
	if err != nil {
		return fail(err)
	}
	r, err := registry(options)
	if err != nil {
		return fail(err)
	}
	result, err := r.Invoke(ctx, name, raw)
	if err != nil {
		return fail(err)
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
	return workspace.Options{StateDir: state, WorktreeRoot: root, Directory: directory, TmuxSocket: os.Getenv("ST_NEXT_TMUX_SOCKET")}, nil
}
