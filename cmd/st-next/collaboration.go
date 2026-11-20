package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/dawksh/supertree/internal/agent"
	"github.com/dawksh/supertree/internal/collab"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/rpc"
	"io"
)

type stringFlags []string

func (s *stringFlags) String() string         { return "value" }
func (s *stringFlags) Set(value string) error { *s = append(*s, value); return nil }

const collaborationUsage = `Shared workspace commands (results are JSON):
  st next task add --title <goal> [--notes <text>] [--workspace <id>]
  st next task list [--workspace <id>]
  st next task update <id> --revision <n> [--title <text>] [--notes <text>] [--status open|done]
  st next context add [--kind note|file|diff|output] [--file <path>] [--start-line <n>] [--end-line <n>] [--text <note>] [--process <id>] [--task <id>]
  st next context list [--workspace <id>]
  st next context refresh <id> [--revision <n>]
  st next context export [--task <id>] [--context <id>] [--allow-stale]
  st next agent start --name <name> [--task <id>] [--model <model>] [--thread <exact-id>]
  st next agent list [--workspace <id>]
  st next agent send <id> --message <text> [--context <id>] [--allow-stale]
  st next agent interrupt <id>
  st next question ask --prompt <text> [--option <text>] [--task <id>]
  st next question list [--workspace <id>]
  st next question answer <id> --revision <n> --answer <text>

Workspace defaults to the current checkout. Context capture never sends to agents.
Inspect exact deliveries with: st next invoke delivery.inspect '{"delivery_id":"..."}'
`

func collaborationCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) < 2 || args[1] == "help" || args[1] == "--help" {
		_, err := fmt.Fprint(stdout, collaborationUsage)
		return err
	}
	options, err := environment()
	if err != nil {
		return err
	}
	namespace, action := args[0], args[1]
	flags := flag.NewFlagSet(namespace+" "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Bool("json", true, "print JSON")
	ws := flags.String("workspace", "", "workspace ID")
	var create collab.TaskArgs
	var capture collab.CaptureArgs
	var native agent.StartArgs
	var send agent.SendArgs
	var ask collab.AskArgs
	var answer collab.AnswerArgs
	var target collab.ContextTarget
	var taskID, title, notes, status string
	var revision int64
	var contexts, choices stringFlags
	var stale bool
	var id string
	remaining := args[2:]
	method := namespace + "." + action
	switch method {
	case "task.add":
		method = "task.create"
		flags.StringVar(&create.Title, "title", "", "goal title")
		flags.StringVar(&create.Notes, "notes", "", "shared notes")
	case "task.update":
		if len(remaining) > 0 {
			id = remaining[0]
			remaining = remaining[1:]
		}
		flags.Int64Var(&revision, "revision", 0, "expected revision")
		flags.StringVar(&title, "title", "", "title")
		flags.StringVar(&notes, "notes", "", "notes")
		flags.StringVar(&status, "status", "", "open or done")
	case "context.add":
		method = "context.capture"
		flags.StringVar(&capture.Kind, "kind", "file", "note, file, diff, or output")
		flags.StringVar(&capture.Path, "file", "", "file path")
		flags.StringVar(&capture.Path, "path", "", "file path")
		flags.IntVar(&capture.StartLine, "start-line", 1, "first line")
		flags.IntVar(&capture.EndLine, "end-line", 0, "last line; defaults to end")
		flags.StringVar(&capture.Text, "text", "", "note text")
		flags.StringVar(&capture.Title, "title", "", "context label")
		flags.StringVar(&capture.ProcessID, "process", "", "process output source")
		flags.StringVar(&capture.TaskID, "task", "", "task ID")
	case "context.refresh":
		if len(remaining) > 0 {
			target.ContextID = remaining[0]
			remaining = remaining[1:]
		}
		flags.Int64Var(&target.ExpectedRevision, "revision", 0, "expected revision")
	case "context.export":
		flags.StringVar(&taskID, "task", "", "task ID")
		flags.Var(&contexts, "context", "selected context ID (repeatable)")
		flags.BoolVar(&stale, "allow-stale", false, "explicitly export stale captures")
	case "agent.start":
		flags.StringVar(&native.Name, "name", "codex", "named process")
		flags.StringVar(&native.TaskID, "task", "", "task ID")
		flags.StringVar(&native.Model, "model", "", "provider model")
		flags.StringVar(&native.ThreadID, "thread", "", "exact thread to resume")
	case "agent.send":
		if len(remaining) > 0 {
			send.AgentID = remaining[0]
			remaining = remaining[1:]
		}
		flags.StringVar(&send.Message, "message", "", "message text")
		flags.Var(&contexts, "context", "selected context ID (repeatable)")
		flags.BoolVar(&send.AllowStale, "allow-stale", false, "explicitly deliver stale captures")
	case "agent.interrupt":
		if len(remaining) > 0 {
			id = remaining[0]
			remaining = remaining[1:]
		}
	case "question.ask":
		flags.StringVar(&ask.Prompt, "prompt", "", "question")
		flags.StringVar(&ask.TaskID, "task", "", "task ID")
		flags.Var(&choices, "option", "answer option (repeatable)")
	case "question.answer":
		if len(remaining) > 0 {
			answer.QuestionID = remaining[0]
			remaining = remaining[1:]
		}
		flags.StringVar(&answer.Answer, "answer", "", "answer")
		flags.Int64Var(&answer.ExpectedRevision, "revision", 0, "expected revision")
	case "task.list", "context.list", "agent.list", "question.list":
	default:
		return &command.Error{Code: "unknown_command", Message: "unknown collaboration command: " + method}
	}
	if err := flags.Parse(remaining); err != nil {
		return &command.Error{Code: "invalid_arguments", Message: err.Error()}
	}
	if flags.NArg() != 0 {
		return &command.Error{Code: "invalid_arguments", Message: "unexpected positional arguments"}
	}
	needsWorkspace := method == "task.create" || method == "context.capture" || method == "context.export" || method == "agent.start" || method == "question.ask"
	if needsWorkspace && *ws == "" {
		*ws, err = currentWorkspace(ctx, options)
		if err != nil {
			return err
		}
	}
	var value any
	switch method {
	case "task.create":
		create.WorkspaceID = *ws
		value = create
	case "task.update":
		args := map[string]any{"task_id": id, "expected_revision": revision}
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "title":
				args["title"] = title
			case "notes":
				args["notes"] = notes
			case "status":
				args["status"] = status
			}
		})
		value = args
	case "context.capture":
		capture.WorkspaceID = *ws
		value = capture
	case "context.refresh":
		value = target
	case "context.export":
		if contexts == nil {
			contexts = []string{}
		}
		value = collab.ExportArgs{WorkspaceID: *ws, TaskID: taskID, ContextIDs: contexts, AllowStale: stale}
	case "agent.start":
		native.WorkspaceID = *ws
		value = native
	case "agent.send":
		send.ContextIDs = contexts
		value = send
	case "agent.interrupt":
		value = agent.Target{AgentID: id}
	case "question.ask":
		ask.WorkspaceID = *ws
		ask.Options = choices
		value = ask
	case "question.answer":
		value = answer
	default:
		value = inspectArgs{WorkspaceID: *ws}
	}
	params, _ := json.Marshal(value)
	data, err := rpc.Call(ctx, daemonSocket(options), method, params, clientMeta(options))
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}
