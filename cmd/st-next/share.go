package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dawksh/supertree/internal/agent"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/uitheme"
)

const shareUsage = `Share with the paired agent in this worktree:
  st share --file <path> [--start-line <n> --end-line <n>] --message <question>
  st share --diff --message <question>
  st share --output [--process <id>] --message <question>
  st share --note <text> --message <question>
  st share --context <id> [--context <id> ...] --message <question>
  st share --file <path> --preview

--output captures the current terminal's last 200 lines. --preview captures but
sends nothing; reuse its context IDs to deliver the reviewed snapshot. Stale
snapshots are rejected. --json returns the same structured result as context.share.
`

func shareCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help") {
		_, err := fmt.Fprint(stdout, shareUsage)
		return err
	}
	f := flag.NewFlagSet("share", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var share agent.ShareArgs
	var source agent.ShareSource
	var ids stringFlags
	var diff, output, asJSON bool
	f.StringVar(&share.WorkspaceID, "workspace", "", "workspace ID")
	f.StringVar(&share.Message, "message", "", "question or instruction")
	f.StringVar(&share.TaskID, "task", "", "shared goal (defaults to latest open goal)")
	f.StringVar(&source.Path, "file", "", "saved file path")
	f.IntVar(&source.StartLine, "start-line", 1, "first line")
	f.IntVar(&source.EndLine, "end-line", 0, "last line; 0 means end")
	f.BoolVar(&diff, "diff", false, "tracked Git diff")
	f.BoolVar(&output, "output", false, "terminal output snapshot")
	f.StringVar(&source.ProcessID, "process", "", "explicit output process")
	f.StringVar(&source.Text, "note", "", "note text")
	f.Var(&ids, "context", "existing context ID (repeatable)")
	f.BoolVar(&share.Preview, "preview", false, "capture/inspect without delivery")
	f.BoolVar(&asJSON, "json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return &command.Error{Code: "invalid_arguments", Message: err.Error()}
	}
	if f.NArg() != 0 {
		return &command.Error{Code: "invalid_arguments", Message: "share accepts flags; use --message for your question"}
	}
	count := 0
	f.Visit(func(v *flag.Flag) {
		if v.Name == "file" {
			source.Kind = "file"
			count++
		}
		if v.Name == "note" {
			source.Kind = "note"
			count++
		}
	})
	if diff {
		source.Kind = "diff"
		count++
	}
	if output {
		source.Kind = "output"
		count++
	}
	if count > 1 {
		return &command.Error{Code: "invalid_arguments", Message: "choose one source (--file, --diff, --output, or --note), plus optional context IDs"}
	}
	if source.ProcessID != "" && !output {
		return &command.Error{Code: "invalid_arguments", Message: "--process selects an --output source"}
	}
	if count == 1 {
		share.Source = &source
	}
	options, err := environment()
	if err != nil {
		return err
	}
	if _, err := startDaemon(ctx, options, daemonSocket(options)); err != nil {
		return err
	}
	if share.WorkspaceID == "" {
		share.WorkspaceID, err = currentWorkspace(ctx, options)
		if err != nil {
			return err
		}
	}
	if source.Kind == "file" && source.Path != "" && !filepath.IsAbs(source.Path) {
		source.Path = filepath.Join(options.Directory, source.Path)
	}
	share.PaneID, share.ContextIDs = os.Getenv("TMUX_PANE"), ids
	params, _ := json.Marshal(share)
	data, err := rpc.Call(ctx, daemonSocket(options), "context.share", params, clientMeta(options))
	if err != nil {
		return coreFeatureError(err)
	}
	if asJSON {
		var result any
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	var result agent.ShareResult
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	if result.Status == "preview" {
		_, err := fmt.Fprintf(stdout, "%s\n\n%s\n", uitheme.Clean(result.Text), uitheme.New().Row("info", "captured", strings.Join(result.ContextIDs, ", ")))
		return err
	}
	if result.Delivery == nil {
		return &command.Error{Code: "invalid_response", Message: "core returned no delivery receipt"}
	}
	if result.Status != "sent" {
		return &command.Error{Code: "delivery_unconfirmed", Message: fmt.Sprintf("delivery %s is %s; inspect its receipt before retrying", result.Delivery.ID, result.Status)}
	}
	_, err = fmt.Fprintln(stdout, uitheme.New().Row("ok", "shared", "with this worktree's agent · "+result.Delivery.ID))
	return err
}
