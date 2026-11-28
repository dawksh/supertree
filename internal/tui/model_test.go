package tui

import (
	"context"
	"encoding/json"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/dawksh/supertree/internal/agent"
	"github.com/dawksh/supertree/internal/collab"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/workspace"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Model, *[]string) {
	t.Helper()
	calls := []string{}
	m := New(Options{Context: context.Background(), Invoke: func(ctx context.Context, name string, args any) (json.RawMessage, error) {
		calls = append(calls, name)
		return json.RawMessage(`{}`), nil
	}})
	m.state = workspace.Snapshot{Epoch: "one", Sequence: 1, Workspaces: []workspace.Workspace{{ID: "ws_one", Label: "one/main"}}, Processes: []workspace.Process{{ID: "proc_one", WorkspaceID: "ws_one", Name: "shell", State: "running", Revision: 7}, {ID: "proc_two", WorkspaceID: "ws_one", Name: "tests", State: "running", Revision: 2}}}
	m.workspaceID, m.processID, m.connected, m.loading = "ws_one", "proc_one", true, false
	m.view = workspace.View{ID: "view_one", WorkspaceID: "ws_one", ProcessID: "proc_one", Mode: "browse", Revision: 1}
	return m, &calls
}
func key(m Model, key tea.KeyMsg) (Model, tea.Cmd) {
	next, cmd := m.Update(key)
	return next.(Model), cmd
}
func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestPairingSelectsNormalAgentAndDoesNotLaunchASecondOne(t *testing.T) {
	m, calls := fixture(t)
	m.state.Agents = []workspace.Agent{{ID: "experiment", WorkspaceID: "ws_one"}, {ID: "primary", WorkspaceID: "ws_one", Primary: true, Name: "codex"}}
	m.normalizeCollaboration()
	if m.agentID != "primary" {
		t.Fatal("pairing did not default to the normal agent tab")
	}
	m, cmd := key(m, runeKey("a"))
	if cmd != nil {
		cmd()
	}
	if m.agentID != "primary" || m.panel != "collaboration" || len(*calls) != 1 || (*calls)[0] != "view.present" {
		t.Fatalf("pairing spawned another agent: %v", *calls)
	}
	m, calls = fixture(t)
	m.state.Processes[0].Kind, m.state.Processes[0].Source = "agent", "observed"
	m, cmd = key(m, runeKey("a"))
	if cmd != nil || len(*calls) != 0 || !strings.Contains(m.message, "existing agent tab") {
		t.Fatal("ordinary CLI was implicitly replaced or duplicated")
	}
}

func TestStopRequiresExplicitConfirmationAndPreservesTarget(t *testing.T) {
	m, calls := fixture(t)
	m, _ = key(m, runeKey("x"))
	if m.mode != "confirm" || len(*calls) != 0 {
		t.Fatal("stop executed before confirmation")
	}
	next, cmd := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if next.mode != "working" || cmd == nil {
		t.Fatal("confirmation did not schedule command")
	}
	msg := cmd().(actionMsg)
	if msg.Name != "process.stop" || len(*calls) != 1 || (*calls)[0] != "process.stop" {
		t.Fatalf("wrong action: %#v %v", msg, *calls)
	}
	args := m.pendingArgs.(process.Target)
	if args.ProcessID != "proc_one" || args.ExpectedRevision != 7 {
		t.Fatal("confirmation lost explicit target/revision")
	}
}

func TestAgentSelectionAndHumanEditingDoNotRaceIntoCommands(t *testing.T) {
	m, calls := fixture(t)
	remote := m.view
	remote.Revision = 2
	remote.ProcessID = "proc_two"
	model, _ := m.Update(stateMsg{State: m.state, View: remote})
	m = model.(Model)
	if m.processID != "proc_two" {
		t.Fatal("semantic agent selection did not update TUI")
	}
	m, _ = key(m, runeKey("n"))
	m, _ = key(m, runeKey("investigation"))
	if m.fields[0] != "investigation" {
		t.Fatal("form input lost")
	}
	remote.Revision++
	remote.ProcessID = "proc_one"
	model, _ = m.Update(stateMsg{State: m.state, View: remote})
	m = model.(Model)
	if m.processID != "proc_two" || m.fields[0] != "investigation" {
		t.Fatal("remote update overwrote active human input")
	}
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != "browse" || len(*calls) != 0 {
		t.Fatal("cancel/reconnect ran a mutation")
	}
}

func TestPaletteUsesDiscoveredCommandsAndSmallTerminalIsSafe(t *testing.T) {
	m, calls := fixture(t)
	m.commands = []command.Definition{{Name: "custom.inspect", Description: "registered extension command", Effects: []string{"read"}, ArgumentSchema: map[string]any{"properties": map[string]any{}, "required": []any{}}}}
	m, _ = key(m, runeKey("p"))
	m, _ = key(m, runeKey("custom"))
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.pendingName != "custom.inspect" || m.mode != "arguments" {
		t.Fatal("palette bypassed registry discovery")
	}
	m, cmd := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("read command not scheduled")
	}
	msg := cmd().(actionMsg)
	if msg.Name != "custom.inspect" || (*calls)[0] != "custom.inspect" {
		t.Fatal("palette invoked a different command")
	}
	model, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	m = model.(Model)
	if !strings.Contains(m.View(), "enlarge terminal") {
		t.Fatal("small terminal has no usable message")
	}
	if strings.Contains(clean("safe\x1b]52;c;ZGFuZ2Vy\a\x1b[31mred\x1b[0m"), "\x1b") {
		t.Fatal("untrusted terminal escape was rendered")
	}
}

func TestPresentRedactsSensitiveArguments(t *testing.T) {
	m, _ := fixture(t)
	var presented core.ViewPresent
	m.options.Invoke = func(_ context.Context, name string, args any) (json.RawMessage, error) {
		presented = args.(core.ViewPresent)
		return json.RawMessage(`{}`), nil
	}
	m.mode, m.pendingName, m.input = "arguments", "process.start", `{"environment":{"TOKEN":"secret"}}`
	m.commands = []command.Definition{{Name: "process.start", SensitiveArguments: []string{"environment"}}}
	m.present()()
	if strings.Contains(presented.Input, "secret") {
		t.Fatal("sensitive input leaked into shared view state")
	}
}

func TestCollaborationSelectsExplicitContextAndNeverDefaultsApproval(t *testing.T) {
	m, _ := fixture(t)
	var sent any
	var method string
	m.options.Invoke = func(_ context.Context, name string, args any) (json.RawMessage, error) {
		method, sent = name, args
		return json.RawMessage(`{}`), nil
	}
	m.state.Agents = []workspace.Agent{{ID: "agent_one", WorkspaceID: "ws_one", Name: "pair", State: "idle"}}
	m.state.Contexts = []workspace.ContextItem{{ID: "ctx_one", WorkspaceID: "ws_one", Kind: "file", Title: "selected code"}}
	m.state.Questions = []workspace.Question{{ID: "provider_question", WorkspaceID: "ws_one", AgentID: "agent_one", Kind: "approval", Prompt: "Allow command?", Options: []string{"accept", "decline"}, Status: "open", Revision: 1}}
	m.normalize()
	m, _ = key(m, runeKey("c"))
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = key(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = key(m, runeKey("i"))
	if m.mode != "message-form" || m.fields[1] != "ctx_one" {
		t.Fatal("marked context not selected explicitly")
	}
	m, _ = key(m, runeKey("investigate this"))
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	_, cmd := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	cmd()
	args := sent.(agent.SendArgs)
	if method != "agent.send" || args.AgentID != "agent_one" || len(args.ContextIDs) != 1 || args.ContextIDs[0] != "ctx_one" {
		t.Fatal("delivery changed explicit targets")
	}
	m.mode = "browse"
	m.collabIndex = 2
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	method = ""
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if method != "" || m.mode != "answer-form" || m.fields[0] != "" {
		t.Fatal("approval defaulted to an answer")
	}
	m, _ = key(m, runeKey("decline"))
	_, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	cmd()
	answer := sent.(collab.AnswerArgs)
	if method != "question.answer" || answer.Answer != "decline" || answer.ExpectedRevision != 1 {
		t.Fatal("explicit approval answer was not preserved")
	}
}

func TestRealHarnessRequestsKeepAgentRepliesSeparateFromHumanInput(t *testing.T) {
	m, _ := fixture(t)
	m.width, m.height, m.panel = 120, 40, "collaboration"
	m.state.Contexts = []workspace.ContextItem{{ID: "ctx_one", WorkspaceID: "ws_one", Kind: "file", Title: "selected code"}}
	m.contextIDs = []string{"ctx_one"}
	var method string
	var sent any
	m.options.Invoke = func(_ context.Context, name string, args any) (json.RawMessage, error) {
		method, sent = name, args
		return json.RawMessage(`{}`), nil
	}
	m, _ = key(m, runeKey("i"))
	if overlay := m.collaborationOverlay(90, 25); m.mode != "message-form" || !strings.Contains(overlay, "ASK WORKTREE AGENT") || !strings.Contains(overlay, "shared request") || !strings.Contains(overlay, "Supertree questions") {
		t.Fatal("real harness handoff is not explained before sharing")
	}
	m, _ = key(m, runeKey("Explain this selection"))
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	_, cmd := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	cmd()
	request, ok := sent.(agent.ShareArgs)
	if !ok || method != "context.share" || request.Target != "harness" || len(request.ContextIDs) != 1 || request.ContextIDs[0] != "ctx_one" {
		t.Fatal("sharing launched another provider or lost selected context")
	}
	m.mode = "browse"
	m.state.Questions = []workspace.Question{{ID: "request", WorkspaceID: "ws_one", Recipient: "agent", Prompt: "Explain this selection", ContextIDs: []string{"ctx_one"}, Status: "open", Revision: 1}}
	if view := m.View(); strings.Contains(view, "need you") || !strings.Contains(view, "For agent") {
		t.Fatal("a request for the agent was presented as waiting for the human")
	}
	m.collabIndex = 1
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != "result" {
		t.Fatal("human was prompted to answer their own request")
	}
	m.mode = "browse"
	m.state.Questions[0].Status, m.state.Questions[0].Answer, m.state.Questions[0].AnsweredBy = "answered", "Agent explanation", "agent:codex"
	if view := m.View(); !strings.Contains(view, "Agent replied") || !strings.Contains(view, "Agent explanation") {
		t.Fatal("agent reply disappeared from the collaboration view")
	}
}
