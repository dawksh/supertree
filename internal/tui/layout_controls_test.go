package tui

import (
	"context"
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/dawksh/supertree/internal/command"
	"strings"
	"testing"
	"time"
)

func TestCaptureRequiresAnchorConfirmation(t *testing.T) {
	m, _ := fixture(t)
	var calls []string
	m.options.Invoke = func(_ context.Context, name string, value any) (json.RawMessage, error) {
		calls = append(calls, name)
		if name == "layout.inspect" {
			return json.RawMessage(`{"workspace_id":"ws_one","revision":7}`), nil
		}
		data, _ := json.Marshal(value)
		if !strings.Contains(string(data), `"process_id":"proc_one"`) {
			t.Errorf("missing anchor: %s", data)
		}
		return json.RawMessage(`{}`), nil
	}
	m, cmd := key(m, runeKey("S"))
	next, cmd := m.Update(cmd())
	m = next.(Model)
	if m.mode != "confirm" || len(calls) != 1 {
		t.Fatalf("capture must confirm anchor, mode=%s calls=%v", m.mode, calls)
	}
	m, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no capture")
	}
	cmd()
}

func TestLayoutCaptureUsesInspectedRevision(t *testing.T) {
	m, _ := fixture(t)
	var names []string
	var args []map[string]any
	m.options.Invoke = func(_ context.Context, name string, value any) (json.RawMessage, error) {
		names = append(names, name)
		data, _ := json.Marshal(value)
		var v map[string]any
		_ = json.Unmarshal(data, &v)
		args = append(args, v)
		if name == "layout.inspect" {
			return json.RawMessage(`{"workspace_id":"ws_one","revision":7}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	m, cmd := key(m, runeKey("S"))
	if cmd == nil {
		t.Fatal("save did not inspect layout")
	}
	msg := cmd()
	next, cmd := m.Update(msg)
	m = next.(Model)
	if cmd == nil {
		t.Fatal("save did not schedule capture")
	}
	m, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	cmd()
	if len(names) != 2 || names[0] != "layout.inspect" || names[1] != "layout.capture" || args[1]["process_id"] != "proc_one" || args[1]["workspace_id"] != "ws_one" || args[1]["expected_revision"] != float64(7) {
		t.Fatalf("unexpected calls: %v %#v", names, args)
	}
}

func TestLayoutRestoreRequiresConfirmationAndShowsIntent(t *testing.T) {
	m, _ := fixture(t)
	var calls []string
	m.options.Invoke = func(_ context.Context, name string, value any) (json.RawMessage, error) {
		calls = append(calls, name)
		if name == "layout.live" {
			return json.RawMessage(`{"workspace_id":"ws_one","process_id":"proc_one","window_id":"@1","revision":3,"expected_layout":"token"}`), nil
		}
		if name == "layout.restore" {
			data, _ := json.Marshal(value)
			if string(data) != `{"expected_layout":"token","expected_revision":3,"workspace_id":"ws_one"}` {
				t.Errorf("wrong restore args: %s", data)
			}
		}
		return json.RawMessage(`{}`), nil
	}
	m, cmd := key(m, runeKey("R"))
	next, _ := m.Update(cmd())
	m = next.(Model)
	if m.mode != "confirm" || len(calls) != 1 || !strings.Contains(m.View(), "Restore saved layout") {
		t.Fatalf("missing restore confirmation: %s %v", m.View(), calls)
	}
	canceled, _ := key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if canceled.mode != "browse" || len(calls) != 1 {
		t.Fatal("cancel mutated")
	}
	_, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	cmd()
	if len(calls) != 2 || calls[1] != "layout.restore" {
		t.Fatalf("unexpected calls: %v", calls)
	}
}

func TestLayoutControlsAreDiscoverable(t *testing.T) {
	m, _ := fixture(t)
	m, _ = key(m, runeKey("?"))
	if !strings.Contains(m.View(), "S save layout") || !strings.Contains(m.View(), "R restore layout") {
		t.Fatal("layout keys missing from help")
	}
}

func TestLayoutCanceledInspectionCannotCompleteNewRequest(t *testing.T) {
	m, _ := fixture(t)
	m.options.Invoke = func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"workspace_id":"ws_one","revision":4}`), nil
	}
	m, old := key(m, runeKey("S"))
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = key(m, runeKey("S"))
	_, cmd := m.Update(old())
	if cmd != nil {
		t.Fatal("canceled inspection scheduled capture for a later request")
	}
}

func TestLayoutErrorsAreVisibleAndDoNotMutate(t *testing.T) {
	for _, response := range []string{`{}`, `{"workspace_id":"other","revision":1}`, `invalid`} {
		t.Run(response, func(t *testing.T) {
			m, _ := fixture(t)
			var calls []string
			m.options.Invoke = func(_ context.Context, name string, _ any) (json.RawMessage, error) {
				calls = append(calls, name)
				return json.RawMessage(response), nil
			}
			m, cmd := key(m, runeKey("S"))
			next, cmd := m.Update(cmd())
			m = next.(Model)
			if cmd != nil || len(calls) != 1 || !strings.Contains(m.View(), "layout.inspect failed") {
				t.Fatalf("bad inspection not shown: %v %s", calls, m.View())
			}
		})
	}
	m, _ := fixture(t)
	next, _ := m.Update(actionMsg{Name: "layout.restore", Err: fmt.Errorf("revision conflict")})
	if !strings.Contains(next.(Model).View(), "revision conflict") {
		t.Fatal("restore failure hidden")
	}
}

func TestLayoutRejectsUnmanagedAnchorAndInvalidLiveToken(t *testing.T) {
	m, _ := fixture(t)
	m.state.Processes[0].Source = "observed"
	if next, _ := key(m, runeKey("S")); !strings.Contains(next.message, "managed") || next.mode != "browse" {
		t.Fatal("observed anchor accepted")
	}
	m, _ = fixture(t)
	m.options.Invoke = func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"workspace_id":"ws_one","process_id":"proc_one","window_id":"@1","revision":3}`), nil
	}
	m, cmd := key(m, runeKey("R"))
	next, cmd := m.Update(cmd())
	m = next.(Model)
	if cmd != nil || m.mode != "browse" {
		t.Fatal("restore accepted missing token")
	}
}

func TestOldLayoutCompletionDoesNotDismissNewConfirmation(t *testing.T) {
	m, _ := fixture(t)
	m.options.Invoke = func(_ context.Context, name string, _ any) (json.RawMessage, error) {
		if name == "layout.inspect" {
			return json.RawMessage(`{"workspace_id":"ws_one","revision":1}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	m, cmd := key(m, runeKey("S"))
	next, _ := m.Update(cmd())
	m = next.(Model)
	m, old := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, cmd = key(m, runeKey("S"))
	next, _ = m.Update(cmd())
	m = next.(Model)
	next, _ = m.Update(old())
	m = next.(Model)
	if m.mode != "confirm" || m.pendingName != "layout.capture" {
		t.Fatalf("old response dismissed new dialog: %s %s", m.mode, m.pendingName)
	}
}
func TestLayoutInspectionWorkspaceChangedExitsWorking(t *testing.T) {
	m, _ := fixture(t)
	m.options.Invoke = func(context.Context, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"workspace_id":"ws_one","revision":1}`), nil
	}
	m, cmd := key(m, runeKey("S"))
	m.workspaceID = "other"
	next, _ := m.Update(cmd())
	m = next.(Model)
	if m.mode == "working" || !strings.Contains(m.message, "workspace") {
		t.Fatalf("stranded or missing error: %s %q", m.mode, m.message)
	}
}
func TestInvalidationWhileLoadingSurvivesTick(t *testing.T) {
	m, _ := fixture(t)
	m.loading = true
	next, _ := m.Update(invalidateMsg{})
	m = next.(Model)
	next, _ = m.Update(tickMsg(time.Now()))
	m = next.(Model)
	if !m.dirty {
		t.Fatal("tick discarded invalidation during load")
	}
	next, cmd := m.Update(stateMsg{State: m.state})
	m = next.(Model)
	if !m.loading || cmd == nil {
		t.Fatal("pending invalidation did not trigger reload")
	}
}

func TestWorkingKeyDoesNotAbandonAction(t *testing.T) {
	m, _ := fixture(t)
	m.options.Invoke = func(_ context.Context, name string, _ any) (json.RawMessage, error) {
		if name == "layout.inspect" {
			return json.RawMessage(`{"workspace_id":"ws_one","revision":1}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	m, cmd := key(m, runeKey("S"))
	next, _ := m.Update(cmd())
	m = next.(Model)
	m, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = key(m, runeKey("z"))
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.mode == "working" {
		t.Fatal("ignored key stranded completed action")
	}
}

func TestWorkspaceNormalizationCancelsInFlightLayout(t *testing.T) {
	m, _ := fixture(t)
	m.options.Invoke = func(context.Context, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"workspace_id":"ws_one","revision":1}`), nil
	}
	m, inspect := key(m, runeKey("S"))
	newState := m.state
	newState.Workspaces = nil
	newState.Processes = nil
	next, _ := m.Update(stateMsg{State: newState})
	m = next.(Model)
	if m.mode == "working" || !strings.Contains(m.message, "workspace") {
		t.Fatalf("normalization stranded action: %s %q", m.mode, m.message)
	}
	next, cmd := m.Update(inspect())
	if cmd != nil || next.(Model).mode == "confirm" {
		t.Fatal("stale inspect reopened confirmation")
	}
}

func TestWorkspaceNormalizationIgnoresInFlightActionCompletion(t *testing.T) {
	m, _ := fixture(t)
	m.options.Invoke = func(_ context.Context, name string, _ any) (json.RawMessage, error) {
		if name == "layout.inspect" {
			return json.RawMessage(`{"workspace_id":"ws_one","revision":1}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	m, cmd := key(m, runeKey("S"))
	next, _ := m.Update(cmd())
	m = next.(Model)
	m, action := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	state := m.state
	state.Workspaces = nil
	state.Processes = nil
	next, _ = m.Update(stateMsg{State: state})
	m = next.(Model)
	next, _ = m.Update(action())
	m = next.(Model)
	if !strings.Contains(m.message, "workspace") {
		t.Fatalf("late completion hid cancellation: %q", m.message)
	}
}

func TestRestoreEscapeAfterSubmissionShowsPartialFailure(t *testing.T) {
	m, _ := fixture(t)
	m.options.Invoke = func(_ context.Context, name string, _ any) (json.RawMessage, error) {
		if name == "layout.live" {
			return json.RawMessage(`{"workspace_id":"ws_one","process_id":"proc_one","window_id":"@1","revision":3,"expected_layout":"token"}`), nil
		}
		return nil, fmt.Errorf("partial_failure: inspect tmux before retrying")
	}
	m, cmd := key(m, runeKey("R"))
	next, _ := m.Update(cmd())
	m = next.(Model)
	m, action := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != "working" {
		t.Fatalf("escape dismissed submitted restore: %s", m.mode)
	}
	next, _ = m.Update(action())
	m = next.(Model)
	if m.mode != "browse" || !strings.Contains(m.View(), "partial_failure") {
		t.Fatalf("restore failure hidden: %s", m.View())
	}
}

func TestBackgroundFailureVisibleAfterNavigationWithoutDismissingDialog(t *testing.T) {
	m, _ := fixture(t)
	m.options.Invoke = func(_ context.Context, name string, _ any) (json.RawMessage, error) {
		if name == "layout.inspect" {
			return json.RawMessage(`{"workspace_id":"ws_one","revision":1}`), nil
		}
		return nil, fmt.Errorf("background start failed")
	}
	m, action := key(m, runeKey("s"))
	m, inspect := key(m, runeKey("S"))
	next, _ := m.Update(inspect())
	m = next.(Model)
	if m.mode != "confirm" {
		t.Fatal("expected newer dialog")
	}
	next, _ = m.Update(action())
	m = next.(Model)
	if m.mode != "confirm" || m.pendingName != "layout.capture" || !strings.Contains(m.View(), "background start failed") {
		t.Fatalf("lost failure or dialog: %s", m.View())
	}
}

func TestRestoreFailureVisibleAlongsideEarlierBackgroundFailure(t *testing.T) {
	m, _ := fixture(t)
	m.notice = "process.start failed: earlier failure"
	m.options.Invoke = func(_ context.Context, name string, _ any) (json.RawMessage, error) {
		if name == "layout.live" {
			return json.RawMessage(`{"workspace_id":"ws_one","process_id":"proc_one","window_id":"@1","revision":3,"expected_layout":"token"}`), nil
		}
		return nil, fmt.Errorf("partial_failure: inspect tmux")
	}
	m, cmd := key(m, runeKey("R"))
	next, _ := m.Update(cmd())
	m = next.(Model)
	m, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	next, _ = m.Update(cmd())
	m = next.(Model)
	if !strings.Contains(m.View(), "partial_failure") || !strings.Contains(m.View(), "earlier failure") {
		t.Fatal("one of the failures is hidden")
	}
}

func TestSubmittedRestoreSurvivesWorkspaceNormalization(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			m, _ := fixture(t)
			m.options.Invoke = func(_ context.Context, name string, _ any) (json.RawMessage, error) {
				if name == "layout.live" {
					return json.RawMessage(`{"workspace_id":"ws_one","process_id":"proc_one","window_id":"@1","revision":3,"expected_layout":"token"}`), nil
				}
				if failure {
					return nil, &command.Error{Code: "partial_failure", Message: "inspect live tmux before retry"}
				}
				return json.RawMessage(`{"revision":3}`), nil
			}
			m, cmd := key(m, runeKey("R"))
			next, _ := m.Update(cmd())
			m = next.(Model)
			m, action := key(m, tea.KeyMsg{Type: tea.KeyEnter})
			state := m.state
			state.Workspaces = nil
			state.Processes = nil
			next, _ = m.Update(stateMsg{State: state})
			m = next.(Model)
			if m.mode != "working" || strings.Contains(m.message, "canceled") {
				t.Fatalf("in-flight restore falsely canceled: %s %s", m.mode, m.message)
			}
			m, _ = key(m, tea.KeyMsg{Type: tea.KeyEsc})
			if m.mode != "working" {
				t.Fatal("escape dismissed submitted restore after workspace changed")
			}
			next, _ = m.Update(action())
			m = next.(Model)
			if m.mode != "browse" {
				t.Fatalf("restore not completed: %s", m.mode)
			}
			if failure {
				if !strings.Contains(m.View(), "inspect live tmux") {
					t.Fatal("partial failure hidden")
				}
			} else if !strings.Contains(m.View(), "layout.restore completed") {
				t.Fatal("restore success lost")
			}
		})
	}
}
func TestPartialFailureRetainedUntilExplicitAcknowledgement(t *testing.T) {
	m, _ := fixture(t)
	next, _ := m.Update(actionMsg{Name: "layout.restore", Err: &command.Error{Code: "partial_failure", Message: "inspect tmux before retry"}})
	m = next.(Model)
	// A later ordinary background error, status update, and navigation cannot hide it.
	next, _ = m.Update(actionMsg{Name: "process.start", Request: 999, Err: fmt.Errorf("background failed")})
	m = next.(Model)
	next, _ = m.Update(connectionMsg{Message: "reconnected"})
	m = next.(Model)
	m, _ = key(m, runeKey("?"))
	if !strings.Contains(m.View(), "inspect tmux before retry") || !strings.Contains(m.View(), "background failed") {
		t.Fatalf("lost warning or error: %s", m.View())
	}
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyCtrlE})
	if strings.Contains(m.View(), "inspect tmux before retry") {
		t.Fatal("acknowledgement did not dismiss warning")
	}
	if !strings.Contains(m.View(), "background failed") {
		t.Fatal("acknowledgement dismissed unrelated error")
	}
}

func TestPartialFailureWarningsQueueAndAcknowledgeInOrder(t *testing.T) {
	m, _ := fixture(t)
	for _, text := range []string{"first inspect tmux", "second inspect tmux"} {
		next, _ := m.Update(actionMsg{Name: "layout.restore", Request: 999, Err: fmt.Errorf("wrapped: %w", &command.Error{Code: "partial_failure", Message: text})})
		m = next.(Model)
	}
	if !strings.Contains(m.View(), "first inspect tmux") || !strings.Contains(m.View(), "2 pending") {
		t.Fatal("first warning or count missing")
	}
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyCtrlE})
	if !strings.Contains(m.View(), "second inspect tmux") || !strings.Contains(m.View(), "1 pending") {
		t.Fatal("second warning lost")
	}
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyCtrlE})
	if strings.Contains(m.View(), "RESTORE PARTIAL FAILURE") {
		t.Fatal("queue not acknowledged")
	}
}

func TestPartialFailureCannotBeAcknowledgedWhileTerminalTooSmall(t *testing.T) {
	m, _ := fixture(t)
	next, _ := m.Update(actionMsg{Name: "layout.restore", Err: &command.Error{Code: "partial_failure", Message: "inspect tmux before retry"}})
	m = next.(Model)
	for _, size := range []tea.WindowSizeMsg{{Width: 59, Height: 30}, {Width: 100, Height: 15}} {
		next, _ = m.Update(size)
		m = next.(Model)
		if strings.Contains(m.View(), "inspect tmux before retry") {
			t.Fatal("test requires warning to be hidden")
		}
		m, _ = key(m, tea.KeyMsg{Type: tea.KeyCtrlE})
		if len(m.criticalNotices) != 1 {
			t.Fatal("hidden warning was acknowledged")
		}
	}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	if !strings.Contains(m.View(), "inspect tmux before retry") {
		t.Fatal("warning missing after resize")
	}
	m, _ = key(m, tea.KeyMsg{Type: tea.KeyCtrlE})
	if len(m.criticalNotices) != 0 {
		t.Fatal("visible warning could not be acknowledged")
	}
}
