package tui

import (
	"context"
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
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
