package buffer_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/dawksh/supertree/internal/buffer"
)

func applyEdit(t *testing.T, doc buffer.Document, change buffer.Change) buffer.Document {
	t.Helper()
	before := clone(doc)
	next, err := buffer.Edit(doc, change)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc, before) {
		t.Fatal("edit mutated the caller's document")
	}
	if err := buffer.ValidateSet([]buffer.Document{next}); err != nil {
		t.Fatal(err)
	}
	return next
}

func TestHideRestoreMoveAndResizePreserveTerminalIdentities(t *testing.T) {
	doc := document("tree")
	doc.Terminals = append(doc.Terminals, buffer.Terminal{ID: "agent"}, buffer.Terminal{ID: "shell"})
	doc.Tabs = append(doc.Tabs,
		buffer.Tab{ID: "agent", Name: "Codex", Root: "agent-pane", Nodes: []buffer.Node{{ID: "agent-pane", Kind: "pane", TerminalID: "agent"}}},
		buffer.Tab{ID: "shell", Name: "Shell", Root: "shell-pane", Nodes: []buffer.Node{{ID: "shell-pane", Kind: "pane", TerminalID: "shell"}}})
	original := clone(doc)
	doc = applyEdit(t, doc, buffer.Change{Operation: "hide", TabID: "shell", PaneID: "shell-pane"})
	doc = applyEdit(t, doc, buffer.Change{Operation: "hide", TabID: "editor", PaneID: "editor-pane"})
	doc = applyEdit(t, doc, buffer.Change{Operation: "place", TabID: "agent", PaneID: "vim-beside", TerminalID: "tree-editor", TargetPaneID: "agent-pane", SplitID: "side-by-side", Axis: "columns", Ratio: 60})
	geometry, err := buffer.Arrange(doc.Tabs[1], buffer.Rect{Width: 101, Height: 40}, "agent-pane", false, buffer.Size{Width: 2, Height: 2})
	if err != nil || len(geometry.Panes) != 2 || geometry.Panes[0].TerminalID != "agent" || geometry.Panes[0].Rect.Width != 60 || geometry.Panes[1].TerminalID != "tree-editor" {
		t.Fatalf("split does not show the original programs: %+v %v", geometry, err)
	}
	doc = applyEdit(t, doc, buffer.Change{Operation: "move", TabID: "agent", PaneID: "vim-beside", TargetPaneID: "agent-pane", SplitID: "vim-first", Axis: "rows", Ratio: 40, Before: true})
	doc = applyEdit(t, doc, buffer.Change{Operation: "resize", TabID: "agent", SplitID: "vim-first", Ratio: 70})
	doc = applyEdit(t, doc, buffer.Change{Operation: "reverse", TabID: "agent", SplitID: "vim-first"})
	geometry, err = buffer.Arrange(doc.Tabs[1], buffer.Rect{Width: 101, Height: 41}, "agent-pane", false, buffer.Size{Width: 2, Height: 2})
	if err != nil || geometry.Panes[0].TerminalID != "agent" || geometry.Panes[0].Rect.Height != 12 || geometry.Panes[1].TerminalID != "tree-editor" {
		t.Fatalf("move/reverse lost identity or proportion: %+v %v", geometry, err)
	}
	doc = applyEdit(t, doc, buffer.Change{Operation: "place", TabID: "shell", PaneID: "shell-restored", TerminalID: "shell"})
	if !reflect.DeepEqual(doc.Terminals, original.Terminals) || len(doc.Tabs) != 3 || doc.Tabs[0].Root != "" || doc.Tabs[2].Root != "shell-restored" {
		t.Fatal("display changes altered ownership, configured slots or the independent shell")
	}
	doc = applyEdit(t, doc, buffer.Change{Operation: "hide", TabID: "agent", PaneID: "vim-beside"})
	doc = applyEdit(t, doc, buffer.Change{Operation: "hide", TabID: "agent", PaneID: "agent-pane"})
	if doc.Tabs[1].Root != "" || len(doc.Tabs[1].Nodes) != 0 || !reflect.DeepEqual(doc.Terminals, original.Terminals) {
		t.Fatal("empty tab retained dangling splits or removed terminal ownership")
	}
}

func TestRepeatedNestedMovesRetainOnePlacementPerTerminal(t *testing.T) {
	doc := document("tree")
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("hidden-%d", i)
		doc.Terminals = append(doc.Terminals, buffer.Terminal{ID: id})
		doc = applyEdit(t, doc, buffer.Change{Operation: "place", TabID: "editor", PaneID: id, TerminalID: id, TargetPaneID: "editor-pane", SplitID: fmt.Sprintf("initial-%d", i), Axis: "columns", Ratio: 50})
	}
	for i := 0; i < 40; i++ {
		pane, target := fmt.Sprintf("hidden-%d", i%3), "editor-pane"
		axis := "columns"
		if i%2 == 1 {
			axis = "rows"
		}
		doc = applyEdit(t, doc, buffer.Change{Operation: "move", TabID: "editor", PaneID: pane, TargetPaneID: target, SplitID: fmt.Sprintf("move-%d", i), Axis: axis, Ratio: 35, Before: i%2 == 0})
		if len(doc.Tabs[0].Nodes) != 7 {
			t.Fatal("move leaked a removed split or pane")
		}
	}
}

func TestInvalidTabChangesLeaveOriginalUntouched(t *testing.T) {
	doc := document("tree")
	doc.Terminals = append(doc.Terminals, buffer.Terminal{ID: "hidden"})
	before, _ := json.Marshal(doc)
	for _, change := range []buffer.Change{
		{Operation: "hide", TabID: "missing", PaneID: "editor-pane"},
		{Operation: "hide", TabID: "editor", PaneID: "missing"},
		{Operation: "hide", TabID: "editor", PaneID: "editor-pane", TerminalID: "ignored"},
		{Operation: "place", TabID: "editor", PaneID: "another", TerminalID: "tree-editor", TargetPaneID: "editor-pane", SplitID: "split", Axis: "rows", Ratio: 50},
		{Operation: "place", TabID: "editor", PaneID: "another", TerminalID: "foreign", TargetPaneID: "editor-pane", SplitID: "split", Axis: "rows", Ratio: 50},
		{Operation: "place", TabID: "editor", PaneID: "editor-pane", TerminalID: "hidden", TargetPaneID: "editor-pane", SplitID: "split", Axis: "rows", Ratio: 50},
		{Operation: "place", TabID: "editor", PaneID: "hidden-pane", TerminalID: "hidden", TargetPaneID: "editor-pane", SplitID: "split", Axis: "rows", Ratio: 99},
		{Operation: "move", TabID: "editor", PaneID: "editor-pane", TargetPaneID: "editor-pane", SplitID: "split", Axis: "rows", Ratio: 50},
		{Operation: "resize", TabID: "editor", SplitID: "editor-pane", Ratio: 50},
		{Operation: "stop", TabID: "editor", PaneID: "editor-pane"},
	} {
		if _, err := buffer.Edit(doc, change); err == nil {
			t.Fatalf("invalid edit accepted: %+v", change)
		}
		after, _ := json.Marshal(doc)
		if string(before) != string(after) {
			t.Fatal("failed edit changed the original")
		}
	}
}

func TestTabEditsPreserveActiveLoanReceipts(t *testing.T) {
	a, b := document("a"), document("b")
	loan := buffer.Loan{ID: "loan", TerminalID: "a-editor", Source: buffer.Location{WorkspaceID: "a", TabID: "editor", PaneID: "editor-pane"}, Destination: buffer.Location{WorkspaceID: "b", TabID: "editor", PaneID: "borrowed"}}
	a.Loans = []buffer.Loan{loan}
	a.Tabs[0].Nodes[0].Kind, a.Tabs[0].Nodes[0].LoanID = "placeholder", loan.ID
	b.Tabs[0].Root = "split"
	b.Tabs[0].Nodes = append(b.Tabs[0].Nodes, buffer.Node{ID: "borrowed", Kind: "pane", TerminalID: "a-editor", LoanID: loan.ID}, buffer.Node{ID: "split", Kind: "split", Axis: "columns", Ratio: 50, Children: []string{"editor-pane", "borrowed"}})
	for _, test := range []struct {
		doc  buffer.Document
		pane string
	}{{a, "editor-pane"}, {b, "borrowed"}} {
		if _, err := buffer.Edit(test.doc, buffer.Change{Operation: "hide", TabID: "editor", PaneID: test.pane}); err == nil {
			t.Fatal("hide orphaned an active loan")
		}
	}
	next, err := buffer.Edit(b, buffer.Change{Operation: "move", TabID: "editor", PaneID: "borrowed", TargetPaneID: "editor-pane", SplitID: "moved", Axis: "rows", Ratio: 50, Before: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := buffer.ValidateSet([]buffer.Document{a, next}); err != nil || !reflect.DeepEqual(a.Loans, []buffer.Loan{loan}) {
		t.Fatalf("move changed loan location/ownership: %v", err)
	}
}
