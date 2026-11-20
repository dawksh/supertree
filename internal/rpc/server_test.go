package rpc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/workspace"
)

func git(t *testing.T, path string, args ...string) {
	t.Helper()
	if data, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, data)
	}
}

func fixture(t *testing.T) (workspace.Options, string) {
	t.Helper()
	base := workspace.CanonicalPath(t.TempDir())
	git(t, base, "init", "-q", "-b", "main")
	dir, err := os.MkdirTemp("/tmp", "st-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return workspace.Options{StateDir: filepath.Join(base, "state"), WorktreeRoot: filepath.Join(base, "trees"), Directory: base, TmuxSocket: filepath.Join(dir, "tmux")}, filepath.Join(dir, "core")
}

func start(t *testing.T, options workspace.Options, socket string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, options, socket, io.Discard) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("server: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("server did not shut down")
			}
		})
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probe, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := rpc.Call(probe, socket, "daemon.status", nil, rpc.Meta{})
		cancel()
		if err == nil {
			return stop
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
	return stop
}

func snapshot(t *testing.T, socket string, meta rpc.Meta) workspace.Snapshot {
	t.Helper()
	data, err := rpc.Call(context.Background(), socket, "workspace.inspect", nil, meta)
	if err != nil {
		t.Fatal(err)
	}
	var value workspace.Snapshot
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSharedDiscoveryEventsPollingRestartAndTmuxPreservation(t *testing.T) {
	options, socket := fixture(t)
	if _, err := exec.LookPath("tmux"); err == nil {
		cmd := exec.Command("tmux", "-S", options.TmuxSocket, "-f", "/dev/null", "new-session", "-d", "-s", "keep", "-c", options.Directory, "sleep 60")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tmux: %s %v", out, err)
		}
		t.Cleanup(func() { exec.Command("tmux", "-S", options.TmuxSocket, "kill-server").Run() })
	}
	stop := start(t, options, socket)
	first := snapshot(t, socket, rpc.Meta{})
	if len(first.Workspaces) != 1 || first.Epoch == "" {
		t.Fatalf("bad shared snapshot: %#v", first)
	}
	other := filepath.Join(t.TempDir(), "other checkout")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, other, "init", "-q", "-b", "other")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := make(chan json.RawMessage, 8)
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- rpc.Stream(ctx, socket, nil, rpc.Meta{Actor: "agent:review"}, func(data json.RawMessage) error {
			messages <- append(json.RawMessage(nil), data...)
			return nil
		})
	}()
	receive := func() json.RawMessage {
		t.Helper()
		select {
		case data := <-messages:
			return data
		case err := <-streamDone:
			t.Fatalf("stream ended: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("no subscription update")
		}
		return nil
	}
	var ack core.Subscription
	if err := json.Unmarshal(receive(), &ack); err != nil || !ack.ResnapshotRequired {
		t.Fatalf("no initial snapshot: %#v %v", ack, err)
	}
	second := snapshot(t, socket, rpc.Meta{Directory: other, Actor: "agent:review", RequestID: "discover-other"})
	if len(second.Workspaces) != 2 {
		t.Fatalf("second client's checkout not discovered: %#v", second)
	}
	var event core.Event
	if err := json.Unmarshal(receive(), &event); err != nil || event.Actor != "agent:review" || event.RequestID != "discover-other" || event.Sequence <= ack.Sequence {
		t.Fatalf("missing attribution: %#v %v", event, err)
	}
	third := snapshot(t, socket, rpc.Meta{})
	second.CapturedAt, third.CapturedAt = "", ""
	if !reflect.DeepEqual(second, third) {
		t.Fatal("clients obtained different workspace state")
	}
	// A manual Git change must be observed by polling, without an inspect call.
	git(t, options.Directory, "symbolic-ref", "HEAD", "refs/heads/manually-changed")
	if err := json.Unmarshal(receive(), &event); err != nil || event.Actor != "system" {
		t.Fatalf("manual change not observed: %#v %v", event, err)
	}
	cancel()
	select {
	case err := <-streamDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle subscription ignored cancellation")
	}
	_, err := rpc.Call(context.Background(), socket, "daemon.stop", nil, rpc.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if first.TmuxStatus == "running" {
		if out, err := exec.Command("tmux", "-N", "-S", options.TmuxSocket, "has-session", "-t", "keep").CombinedOutput(); err != nil {
			t.Fatalf("daemon stopped a terminal: %s %v", out, err)
		}
	}
	start(t, options, socket)
	restarted := snapshot(t, socket, rpc.Meta{})
	if len(restarted.Workspaces) != 2 || restarted.Epoch == first.Epoch {
		t.Fatalf("restart lost remembered checkouts: %#v", restarted)
	}
	for _, w := range restarted.Workspaces {
		if w.Path == first.Workspaces[0].Path && w.ID != first.Workspaces[0].ID {
			t.Fatal("restart changed workspace identity")
		}
	}
}

func TestProtocolValidationBatchesAndNotifications(t *testing.T) {
	options, socket := fixture(t)
	start(t, options, socket)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), rpc.MaxFrame)
	exchange := func(raw string) []byte {
		t.Helper()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, raw+"\n"); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("no response: %v", scanner.Err())
		}
		return append([]byte(nil), scanner.Bytes()...)
	}
	for _, tc := range []struct {
		raw  string
		code int
	}{
		{`{`, -32700},
		{`[]`, -32600},
		{`42`, -32600},
		{`{"jsonrpc":"1.0","id":1,"method":"command.list"}`, -32600},
		{`{"jsonrpc":"2.0","id":true,"method":"command.list"}`, -32600},
		{`{"jsonrpc":"2.0","id":"test","method":"missing"}`, -32601},
		{`{"jsonrpc":"2.0","id":"test","method":"workspace.inspect","params":{"workspace_id":null}}`, -32602},
		{`{"jsonrpc":"2.0","id":1,"method":"event.subscribe","params":{"after":-1}}`, -32602},
		{`{"jsonrpc":"2.0","id":1,"method":"event.subscribe","params":{"unexpected":1}}`, -32602},
	} {
		var response rpc.Response
		data := exchange(tc.raw)
		if err := json.Unmarshal(data, &response); err != nil || response.Error == nil || response.Error.Code != tc.code {
			t.Fatalf("%s => %s (%v)", tc.raw, data, err)
		}
	}
	// A notification gets no response; the following request is the next frame.
	_, _ = io.WriteString(conn, `{"jsonrpc":"2.0","method":"command.list"}`+"\n")
	data := exchange(`{"jsonrpc":"2.0","id":"after-notification","method":"command.list"}`)
	if !strings.Contains(string(data), `"id":"after-notification"`) {
		t.Fatalf("notification incorrectly replied: %s", data)
	}
	data = exchange(`[{"jsonrpc":"2.0","id":1,"method":"daemon.status"},{"jsonrpc":"2.0","method":"command.list"},false,{"jsonrpc":"2.0","id":2,"method":"event.subscribe"}]`)
	var batch []rpc.Response
	if err := json.Unmarshal(data, &batch); err != nil || len(batch) != 3 || batch[0].Error != nil || batch[1].Error.Code != -32600 || batch[2].Error.Code != -32602 {
		t.Fatalf("invalid batch handling: %s %v", data, err)
	}
	_, err = rpc.Call(context.Background(), socket, "workspace.inspect", nil, rpc.Meta{WorktreeRoot: "/different"})
	var coded *command.Error
	if !errors.As(err, &coded) || coded.Code != "configuration_mismatch" {
		t.Fatalf("configuration mismatch not detected: %v", err)
	}
	if _, err := rpc.Call(context.Background(), socket, "daemon.status", nil, rpc.Meta{WorktreeRoot: "/different"}); err != nil {
		t.Fatalf("changed config prevented diagnostics: %v", err)
	}
}

func TestEndpointOwnershipLockAndCrashRecovery(t *testing.T) {
	options, socket := fixture(t)
	endpoint, err := rpc.Listen(options.StateDir, socket)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := rpc.Listen(options.StateDir, socket+"-other"); err == nil {
		other.Close()
		t.Fatal("two sockets opened the same state")
	}
	running, err := rpc.Running(options.StateDir)
	if err != nil || !running {
		t.Fatalf("lock not reported: %v %v", running, err)
	}
	endpoint.Close()
	// Simulate a crash that releases its lock but leaves the Unix socket behind.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	listener.Close()
	endpoint, err = rpc.Listen(options.StateDir, socket)
	if err != nil {
		t.Fatalf("stale socket could not recover: %v", err)
	}
	endpoint.Close()
	if err := os.WriteFile(socket, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if endpoint, err := rpc.Listen(options.StateDir, socket); err == nil {
		endpoint.Close()
		t.Fatal("ordinary file was overwritten")
	}
	data, _ := os.ReadFile(socket)
	if string(data) != "keep" {
		t.Fatal("ordinary file changed")
	}
	if err := os.Chmod(filepath.Dir(socket), 0755); err != nil {
		t.Fatal(err)
	}
	if err := rpc.PrivateDir(filepath.Dir(socket)); err == nil {
		t.Fatal("shared socket directory accepted")
	}
}

func TestSocketPathStableAcrossStateCreationAndSymlinkAlias(t *testing.T) {
	base := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(alias, "not-created", "state")
	before := rpc.SocketPath(state)
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if before != rpc.SocketPath(state) || before != rpc.SocketPath(filepath.Join(base, "not-created", "state")) {
		t.Fatal("socket identity changed after first launch")
	}
}
