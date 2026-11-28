package buffer_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dawksh/supertree/internal/buffer"
)

func document(ws string) buffer.Document {
	return buffer.Document{Version: 1, WorkspaceID: ws, Terminals: []buffer.Terminal{{ID: ws + "-editor"}},
		Tabs: []buffer.Tab{{ID: "editor", Name: "Vim", Root: "editor-pane", Nodes: []buffer.Node{{ID: "editor-pane", Kind: "pane", TerminalID: ws + "-editor"}}}}, Loans: []buffer.Loan{}}
}

func clone[T any](value T) T {
	raw, _ := json.Marshal(value)
	var result T
	_ = json.Unmarshal(raw, &result)
	return result
}

func TestOwnershipAndAtomicLoanModel(t *testing.T) {
	a, b := document("a"), document("b")
	if err := buffer.ValidateSet([]buffer.Document{a, b}); err != nil {
		t.Fatal(err)
	}
	loan := buffer.Loan{ID: "loan-1", TerminalID: "a-editor", Source: buffer.Location{WorkspaceID: "a", TabID: "editor", PaneID: "editor-pane"}, Destination: buffer.Location{WorkspaceID: "b", TabID: "borrowed", PaneID: "borrowed-pane"}}
	a.Loans = []buffer.Loan{loan}
	a.Tabs[0].Nodes[0].Kind, a.Tabs[0].Nodes[0].LoanID = "placeholder", loan.ID
	b.Tabs = append(b.Tabs, buffer.Tab{ID: "borrowed", Name: "Borrowed Vim", Root: "borrowed-pane", Nodes: []buffer.Node{{ID: "borrowed-pane", Kind: "pane", TerminalID: "a-editor", LoanID: loan.ID}}})
	if err := buffer.ValidateSet([]buffer.Document{a, b}); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*buffer.Document, *buffer.Document){
		"no receipt":          func(a, b *buffer.Document) { a.Loans = nil },
		"missing source":      func(a, b *buffer.Document) { a.Tabs = nil },
		"missing destination": func(a, b *buffer.Document) { b.Tabs = b.Tabs[:1] },
		"duplicate display":   func(a, b *buffer.Document) { a.Tabs[0].Nodes[0].Kind = "pane" },
		"wrong terminal":      func(a, b *buffer.Document) { b.Tabs[1].Nodes[0].TerminalID = "b-editor" },
		"wrong source":        func(a, b *buffer.Document) { a.Loans[0].Source.WorkspaceID = "b" },
		"duplicate owner":     func(a, b *buffer.Document) { b.Terminals = append(b.Terminals, buffer.Terminal{ID: "a-editor"}) },
	} {
		t.Run(name, func(t *testing.T) {
			aa, bb := clone(a), clone(b)
			change(&aa, &bb)
			if err := buffer.ValidateSet([]buffer.Document{aa, bb}); err == nil {
				t.Fatal("invalid loan accepted")
			}
		})
	}
	// Returning both ends restores the same terminal ID; hiding its display is
	// also valid and never deletes its ownership declaration.
	a = document("a")
	b = document("b")
	a.Tabs[0].Root, a.Tabs[0].Nodes = "", []buffer.Node{}
	if err := buffer.ValidateSet([]buffer.Document{a, b}); err != nil {
		t.Fatal(err)
	}
}

func TestRejectMalformedDocumentsBeforeTraversal(t *testing.T) {
	for name, change := range map[string]func(*buffer.Document){
		"version":      func(d *buffer.Document) { d.Version = 2 },
		"missing node": func(d *buffer.Document) { d.Tabs[0].Root = "gone" },
		"cycle": func(d *buffer.Document) {
			d.Tabs[0].Nodes = []buffer.Node{{ID: "editor-pane", Kind: "split", Axis: "columns", Ratio: 50, Children: []string{"editor-pane", "editor-pane"}}}
		},
		"unreachable":       func(d *buffer.Document) { d.Tabs[0].Root = "" },
		"duplicate tab":     func(d *buffer.Document) { d.Tabs = append(d.Tabs, d.Tabs[0]) },
		"extra pane fields": func(d *buffer.Document) { d.Tabs[0].Nodes[0].Ratio = 50 },
		"title escape":      func(d *buffer.Document) { d.Tabs[0].Name = "Vim\x1b[2J" },
		"node bound":        func(d *buffer.Document) { d.Tabs[0].Nodes = make([]buffer.Node, buffer.MaxNodes+1) },
	} {
		t.Run(name, func(t *testing.T) {
			d := document("a")
			change(&d)
			if err := buffer.Validate(d); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
	d := document("a")
	raw, _ := json.Marshal(d)
	decoded, err := buffer.Decode(raw)
	if err != nil || !reflect.DeepEqual(d, decoded) {
		t.Fatalf("decode: %#v %v", decoded, err)
	}
	for _, data := range []string{
		strings.Replace(string(raw), `"workspace_id"`, `"Workspace_ID"`, 1),
		strings.Replace(string(raw), `"terminal_id"`, `"Terminal_ID"`, 1),
		strings.Replace(string(raw), `"kind":"pane"`, `"kind":"pane","unknown":true`, 1),
		string(raw) + ` {}`,
		strings.Repeat(" ", buffer.MaxDocumentBytes+1),
	} {
		if _, err := buffer.Decode([]byte(data)); err == nil {
			t.Fatal("malformed JSON accepted")
		}
	}
}

func TestSplitDepthAndRatioBounds(t *testing.T) {
	doc := document("a")
	doc.Tabs[0].Root = "split-0"
	doc.Tabs[0].Nodes = nil
	for i := 0; i < buffer.MaxDepth; i++ {
		doc.Tabs[0].Nodes = append(doc.Tabs[0].Nodes,
			buffer.Node{ID: fmt.Sprintf("split-%d", i), Kind: "split", Axis: "rows", Ratio: 50, Children: []string{fmt.Sprintf("split-%d", i+1), fmt.Sprintf("spare-%d", i)}},
			buffer.Node{ID: fmt.Sprintf("spare-%d", i), Kind: "pane", TerminalID: "a-editor"})
	}
	doc.Tabs[0].Nodes = append(doc.Tabs[0].Nodes, buffer.Node{ID: fmt.Sprintf("split-%d", buffer.MaxDepth), Kind: "pane", TerminalID: "a-editor"})
	if err := buffer.Validate(doc); err == nil {
		t.Fatal("excessive split depth accepted")
	}
	doc = document("a")
	doc.Tabs[0].Root = "split"
	doc.Tabs[0].Nodes = append(doc.Tabs[0].Nodes,
		buffer.Node{ID: "split", Kind: "split", Axis: "columns", Ratio: 4, Children: []string{"editor-pane", "other"}},
		buffer.Node{ID: "other", Kind: "pane", TerminalID: "a-editor"})
	if err := buffer.Validate(doc); err == nil {
		t.Fatal("out-of-bounds ratio accepted")
	}
}
