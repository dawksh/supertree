package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/buffer"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/workspace"
)

func rejectLegacyTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "invocations")
	for _, name := range []string{"tmux", "fzf", "codex", "claude", "opencode"} {
		script := "#!/bin/sh\nprintf 'invoked\\n' >> '" + strings.ReplaceAll(log, "'", "'\\''") + "'\nexit 93\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ST_AGENT_TOOLS", "0")
	return log
}

func nativeRegistry(t *testing.T, c *core.Core) *command.Registry {
	t.Helper()
	r, err := c.Registry(func(ctx context.Context, args core.InspectArgs) (workspace.Snapshot, error) {
		if err := c.Refresh(ctx, "", command.Caller(ctx)); err != nil {
			return workspace.Snapshot{}, err
		}
		return c.Snapshot(args.WorkspaceID)
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func callNative(ctx context.Context, r *command.Registry, name string, value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return r.Invoke(ctx, name, raw)
}

func nativeDocument(id string, names ...string) buffer.Document {
	doc := buffer.Document{Version: 1, WorkspaceID: id, Terminals: []buffer.Terminal{}, Tabs: []buffer.Tab{}, Loans: []buffer.Loan{}}
	for _, name := range names {
		terminal := id + "-" + name
		doc.Terminals = append(doc.Terminals, buffer.Terminal{ID: terminal})
		doc.Tabs = append(doc.Tabs, buffer.Tab{ID: name, Name: name, Root: name + "-pane", Nodes: []buffer.Node{{ID: name + "-pane", Kind: "pane", TerminalID: terminal}}})
	}
	return doc
}

func TestNativeCoreHasNoLegacyRuntimeObservationOrCommands(t *testing.T) {
	options, repo := fixture(t)
	log := rejectLegacyTools(t)
	// Legacy documents must not be loaded by native startup or polling.
	s, err := store.Open(options.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"process", "agent", "task", "context", "question"} {
		if _, err := s.PutDocument(context.Background(), kind, "unread", "not-a-legacy-record", 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, err := core.OpenNative(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := nativeRegistry(t, c)
	for _, definition := range r.Definitions() {
		if !strings.HasPrefix(definition.Name, "buffer.") && !strings.HasPrefix(definition.Name, "terminal.") && definition.Name != "workspace.inspect" && definition.Name != "command.list" {
			t.Fatalf("unexpected native command %s", definition.Name)
		}
	}
	for _, register := range []func(*command.Registry) error{c.RegisterProcesses, c.RegisterViews, c.RegisterLayouts, c.RegisterCollaboration} {
		if err := register(command.New()); err == nil {
			t.Fatal("legacy registration accepted in native mode")
		}
	}
	if _, err := callNative(ctx, r, "workspace.inspect", core.InspectArgs{}); err != nil {
		t.Fatal(err)
	}
	ack, events, unsubscribe, err := c.Subscribe(core.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if len(ack.Snapshot.Workspaces) != 1 || ack.Snapshot.TmuxStatus != "disabled" || len(ack.Snapshot.Sessions) != 0 || len(ack.Snapshot.Processes) != 0 || len(ack.Snapshot.Agents) != 0 {
		t.Fatalf("native observation: %#v", ack.Snapshot)
	}
	pollCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	failures := make(chan error, 1)
	go func() {
		defer close(done)
		c.Poll(pollCtx, 10*time.Millisecond, func(err error) {
			select {
			case failures <- err:
			default:
			}
		})
	}()
	git(t, repo, "symbolic-ref", "HEAD", "refs/heads/changed")
	select {
	case <-events:
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Error("native poll missed Git change")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("native polling did not stop")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("native assembly invoked legacy executable: %v", err)
	}
}

func TestNativeBuffersPersistOrderLoansAndMissingWorkspaces(t *testing.T) {
	options, repo := fixture(t)
	linked := filepath.Join(options.WorktreeRoot, "feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", linked)
	log := rejectLegacyTools(t)
	ctx := command.WithOrigin(context.Background(), command.Origin{Actor: "human", RequestID: "arrange"})
	c, err := core.OpenNative(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if c != nil {
			c.Close()
		}
	}()
	r := nativeRegistry(t, c)
	snapshot, _ := c.Snapshot("")
	var a, b string
	for _, w := range snapshot.Workspaces {
		if w.Path == repo {
			a = w.ID
		} else {
			b = w.ID
		}
	}
	aDoc, bDoc := nativeDocument(a, "agent", "vim", "shell"), nativeDocument(b, "shell", "agent", "vim")
	bDoc.Terminals = append(bDoc.Terminals, buffer.Terminal{ID: b + "-tests"})
	bDoc.Tabs[0].Root = "tools-split"
	bDoc.Tabs[0].Nodes = append(bDoc.Tabs[0].Nodes,
		buffer.Node{ID: "tools-split", Kind: "split", Axis: "rows", Ratio: 60, Children: []string{"shell-pane", "tests-pane"}},
		buffer.Node{ID: "tests-pane", Kind: "pane", TerminalID: b + "-tests"})
	save := func(edits ...core.BufferEdit) (core.Buffers, error) {
		value, err := callNative(ctx, r, "buffer.save", core.BufferSave{Edits: edits})
		if err != nil {
			return core.Buffers{}, err
		}
		return value.(core.Buffers), nil
	}
	inspect := func(id string) core.SavedBuffer {
		t.Helper()
		value, err := callNative(ctx, r, "buffer.inspect", core.InspectArgs{WorkspaceID: id})
		if err != nil {
			t.Fatal(err)
		}
		return value.(core.Buffers).Buffers[0]
	}
	if initial := inspect(a); initial.Revision != 0 || len(initial.Buffer.Tabs) != 0 {
		t.Fatal("inspection created an arrangement")
	}
	if _, err := save(core.BufferEdit{Buffer: aDoc}, core.BufferEdit{Buffer: bDoc}); err != nil {
		t.Fatal(err)
	}
	// Moving a display is allowed; transferring the terminal's owner is not.
	copyDocument := func(doc buffer.Document) buffer.Document {
		raw, _ := json.Marshal(doc)
		var copy buffer.Document
		_ = json.Unmarshal(raw, &copy)
		return copy
	}
	transferredA, transferredB := copyDocument(aDoc), copyDocument(bDoc)
	transferredA.Terminals = []buffer.Terminal{{ID: a + "-agent"}, {ID: a + "-shell"}}
	transferredA.Tabs[1].Root, transferredA.Tabs[1].Nodes = "", []buffer.Node{}
	transferredB.Terminals = append(transferredB.Terminals, buffer.Terminal{ID: a + "-vim"})
	if _, err := save(core.BufferEdit{ExpectedRevision: 1, Buffer: transferredA}, core.BufferEdit{ExpectedRevision: 1, Buffer: transferredB}); err == nil {
		t.Fatal("layout edit transferred terminal ownership")
	}
	ack, events, unsub, err := c.Subscribe(core.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	loan := buffer.Loan{ID: "vim-loan", TerminalID: a + "-vim", Source: buffer.Location{WorkspaceID: a, TabID: "vim", PaneID: "vim-pane"}, Destination: buffer.Location{WorkspaceID: b, TabID: "borrowed", PaneID: "borrowed-pane"}}
	aDoc.Loans = append(aDoc.Loans, loan)
	aDoc.Tabs[1].Nodes[0].Kind, aDoc.Tabs[1].Nodes[0].LoanID = "placeholder", loan.ID
	bDoc.Tabs = append(bDoc.Tabs, buffer.Tab{ID: "borrowed", Name: "Borrowed Vim", Root: "borrowed-pane", Nodes: []buffer.Node{{ID: "borrowed-pane", Kind: "pane", TerminalID: loan.TerminalID, LoanID: loan.ID}}})
	if _, err := save(core.BufferEdit{ExpectedRevision: 1, Buffer: aDoc}); err == nil {
		t.Fatal("half loan saved")
	}
	if _, err := save(core.BufferEdit{ExpectedRevision: 1, Buffer: aDoc}, core.BufferEdit{ExpectedRevision: 0, Buffer: bDoc}); err == nil {
		t.Fatal("stale destination saved")
	}
	if inspect(a).Revision != 1 || inspect(b).Revision != 1 {
		t.Fatal("failed batch advanced a revision")
	}
	if _, err := save(core.BufferEdit{ExpectedRevision: 1, Buffer: aDoc}, core.BufferEdit{ExpectedRevision: 1, Buffer: bDoc}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Type != "buffer.changed" || len(event.WorkspaceIDs) != 2 || event.Sequence != ack.Sequence+1 || event.Actor != "human" {
			t.Fatalf("bad layout event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("no layout event")
	}
	beforeA, beforeB := inspect(a), inspect(b)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c = nil
	c, err = core.OpenNative(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	r = nativeRegistry(t, c)
	if !reflect.DeepEqual(inspect(a), beforeA) || !reflect.DeepEqual(inspect(b), beforeB) {
		t.Fatal("restart lost tab order, loan or revisions")
	}
	// Two clients editing the same revision cannot both win.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"human arrangement", "other arrangement"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			doc := copyDocument(bDoc)
			doc.Tabs[0].Name = name
			_, err := callNative(ctx, r, "buffer.save", core.BufferSave{Edits: []core.BufferEdit{{ExpectedRevision: 2, Buffer: doc}}})
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			var coded *command.Error
			if !errors.As(err, &coded) || coded.Code != "conflict" {
				t.Fatal(err)
			}
			conflicts++
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal("concurrent stale edit was not rejected")
	}
	git(t, repo, "worktree", "remove", "--force", linked)
	if err := c.Refresh(ctx, repo, core.Origin{Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	missing := inspect(b)
	if missing.Revision != 3 || len(missing.Buffer.Tabs) != 4 {
		t.Fatal("missing tree lost its saved arrangement")
	}
	found := false
	for _, ref := range inspect(a).References {
		if ref.WorkspaceID == b && ref.State == "missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("loan target missing state was hidden: %#v", inspect(a).References)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("native save invoked a legacy executable")
	}
}
