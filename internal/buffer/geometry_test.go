package buffer_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dawksh/supertree/internal/buffer"
)

func TestGeometryResizesWithoutChangingSavedArrangement(t *testing.T) {
	tab := buffer.Tab{ID: "coding", Name: "Coding", Root: "split", Nodes: []buffer.Node{
		{ID: "split", Kind: "split", Axis: "columns", Ratio: 60, Children: []string{"agent", "stack"}},
		{ID: "agent", Kind: "pane", TerminalID: "codex"},
		{ID: "stack", Kind: "split", Axis: "rows", Ratio: 50, Children: []string{"editor", "shell"}},
		{ID: "editor", Kind: "pane", TerminalID: "vim"},
		{ID: "shell", Kind: "pane", TerminalID: "zsh"},
	}}
	before, _ := json.Marshal(tab)
	minimum := buffer.Size{Width: 8, Height: 3}
	full, err := buffer.Arrange(tab, buffer.Rect{X: 2, Y: 1, Width: 81, Height: 21}, "editor", false, minimum)
	if err != nil {
		t.Fatal(err)
	}
	want := []buffer.Placement{
		{PaneID: "agent", TerminalID: "codex", Rect: buffer.Rect{X: 2, Y: 1, Width: 48, Height: 21}},
		{PaneID: "editor", TerminalID: "vim", Rect: buffer.Rect{X: 51, Y: 1, Width: 32, Height: 10}},
		{PaneID: "shell", TerminalID: "zsh", Rect: buffer.Rect{X: 51, Y: 12, Width: 32, Height: 10}},
	}
	if full.Narrow || !reflect.DeepEqual(full.Panes, want) {
		t.Fatalf("geometry: %#v", full)
	}
	for width := 0; width < 100; width++ {
		for height := 0; height < 30; height++ {
			layout, err := buffer.Arrange(tab, buffer.Rect{Width: width, Height: height}, "editor", false, minimum)
			if err != nil {
				t.Fatal(err)
			}
			for i, p := range layout.Panes {
				if p.Rect.Width < 1 || p.Rect.Height < 1 || p.Rect.X < 0 || p.Rect.Y < 0 || p.Rect.X+p.Rect.Width > width || p.Rect.Y+p.Rect.Height > height {
					t.Fatalf("out of bounds at %dx%d: %#v", width, height, p)
				}
				for _, q := range layout.Panes[:i] {
					if p.Rect.X < q.Rect.X+q.Rect.Width && q.Rect.X < p.Rect.X+p.Rect.Width && p.Rect.Y < q.Rect.Y+q.Rect.Height && q.Rect.Y < p.Rect.Y+p.Rect.Height {
						t.Fatal("overlapping panes")
					}
				}
			}
			if layout.Narrow && width > 0 && height > 0 && (len(layout.Panes) != 1 || layout.Panes[0].PaneID != "editor") {
				t.Fatal("narrow layout lost focused editor")
			}
		}
	}
	zoom, err := buffer.Arrange(tab, buffer.Rect{Width: 81, Height: 21}, "shell", true, minimum)
	if err != nil || len(zoom.Panes) != 1 || zoom.Panes[0].TerminalID != "zsh" {
		t.Fatalf("zoom: %#v %v", zoom, err)
	}
	if _, err := buffer.Arrange(tab, buffer.Rect{Width: 81, Height: 21}, "missing", true, minimum); err == nil {
		t.Fatal("invalid focus accepted")
	}
	after, _ := json.Marshal(tab)
	if string(before) != string(after) {
		t.Fatal("geometry mutated saved arrangement")
	}
	if restored, err := buffer.Arrange(tab, buffer.Rect{X: 2, Y: 1, Width: 81, Height: 21}, "editor", false, minimum); err != nil || !reflect.DeepEqual(restored, full) {
		t.Fatal("resize did not restore arrangement")
	}
}
