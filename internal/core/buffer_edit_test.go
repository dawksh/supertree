package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/dawksh/supertree/internal/buffer"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
)

func TestNativeTabEditsAreRevisionCheckedAndPersistWithoutStartingRuntime(t *testing.T) {
	options, _ := fixture(t)
	log := rejectLegacyTools(t)
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
	r := nativeRegistry(t, c)
	snapshot, _ := c.Snapshot("")
	id := snapshot.Workspaces[0].ID
	doc := nativeDocument(id, "agent", "vim", "shell")
	doc.Terminals = append(doc.Terminals, buffer.Terminal{ID: "hidden"})
	if _, err := callNative(ctx, r, "buffer.save", core.BufferSave{Edits: []core.BufferEdit{{Buffer: doc}}}); err != nil {
		t.Fatal(err)
	}
	change := core.BufferChange{WorkspaceID: id, ExpectedRevision: 1, Change: buffer.Change{Operation: "place", TabID: "agent", PaneID: "extra", TerminalID: "hidden", TargetPaneID: "agent-pane", SplitID: "split", Axis: "columns", Ratio: 50}}
	if _, err := callNative(ctx, r, "buffer.edit", change); err != nil {
		t.Fatal(err)
	}
	if _, err := callNative(ctx, r, "buffer.edit", change); err == nil {
		t.Fatal("stale edit was silently replayed")
	} else {
		var coded *command.Error
		if !errors.As(err, &coded) || coded.Code != "conflict" {
			t.Fatalf("stale revision lost its structured conflict: %v", err)
		}
	}
	change.ExpectedRevision = 2
	change.Change = buffer.Change{Operation: "resize", TabID: "agent", SplitID: "split", Ratio: 70}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := callNative(ctx, r, "buffer.edit", change)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		var coded *command.Error
		if !errors.As(err, &coded) || coded.Code != "conflict" {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent same-revision edits committed %d times", successes)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c = nil
	c, err = core.OpenNative(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	r = nativeRegistry(t, c)
	value, err := callNative(ctx, r, "buffer.inspect", core.InspectArgs{WorkspaceID: id})
	if err != nil {
		t.Fatal(err)
	}
	saved := value.(core.Buffers).Buffers[0]
	if saved.Revision != 3 || !reflect.DeepEqual(saved.Buffer.Terminals, doc.Terminals) || !reflect.DeepEqual(saved.Buffer.Tabs[1:], doc.Tabs[1:]) {
		t.Fatal("persisted edit changed terminal ownership, another tab or revision")
	}
	geometry, err := buffer.Arrange(saved.Buffer.Tabs[0], buffer.Rect{Width: 101, Height: 40}, "agent-pane", false, buffer.Size{Width: 2, Height: 2})
	if err != nil || len(geometry.Panes) != 2 || geometry.Panes[0].Rect.Width != 70 {
		t.Fatalf("saved split geometry was not restored: %+v %v", geometry, err)
	}
	for _, path := range []string{log, filepath.Join(options.StateDir, "native-host")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("tab edit started a runtime or invoked legacy tools")
		}
	}
}
