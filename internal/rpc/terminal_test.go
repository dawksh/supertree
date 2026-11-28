package rpc_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/terminal"
)

func TestNativeTerminalRPCSurvivesControlRestart(t *testing.T) {
	options, socket := fixture(t)
	hostCtx, cancelHost := context.WithCancel(context.Background())
	hostDone := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		hostDone <- terminal.ServeReady(hostCtx, filepath.Join(options.StateDir, "native-host"), func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-hostDone:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("host not ready")
	}
	t.Cleanup(func() {
		cancelHost()
		if err := <-hostDone; err != nil {
			t.Error(err)
		}
	})
	startControl := func() func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		closed := false
		go func() { done <- rpc.ServeNative(ctx, options, socket, io.Discard) }()
		stop := func() {
			if closed {
				return
			}
			closed = true
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("control did not stop")
			}
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(5 * time.Second)
		for {
			probe, end := context.WithTimeout(ctx, 100*time.Millisecond)
			_, err := rpc.Call(probe, socket, "daemon.status", nil, rpc.Meta{})
			end()
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("control not ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return stop
	}
	stopControl := startControl()
	ctx := context.Background()
	meta := rpc.Meta{Directory: options.Directory, Actor: "human"}
	w := snapshot(t, socket, meta).Workspaces[0]
	args := core.TerminalLaunchArgs{RequestID: "rpc-start", Definition: terminal.Definition{WorkspaceID: w.ID, Executable: "/bin/sh", Args: []string{"-c", `printf x >> rpc-once; stty -echo; printf 'RPC_READY\n'; while IFS= read -r line; do printf 'RPC_INPUT:%s\n' "$line"; done`}, Directory: w.Path, Environment: map[string]string{"PATH": "/usr/bin:/bin"}, Columns: 80, Rows: 24}}
	params, _ := json.Marshal(args)
	launch := func() terminal.Info {
		t.Helper()
		raw, err := rpc.Call(ctx, socket, "terminal.launch", params, meta)
		if err != nil {
			t.Fatal(err)
		}
		var info terminal.Info
		if err = json.Unmarshal(raw, &info); err != nil || info.PID == 0 {
			t.Fatal("invalid RPC terminal outcome")
		}
		return info
	}
	original := launch()
	attachment, err := terminal.DialAttachment(ctx, filepath.Join(options.StateDir, "native-host"), terminal.Target{TerminalID: original.TerminalID, Generation: original.Generation}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	awaitFrame := func(want string) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case frame, ok := <-attachment.Snapshots():
				if !ok {
					t.Fatal(attachment.Err())
				}
				var text strings.Builder
				for _, cell := range frame.Screen.Cells {
					text.WriteString(cell.Text)
				}
				if strings.Contains(text.String(), want) {
					if frame.Info.PID != original.PID {
						t.Fatal("control restart changed attached program")
					}
					return
				}
			case <-timer.C:
				t.Fatal("attached terminal stopped responding during control restart")
			}
		}
	}
	awaitFrame("RPC_READY")
	if _, err := rpc.Call(ctx, socket, "daemon.stop", nil, meta); err != nil {
		t.Fatal(err)
	}
	stopControl()
	if err := attachment.SendInput(ctx, nil, "while-control-off\r", false); err != nil {
		t.Fatal(err)
	}
	awaitFrame("RPC_INPUT:while-control-off")
	startControl()
	reconnected := launch()
	if reconnected.PID != original.PID || reconnected.HostEpoch != original.HostEpoch || reconnected.Generation != original.Generation {
		t.Fatal("RPC control restart replaced terminal")
	}
	if err := attachment.SendInput(ctx, nil, "after-control-restart\r", false); err != nil {
		t.Fatal(err)
	}
	awaitFrame("RPC_INPUT:after-control-restart")
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(w.Path, "rpc-once"))
		if string(b) == "x" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RPC launch was duplicated or never executed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	action := core.TerminalActionArgs{WorkspaceID: w.ID, RequestID: "rpc-stop", Target: terminal.Target{TerminalID: original.TerminalID, Generation: original.Generation}, Force: true}
	raw, _ := json.Marshal(action)
	if _, err := rpc.Call(ctx, socket, "terminal.stop", raw, meta); err != nil {
		t.Fatal(err)
	}
}
