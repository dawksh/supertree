package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/buffer"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/terminal"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestNativeLaunchIntentSurvivesControlRestartAndPendingReply(t *testing.T) {
	options, repo := fixture(t)
	rejectLegacyTools(t)
	dir := filepath.Join(options.StateDir, "native-host")
	hostCtx, cancelHost := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- terminal.ServeReady(hostCtx, dir, func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("host startup")
	}
	defer func() {
		cancelHost()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx := context.Background()
	c, err := core.OpenNative(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if c != nil {
			c.Close()
		}
	}()
	snapshot, _ := c.Snapshot("")
	workspaceID := snapshot.Workspaces[0].ID
	args := core.TerminalLaunchArgs{RequestID: "human-start", Definition: terminal.Definition{WorkspaceID: workspaceID, Executable: "/bin/sh", Args: []string{"-c", `printf x >> launch-count; exec sleep 60`}, Directory: repo, Environment: map[string]string{"PATH": "/usr/bin:/bin", "PRIVATE_TOKEN": "test-private-value"}, Columns: 80, Rows: 24}}
	r := nativeRegistry(t, c)
	const n = 8
	results := make(chan terminal.Info, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := callNative(ctx, r, "terminal.launch", args)
			if err != nil {
				errs <- err
				return
			}
			results <- value.(terminal.Info)
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var original terminal.Info
	for info := range results {
		if original.PID != 0 && original.PID != info.PID {
			t.Fatal("concurrent intents duplicated terminal")
		}
		original = info
	}
	if original.PID == 0 {
		t.Fatal("no terminal launched")
	}
	// Hiding and restoring a display is metadata only, including for a live
	// program. Its identity, generation, cwd and process survive both edits.
	doc := buffer.Document{Version: 1, WorkspaceID: workspaceID, Terminals: []buffer.Terminal{{ID: original.TerminalID}}, Tabs: []buffer.Tab{{ID: "agent", Name: "Codex", Root: "pane", Nodes: []buffer.Node{{ID: "pane", Kind: "pane", TerminalID: original.TerminalID}}}}, Loans: []buffer.Loan{}}
	if _, err := callNative(ctx, r, "buffer.save", core.BufferSave{Edits: []core.BufferEdit{{Buffer: doc}}}); err != nil {
		t.Fatal(err)
	}
	for i, change := range []buffer.Change{
		{Operation: "hide", TabID: "agent", PaneID: "pane"},
		{Operation: "place", TabID: "agent", PaneID: "restored", TerminalID: original.TerminalID},
	} {
		if _, err := callNative(ctx, r, "buffer.edit", core.BufferChange{WorkspaceID: workspaceID, ExpectedRevision: int64(i + 1), Change: change}); err != nil {
			t.Fatal(err)
		}
		status, err := terminal.Call(ctx, dir, terminal.Request{Op: "inspect", Epoch: original.HostEpoch, Target: &terminal.Target{TerminalID: original.TerminalID, Generation: original.Generation}})
		if err != nil || status.Info.PID != original.PID || status.Info.State != "running" {
			t.Fatal("display edit stopped or replaced a live program")
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(repo, "launch-count"))
		if string(b) == "x" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("launch marker")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	c = nil
	// Simulate the controller losing its final receipt commit after the host
	// has spawned: its durable intent remains pending across a control restart.
	s, err := store.Open(options.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := s.Documents(ctx, "native-terminal-intent-v1")
	if err != nil || len(docs) != 1 {
		t.Fatal("missing durable intent")
	}
	var intent map[string]any
	if err = json.Unmarshal(docs[0].Data, &intent); err != nil {
		t.Fatal(err)
	}
	intent["complete"] = false
	if _, err = s.PutDocument(ctx, "native-terminal-intent-v1", docs[0].ID, intent, docs[0].Revision); err != nil {
		t.Fatal(err)
	}
	s.Close()
	c, err = core.OpenNative(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	r = nativeRegistry(t, c)
	value, err := callNative(ctx, r, "terminal.launch", args)
	if err != nil {
		t.Fatal(err)
	}
	reconciled := value.(terminal.Info)
	if reconciled.PID != original.PID || reconciled.Generation != 1 || reconciled.HostEpoch != original.HostEpoch {
		t.Fatal("control restart replaced program")
	}
	b, _ := os.ReadFile(filepath.Join(repo, "launch-count"))
	if string(b) != "x" {
		t.Fatal("pending intent replayed")
	}
	args.Definition.Args = []string{"-c", "exit 0"}
	if _, err = callNative(ctx, r, "terminal.launch", args); err == nil {
		t.Fatal("request ID reused with changed definition")
	}
	action := core.TerminalActionArgs{WorkspaceID: workspaceID, RequestID: "stop-human", Target: terminal.Target{TerminalID: original.TerminalID, Generation: 1}, Force: true}
	if _, err = callNative(ctx, r, "terminal.stop", action); err != nil {
		t.Fatal(err)
	}
	action.RequestID = "restart-human"
	value, err = callNative(ctx, r, "terminal.restart", action)
	if err != nil {
		t.Fatal(err)
	}
	if value.(terminal.Info).Generation != 2 {
		t.Fatal("restart did not advance generation")
	}
	value2, err := callNative(ctx, r, "terminal.restart", action)
	if err != nil || value2.(terminal.Info).PID != value.(terminal.Info).PID {
		t.Fatal("restart request duplicated program")
	}
	value, err = callNative(ctx, r, "terminal.inspect", core.InspectArgs{WorkspaceID: workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "test-private-value") {
		t.Fatal("private launch environment leaked")
	}
}

func TestNativeLaunchValidationDoesNotStartHost(t *testing.T) {
	options, repo := fixture(t)
	c, err := core.OpenNative(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := nativeRegistry(t, c)
	snapshot, _ := c.Snapshot("")
	args := core.TerminalLaunchArgs{RequestID: "explicit", Definition: terminal.Definition{WorkspaceID: snapshot.Workspaces[0].ID, Executable: "/bin/sh", Directory: filepath.Dir(repo), Environment: map[string]string{}, Columns: 80, Rows: 24}}
	if _, err = callNative(context.Background(), r, "terminal.launch", args); err == nil {
		t.Fatal("foreign cwd accepted")
	}
	args.Definition.Directory = repo
	if _, err = callNative(context.Background(), r, "terminal.launch", args); err == nil {
		t.Fatal("launch implicitly started host")
	}
	if running, _ := terminal.Running(filepath.Join(options.StateDir, "native-host")); running {
		t.Fatal("validation started a host")
	}
	for _, definition := range r.Definitions() {
		if definition.Name == "terminal.launch" && len(definition.SensitiveArguments) == 0 {
			t.Fatal("missing sensitive argument metadata")
		}
	}
	if _, err = callNative(context.Background(), r, "terminal.launch", map[string]any{"request_id": "x", "unexpected": "bad"}); err == nil {
		t.Fatal("unknown launch fields accepted")
	}
	legacy, err := core.Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	registry, err := legacy.Registry(func(_ context.Context, args core.InspectArgs) (workspace.Snapshot, error) {
		return legacy.Snapshot(args.WorkspaceID)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range registry.Definitions() {
		if strings.HasPrefix(definition.Name, "terminal.") {
			t.Fatal("native terminal commands leaked into legacy core")
		}
	}
}
