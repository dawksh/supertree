package tui

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

// This test requires the isolated runner's tmux wrapper; never address a user's server.
func TestLayoutControlsThroughDaemonAndTmux(t *testing.T) {
	if os.Getenv("SUPERTREE_TEST_SANDBOX") == "" {
		t.Skip("requires scripts/test-local.sh")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	base := t.TempDir()
	if out, err := exec.Command("git", "-C", base, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git: %s %v", out, err)
	}
	sockdir, err := os.MkdirTemp(os.Getenv("SUPERTREE_TEST_SANDBOX"), "rpc-")
	if err != nil {
		t.Fatal(err)
	}
	opts := workspace.Options{Directory: base, StateDir: filepath.Join(base, "state"), WorktreeRoot: filepath.Join(base, "trees"), TmuxSocket: filepath.Join(sockdir, "tmux")}
	socket := filepath.Join(sockdir, "core")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, opts, socket, io.Discard) }()
	client := tmux.Client{Socket: opts.TmuxSocket}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not exit")
		}
		_, _ = client.Run(context.Background(), "kill-server")
	})
	call := func(name string, args any) json.RawMessage {
		t.Helper()
		if args == nil {
			args = struct{}{}
		}
		raw, _ := json.Marshal(args)
		b, e := rpc.Call(context.Background(), socket, name, raw, rpc.Meta{})
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		return b
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err = rpc.Call(context.Background(), socket, "daemon.status", nil, rpc.Meta{})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var snap workspace.Snapshot
	if err := json.Unmarshal(call("workspace.inspect", nil), &snap); err != nil {
		t.Fatal(err)
	}
	ws := snap.Workspaces[0].ID
	start := func(name string) workspace.Process {
		var p workspace.Process
		if err := json.Unmarshal(call("process.start", process.StartArgs{WorkspaceID: ws, Name: name, Command: []string{"sleep", "120"}}), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := start("a"), start("b")
	call("process.move", process.MoveArgs{ProcessID: b.ID, TargetID: a.ID, Direction: "horizontal"})
	run := func(args ...string) string {
		t.Helper()
		s, e := client.Run(context.Background(), args...)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	original := run("display-message", "-p", "-t", a.PaneID, "#{window_layout}")
	pid := run("display-message", "-p", "-t", a.PaneID, "#{pane_pid}")
	if err := json.Unmarshal(call("workspace.inspect", nil), &snap); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Context: context.Background(), WorkspaceID: ws, Invoke: func(ctx context.Context, name string, args any) (json.RawMessage, error) {
		raw, _ := json.Marshal(args)
		return rpc.Call(ctx, socket, name, raw, rpc.Meta{})
	}})
	m.state = snap
	m.workspaceID = ws
	m.processID = a.ID
	action := func(k string) {
		t.Helper()
		var cmd tea.Cmd
		m, cmd = key(m, runeKey(k))
		if cmd == nil {
			t.Fatalf("%s no inspect: %s", k, m.message)
		}
		next, _ := m.Update(cmd())
		m = next.(Model)
		if m.mode != "confirm" {
			t.Fatalf("%s no confirmation: %s", k, m.message)
		}
		m, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("no action")
		}
		result := cmd().(actionMsg)
		if result.Err != nil {
			t.Fatalf("%s: %v", k, result.Err)
		}
		next, _ = m.Update(result)
		m = next.(Model)
	}
	action("S")
	run("select-layout", "-t", a.PaneID, "even-vertical")
	changed := run("display-message", "-p", "-t", a.PaneID, "#{window_layout}")
	if changed == original {
		t.Fatal("layout did not change")
	}
	// Capture a restore token, then change tmux before confirming: no overwrite.
	var cmd tea.Cmd
	m, cmd = key(m, runeKey("R"))
	next, _ := m.Update(cmd())
	m = next.(Model)
	if m.mode != "confirm" {
		t.Fatalf("restore confirmation: %s", m.message)
	}
	run("resize-pane", "-t", a.PaneID, "-D", "2")
	staleTarget := run("display-message", "-p", "-t", a.PaneID, "#{window_layout}")
	if staleTarget == changed {
		t.Fatal("resize did not change live token")
	}
	m, cmd = key(m, tea.KeyMsg{Type: tea.KeyEnter})
	result := cmd().(actionMsg)
	if result.Err == nil {
		t.Fatal("stale restore accepted")
	}
	if got := run("display-message", "-p", "-t", a.PaneID, "#{window_layout}"); got != staleTarget {
		t.Fatal("stale restore changed layout")
	}
	next, _ = m.Update(result)
	m = next.(Model)
	action("R")
	if got := run("display-message", "-p", "-t", a.PaneID, "#{window_layout}"); got != original {
		t.Fatalf("restore: %s != %s", got, original)
	}
	if got := run("display-message", "-p", "-t", a.PaneID, "#{pane_pid}"); got != pid {
		t.Fatal("process restarted")
	}
}
