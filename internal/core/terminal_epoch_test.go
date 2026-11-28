package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/terminal"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestNativePendingIntentCannotLaunchIntoAnotherHostEpoch(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	ctx := context.Background()
	options := workspace.Options{StateDir: filepath.Join(base, "state"), Directory: repo, WorktreeRoot: filepath.Join(base, "trees")}
	c, err := OpenNative(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	snapshot, _ := c.Snapshot("")
	ws := snapshot.Workspaces[0].ID
	args := TerminalLaunchArgs{RequestID: "previous-host-request", Definition: terminal.Definition{WorkspaceID: ws, Executable: "/bin/sh", Args: []string{"-c", "printf unintended > epoch-marker; sleep 30"}, Directory: repo, Environment: map[string]string{"PATH": "/usr/bin:/bin"}, Columns: 80, Rows: 24}}
	id := intentID(ws, args.RequestID)
	l := terminal.Launch{RequestID: id, TerminalID: "term_" + strings.TrimPrefix(id, "launch_"), Generation: 1, Definition: args.Definition}
	intent := terminalIntent{Version: 1, Epoch: "dead_host_epoch", Operation: "ensure", Launch: l}
	if _, err = c.store.PutDocument(ctx, terminalIntentKind, id, intent, 0); err != nil {
		t.Fatal(err)
	}
	hostCtx, cancelHost := context.WithCancel(ctx)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- terminal.ServeReady(hostCtx, c.terminalDir(), func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		cancelHost()
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		cancelHost()
		t.Fatal("host not ready")
	}
	defer func() {
		cancelHost()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 2; i++ {
		info, err := c.launchTerminal(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		if info.State != "lost" || info.PID != 0 {
			t.Fatal("pending intent was executed/adopted by another epoch")
		}
	}
	if _, err = os.Stat(filepath.Join(repo, "epoch-marker")); !os.IsNotExist(err) {
		t.Fatal("old launch definition was replayed")
	}
	response, err := terminal.Call(ctx, c.terminalDir(), terminal.Request{Op: "hello"})
	if err != nil || len(response.Status.Terminals) != 0 {
		t.Fatal("old intent populated a fresh host")
	}
	// A fresh, explicit human request is still allowed to launch independently.
	args.RequestID = "fresh-human-request"
	info, err := c.launchTerminal(ctx, args)
	if err != nil || info.PID == 0 || info.State != "running" {
		t.Fatal("fresh explicit launch failed")
	}
}
