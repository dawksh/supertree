package rpc_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/buffer"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/rpc"
)

func TestNativeMetadataRPCNeverInvokesLegacyTools(t *testing.T) {
	options, socket := fixture(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "legacy-invocations")
	for _, name := range []string{"tmux", "fzf", "codex", "claude", "opencode"} {
		script := "#!/bin/sh\nprintf 'invoked\\n' >> '" + strings.ReplaceAll(log, "'", "'\\''") + "'\nexit 93\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ST_AGENT_TOOLS", "0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rpc.ServeNative(ctx, options, socket, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("native server failed to stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	var status rpc.Status
	for {
		probe, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		raw, err := rpc.Call(probe, socket, "daemon.status", nil, rpc.Meta{})
		stop()
		if err == nil {
			if err := json.Unmarshal(raw, &status); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native server not ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.TmuxSocket != "" || !slices.Contains(status.Capabilities, "native-buffer-layouts-v1") {
		t.Fatalf("native capabilities: %#v", status)
	}
	// A caller's stale legacy socket setting does not enable legacy observation.
	meta := rpc.Meta{Directory: options.Directory, TmuxSocket: "/unused/legacy/socket", Actor: "human", RequestID: "native-metadata"}
	first := snapshot(t, socket, meta)
	if len(first.Workspaces) != 1 || first.TmuxStatus != "disabled" {
		t.Fatalf("native snapshot: %#v", first)
	}
	for _, method := range []string{"process.list", "process.start", "agent.start", "view.open", "layout.save", "layout.capture", "layout.restore"} {
		if _, err := rpc.Call(ctx, socket, method, nil, meta); err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("unsupported command %s: %v", method, err)
		}
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	messages := make(chan json.RawMessage, 8)
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- rpc.Stream(streamCtx, socket, nil, meta, func(data json.RawMessage) error {
			select {
			case messages <- append(json.RawMessage(nil), data...):
				return nil
			case <-streamCtx.Done():
				return streamCtx.Err()
			}
		})
	}()
	receive := func() json.RawMessage {
		t.Helper()
		select {
		case raw := <-messages:
			return raw
		case err := <-streamDone:
			t.Fatalf("native stream: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("native stream stalled")
		}
		return nil
	}
	_ = receive() // Subscription acknowledgement.
	id := first.Workspaces[0].ID
	doc := buffer.Document{Version: 1, WorkspaceID: id, Terminals: []buffer.Terminal{{ID: "editor"}}, Tabs: []buffer.Tab{{ID: "vim", Name: "Vim", Root: "vim-pane", Nodes: []buffer.Node{{ID: "vim-pane", Kind: "pane", TerminalID: "editor"}}}}, Loans: []buffer.Loan{}}
	params, _ := json.Marshal(core.BufferSave{Edits: []core.BufferEdit{{Buffer: doc}}})
	for _, bad := range []string{
		strings.Replace(string(params), `"terminal_id"`, `"Terminal_ID"`, 1),
		strings.Replace(string(params), `"version":1`, `"version":2`, 1),
		strings.Replace(string(params), `"kind":"pane"`, `"kind":"pane","unexpected":true`, 1),
	} {
		if _, err := rpc.Call(ctx, socket, "buffer.save", json.RawMessage(bad), meta); err == nil {
			t.Fatal("invalid buffer schema accepted")
		}
	}
	if _, err := rpc.Call(ctx, socket, "buffer.save", params, meta); err != nil {
		t.Fatal(err)
	}
	if raw := receive(); !strings.Contains(string(raw), "buffer.changed") {
		t.Fatalf("missing layout notification: %s", raw)
	}
	params, _ = json.Marshal(core.InspectArgs{WorkspaceID: id})
	raw, err := rpc.Call(ctx, socket, "buffer.inspect", params, meta)
	if err != nil {
		t.Fatal(err)
	}
	var saved core.Buffers
	if err := json.Unmarshal(raw, &saved); err != nil || len(saved.Buffers) != 1 || saved.Buffers[0].Revision != 1 {
		t.Fatalf("saved layout: %s %v", raw, err)
	}
	// Polling discovers a Git change without a tmux watch subprocess.
	git(t, options.Directory, "symbolic-ref", "HEAD", "refs/heads/native-poll")
	if raw := receive(); !strings.Contains(string(raw), "workspace.changed") {
		t.Fatalf("missing Git notification: %s", raw)
	}
	stopStream()
	select {
	case <-streamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("native stream did not cancel")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("native server invoked a legacy executable: %v", err)
	}
}
