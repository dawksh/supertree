package core_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/terminal"
	"github.com/dawksh/supertree/internal/workspace"
)

type nativeRestartFixture struct {
	registry                     *command.Registry
	options                      workspace.Options
	repo, tree, cwd, workspaceID string
	original                     terminal.Info
	definition                   terminal.Definition
}

func stoppedNativeTerminal(t *testing.T) nativeRestartFixture {
	t.Helper()
	options, repo := fixture(t)
	tree := filepath.Join(options.WorktreeRoot, "before move")
	git(t, repo, "worktree", "add", "-q", "-b", "restart-fixture", tree)
	cwd := filepath.Join(tree, "nested cwd")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	hostCtx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- terminal.ServeReady(hostCtx, filepath.Join(options.StateDir, "native-host"), func() { close(ready) })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("native fixture host did not stop")
		}
	})
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("native fixture host did not start")
	}
	ctx := context.Background()
	c, err := core.OpenNative(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	r := nativeRegistry(t, c)
	snapshot, _ := c.Snapshot("")
	id := ""
	for _, w := range snapshot.Workspaces {
		if w.Path == tree {
			id = w.ID
		}
	}
	if id == "" {
		t.Fatal("fixture worktree was not discovered")
	}
	definition := terminal.Definition{WorkspaceID: id, Executable: "/bin/sh", Args: []string{"-c", "exec sleep 60"}, Directory: cwd, Environment: map[string]string{"PATH": "/usr/bin:/bin"}, Columns: 80, Rows: 24}
	value, err := callNative(ctx, r, "terminal.launch", core.TerminalLaunchArgs{RequestID: "first", Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	f := nativeRestartFixture{registry: r, options: options, repo: repo, tree: tree, cwd: cwd, workspaceID: id, original: value.(terminal.Info), definition: definition}
	f.stop(t, f.original)
	return f
}

func (f nativeRestartFixture) restart(request string, generation int64) core.TerminalActionArgs {
	return core.TerminalActionArgs{WorkspaceID: f.workspaceID, RequestID: request, Target: terminal.Target{TerminalID: f.original.TerminalID, Generation: generation}}
}

func (f nativeRestartFixture) stop(t *testing.T, info terminal.Info) {
	t.Helper()
	args := f.restart("stop", info.Generation)
	args.Force = true
	if _, err := callNative(context.Background(), f.registry, "terminal.stop", args); err != nil {
		t.Fatal(err)
	}
}

func (f nativeRestartFixture) inventory(t *testing.T) terminal.Info {
	t.Helper()
	response, err := terminal.Call(context.Background(), filepath.Join(f.options.StateDir, "native-host"), terminal.Request{Op: "hello"})
	if err != nil || len(response.Status.Terminals) != 1 {
		t.Fatalf("native inventory: %v", err)
	}
	return response.Status.Terminals[0]
}

func (f nativeRestartFixture) moveAndReuseOldPath(t *testing.T) {
	t.Helper()
	git(t, f.repo, "worktree", "move", f.tree, filepath.Join(f.options.WorktreeRoot, "after move"))
	if err := os.MkdirAll(f.cwd, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRestartRejectsUnsafeWorkingDirectory(t *testing.T) {
	for _, name := range []string{"moved", "removed", "missing-directory", "symlink-outside"} {
		t.Run(name, func(t *testing.T) {
			f := stoppedNativeTerminal(t)
			code := "invalid_arguments"
			switch name {
			case "moved":
				f.moveAndReuseOldPath(t)
			case "removed":
				git(t, f.repo, "worktree", "remove", "--force", f.tree)
				if err := os.MkdirAll(f.cwd, 0700); err != nil {
					t.Fatal(err)
				}
				code = "workspace_unavailable"
			case "missing-directory":
				if err := os.Remove(f.cwd); err != nil {
					t.Fatal(err)
				}
			case "symlink-outside":
				if err := os.Remove(f.cwd); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.repo, f.cwd); err != nil {
					t.Fatal(err)
				}
			}
			// Do not manually refresh: a restart must not trust the cache while
			// discovery has yet to observe an external Git/filesystem change.
			_, err := callNative(context.Background(), f.registry, "terminal.restart", f.restart("unsafe", 1))
			var coded *command.Error
			if !errors.As(err, &coded) || coded.Code != code {
				t.Fatalf("unsafe restart: wanted %s, got %v", code, err)
			}
			current := f.inventory(t)
			if current.Generation != 1 || current.State != "exited" {
				t.Fatal("rejected restart changed the existing terminal")
			}
			s, err := store.Open(f.options.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			docs, err := s.Documents(context.Background(), "native-terminal-intent-v1")
			if err != nil || len(docs) != 1 {
				t.Fatal("rejected restart recorded a new launch intent")
			}
		})
	}
}

func TestNativePendingRestartRevalidatesBeforeDispatch(t *testing.T) {
	f := stoppedNativeTerminal(t)
	args := f.restart("pending", 1)
	// Model a controller interruption after durable intent but before host spawn.
	sum := sha256.Sum256([]byte(f.workspaceID + "\x00" + args.RequestID))
	id := fmt.Sprintf("launch_%x", sum[:16])
	launch := terminal.Launch{RequestID: id, TerminalID: f.original.TerminalID, Generation: 2, Definition: f.definition}
	intent := map[string]any{"version": 1, "epoch": f.original.HostEpoch, "operation": "restart", "launch": launch, "complete": false, "info": terminal.Info{}}
	s, err := store.Open(f.options.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument(context.Background(), "native-terminal-intent-v1", id, intent, 0); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()
	f.moveAndReuseOldPath(t)
	_, err = callNative(context.Background(), f.registry, "terminal.restart", args)
	var coded *command.Error
	if !errors.As(err, &coded) || coded.Code != "invalid_arguments" {
		t.Fatalf("pending restart used an obsolete cwd: %v", err)
	}
	if current := f.inventory(t); current.Generation != 1 || current.State != "exited" {
		t.Fatal("pending restart spawned after its owning worktree moved")
	}
}

func TestNativeRestartReceiptsRemainReconcilableAfterWorktreeMove(t *testing.T) {
	f := stoppedNativeTerminal(t)
	ctx := context.Background()
	args := f.restart("second", 1)
	value, err := callNative(ctx, f.registry, "terminal.restart", args)
	if err != nil {
		t.Fatal(err)
	}
	second := value.(terminal.Info)
	if second.State != "running" || second.Generation != 2 {
		t.Fatal("valid restart did not launch the next generation")
	}
	f.moveAndReuseOldPath(t)
	value, err = callNative(ctx, f.registry, "terminal.restart", args)
	if err != nil || value.(terminal.Info).PID != second.PID {
		t.Fatalf("executed restart could not be reconciled: %v", err)
	}
	// Return the tree so a third generation can be explicitly started there.
	if err := os.Remove(f.cwd); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.tree); err != nil {
		t.Fatal(err)
	}
	git(t, f.repo, "worktree", "move", filepath.Join(f.options.WorktreeRoot, "after move"), f.tree)
	f.stop(t, second)
	value, err = callNative(ctx, f.registry, "terminal.restart", f.restart("third", 2))
	if err != nil {
		t.Fatal(err)
	}
	third := value.(terminal.Info)
	f.moveAndReuseOldPath(t)
	value, err = callNative(ctx, f.registry, "terminal.restart", args)
	if err != nil || value.(terminal.Info).Generation != 2 || value.(terminal.Info).State != "exited" {
		t.Fatalf("historical restart receipt could not be reconciled: %v", err)
	}
	if current := f.inventory(t); current.Generation != 3 || current.PID != third.PID || current.State != "running" {
		t.Fatal("historical receipt lookup changed the current program")
	}
}
