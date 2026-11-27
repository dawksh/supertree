package buffer

import "fmt"

type Rect struct {
	X, Y, Width, Height int
}

type Size struct{ Width, Height int }

type Placement struct {
	PaneID      string
	TerminalID  string
	Placeholder bool
	Rect        Rect
}

type Geometry struct {
	Panes  []Placement
	Narrow bool
}

// Arrange computes content rectangles with a one-cell divider. Focus and zoom
// are client inputs, never stored in the document. At insufficient sizes it
// temporarily shows the focused leaf without changing the saved split tree.
func Arrange(tab Tab, area Rect, focus string, zoom bool, minimum Size) (Geometry, error) {
	result := Geometry{Panes: []Placement{}}
	if area.X < 0 || area.Y < 0 || area.Width < 0 || area.Height < 0 ||
		area.X > 1<<20 || area.Y > 1<<20 || area.Width > 1<<20 || area.Height > 1<<20 ||
		minimum.Width < 1 || minimum.Height < 1 || minimum.Width > 1<<20 || minimum.Height > 1<<20 {
		return result, fmt.Errorf("invalid content rectangle or minimum pane size")
	}
	if err := Validate(Document{Version: Version, WorkspaceID: "geometry", Tabs: []Tab{tab}}); err != nil {
		return result, err
	}
	if tab.Root == "" {
		if focus != "" {
			return result, fmt.Errorf("focused pane is missing")
		}
		return result, nil
	}
	nodes := map[string]Node{}
	for _, node := range tab.Nodes {
		nodes[node.ID] = node
	}
	sizes := map[string]Size{}
	first := ""
	var measure func(string) Size
	measure = func(id string) Size {
		node := nodes[id]
		if node.Kind != "split" {
			if first == "" {
				first = id
			}
			sizes[id] = minimum
			return minimum
		}
		a, b := measure(node.Children[0]), measure(node.Children[1])
		size := Size{max(a.Width, b.Width), a.Height + b.Height + 1}
		if node.Axis == "columns" {
			size = Size{a.Width + b.Width + 1, max(a.Height, b.Height)}
		}
		sizes[id] = size
		return size
	}
	required := measure(tab.Root)
	if focus == "" {
		focus = first
	}
	focused, exists := nodes[focus]
	if !exists || focused.Kind == "split" {
		return result, fmt.Errorf("focused pane is missing")
	}
	result.Narrow = area.Width < required.Width || area.Height < required.Height
	if area.Width == 0 || area.Height == 0 {
		return result, nil
	}
	add := func(node Node, rect Rect) {
		result.Panes = append(result.Panes, Placement{node.ID, node.TerminalID, node.Kind == "placeholder", rect})
	}
	if zoom || result.Narrow {
		add(focused, area)
		return result, nil
	}
	var place func(string, Rect)
	place = func(id string, rect Rect) {
		node := nodes[id]
		if node.Kind != "split" {
			add(node, rect)
			return
		}
		a, b := node.Children[0], node.Children[1]
		left, right := rect, rect
		if node.Axis == "columns" {
			available := rect.Width - 1
			width := max(sizes[a].Width, min(available-sizes[b].Width, available*node.Ratio/100))
			left.Width, right.X, right.Width = width, rect.X+width+1, available-width
		} else {
			available := rect.Height - 1
			height := max(sizes[a].Height, min(available-sizes[b].Height, available*node.Ratio/100))
			left.Height, right.Y, right.Height = height, rect.Y+height+1, available-height
		}
		place(a, left)
		place(b, right)
	}
	place(tab.Root, area)
	return result, nil
}
