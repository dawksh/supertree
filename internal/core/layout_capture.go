package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/tmux"
)

type LayoutCapture struct {
	WorkspaceID      string `json:"workspace_id"`
	ProcessID        string `json:"process_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type capturePane struct {
	id, resource string
	x, y, w, h   int
}

func (c *Core) registerCapture(r *command.Registry) error {
	return command.Register(r, command.Definition{Name: "layout.capture", Version: 1, Description: "Save the live window containing a managed or registered process without changing tmux.", Effects: []string{"layout.save"}}, func(ctx context.Context, a LayoutCapture) (Layout, error) {
		c.refresh.Lock()
		defer c.refresh.Unlock()
		fail := func(s string) (Layout, error) { return Layout{}, &command.Error{Code: "conflict", Message: s} }
		if a.WorkspaceID == "" || a.ProcessID == "" {
			return Layout{}, &command.Error{Code: "invalid_arguments", Message: "workspace_id and process_id are required"}
		}
		snap, err := c.Snapshot(a.WorkspaceID)
		if err != nil {
			return Layout{}, err
		}
		client := tmux.Client{Socket: c.options.TmuxSocket}
		panes, err := client.Panes(ctx)
		if err != nil {
			return Layout{}, err
		}
		byID := map[string]string{}
		anchor := ""
		for _, p := range snap.Processes {
			if p.WorkspaceID == a.WorkspaceID && p.Source != "observed" && p.ID != "" {
				byID[p.ID] = p.PaneID
				if p.ID == a.ProcessID {
					anchor = p.PaneID
				}
			}
		}
		if anchor == "" {
			return fail("capture requires a managed or registered process in this workspace")
		}
		window := ""
		for _, p := range panes {
			if p.ID == anchor && p.ProcessID == a.ProcessID {
				if window != "" {
					return fail("ambiguous anchor pane")
				}
				window = p.WindowID
			}
		}
		if window == "" {
			return fail("anchor pane is missing or identity changed")
		}
		read := func() (string, error) {
			return client.Run(ctx, "display-message", "-p", "-t", anchor, "#{window_id}\t#{window_zoomed_flag}\t#{window_layout}")
		}
		before, err := read()
		if err != nil {
			return Layout{}, err
		}
		parts := strings.Split(before, "\t")
		if len(parts) != 3 || parts[0] != window || parts[1] != "0" {
			return fail("window is zoomed or changed")
		}
		raw, err := client.Run(ctx, "list-panes", "-t", window, "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}\t#{@st_next_process}")
		if err != nil {
			return Layout{}, err
		}
		var geometry []capturePane
		seen := map[string]bool{}
		live := map[string]int{}
		globalTags := map[string]int{}
		for _, p := range panes {
			if p.ProcessID != "" {
				globalTags[p.ProcessID]++
			}
			if p.WindowID == window {
				live[p.ID]++
			}
		}
		for _, line := range strings.Split(raw, "\n") {
			f := strings.Split(line, "\t")
			if len(f) != 6 {
				return fail("unsupported pane record")
			}
			n := make([]int, 4)
			for i := range n {
				n[i], err = strconv.Atoi(f[i+1])
				if err != nil || n[i] < 0 {
					return fail("unsupported pane geometry")
				}
			}
			if n[2] < 1 || n[3] < 1 || f[5] == "" || byID[f[5]] != f[0] || seen[f[5]] || live[f[0]] != 1 || globalTags[f[5]] != 1 {
				return fail("window contains a foreign or ambiguous pane; inspect tmux before retrying")
			}
			seen[f[5]] = true
			geometry = append(geometry, capturePane{f[0], f[5], n[0], n[1], n[2], n[3]})
		}
		if len(geometry) != len(live) {
			return fail("window pane list changed")
		}
		paneResources := map[string]string{}
		for _, q := range geometry {
			paneResources[q.id] = q.resource
		}
		root, err := captureTree(parts[2], paneResources)
		if err != nil {
			return fail(err.Error())
		}
		resources := map[string]bool{}
		for id := range seen {
			resources[id] = true
		}
		if err = validateLayout(root, resources); err != nil {
			return fail("unsupported layout: " + err.Error())
		}
		after, err := read()
		if err != nil {
			return Layout{}, err
		}
		if before != after {
			return fail("window layout changed during capture")
		}
		// Re-read identity as well as geometry; pane ids can be reused or retagged.
		again, err := client.Run(ctx, "list-panes", "-t", window, "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}\t#{@st_next_process}")
		if err != nil {
			return Layout{}, err
		}
		if raw != again {
			return fail("window panes changed during capture")
		}
		// Re-check globally, not just the chosen window: tags are not unique
		// by construction and another client may duplicate them during capture.
		finalPanes, err := client.Panes(ctx)
		if err != nil {
			return Layout{}, err
		}
		counts := map[string]int{}
		for _, p := range finalPanes {
			if seen[p.ProcessID] {
				counts[p.ProcessID]++
				if byID[p.ProcessID] != p.ID || p.WindowID != window || p.Dead {
					return fail("process pane identity changed during capture")
				}
			}
		}
		for id := range seen {
			if counts[id] != 1 {
				return fail("duplicate or missing process tag during capture")
			}
		}
		rev, err := c.store.PutDocument(ctx, "layout", a.WorkspaceID, root, a.ExpectedRevision)
		if err != nil {
			return Layout{}, err
		}
		return Layout{a.WorkspaceID, rev, root}, nil
	})
}

// Build the tree from tmux's own #{window_layout} so the saved pane order is
// exactly tmux's order, which restore requires. Splits with more than two
// panes become nested two-pane splits; restore renders those the same way.
func captureTree(layout string, resources map[string]string) (map[string]any, error) {
	cell, err := parseWindowLayout(layout)
	if err != nil {
		return nil, err
	}
	leaves := cell.leaves(nil)
	if len(leaves) != len(resources) {
		return nil, fmt.Errorf("window contains a foreign or ambiguous pane; inspect tmux before retrying")
	}
	for _, leaf := range leaves {
		if resources[leaf.paneID] == "" {
			return nil, fmt.Errorf("window contains a foreign or ambiguous pane; inspect tmux before retrying")
		}
	}
	return toCaptureNode(cell, resources, 0)
}

type layoutCell struct {
	w, h, x, y int
	horizontal bool
	paneID     string
	children   []layoutCell
}

func (c layoutCell) leaves(out []layoutCell) []layoutCell {
	if c.paneID != "" {
		return append(out, layoutCell{w: c.w, h: c.h, x: c.x, y: c.y, paneID: c.paneID})
	}
	for _, child := range c.children {
		out = child.leaves(out)
	}
	return out
}

// tmux 3.8 prints layouts as JSON ({"V":2,"L":...}); older versions print
// "csum,WxH,X,Y{...}". See LAYOUTS in tmux(1) select-layout.
func parseWindowLayout(layout string) (layoutCell, error) {
	if len(layout) > 64<<10 {
		return layoutCell{}, fmt.Errorf("unsupported layout size")
	}
	if strings.HasPrefix(layout, "{") {
		return parseJSONLayout(layout)
	}
	if len(layout) < 5 || layout[4] != ',' || strings.Trim(layout[:4], "0123456789abcdef") != "" {
		return layoutCell{}, fmt.Errorf("unsupported pane record")
	}
	parser := layoutParser{text: layout[5:]}
	cell, err := parser.cell(0)
	if err != nil {
		return layoutCell{}, err
	}
	if parser.pos != len(parser.text) {
		return layoutCell{}, fmt.Errorf("unsupported pane record")
	}
	return cell, nil
}

type jsonLayoutCell struct {
	Type   string `json:"t"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	PaneID string `json:"I"`
	// encoding/json matches keys case-insensitively; claim tmux's numeric "i"
	// so it is not decoded into "I".
	Index    json.RawMessage  `json:"i"`
	Children []jsonLayoutCell `json:"c"`
}

func parseJSONLayout(layout string) (layoutCell, error) {
	var doc struct {
		Version int             `json:"V"`
		Root    *jsonLayoutCell `json:"L"`
	}
	if err := json.Unmarshal([]byte(layout), &doc); err != nil || doc.Version != 2 || doc.Root == nil {
		return layoutCell{}, fmt.Errorf("unsupported pane record")
	}
	panes := 0
	var convert func(jsonLayoutCell, int) (layoutCell, error)
	convert = func(j jsonLayoutCell, depth int) (layoutCell, error) {
		c := layoutCell{w: j.W, h: j.H, x: j.X, y: j.Y}
		if depth > 32 || c.w < 1 || c.h < 1 || c.x < 0 || c.y < 0 {
			return c, fmt.Errorf("unsupported pane geometry")
		}
		switch j.Type {
		case "p":
			panes++
			if panes > 128 || len(j.Children) != 0 || !strings.HasPrefix(j.PaneID, "%") {
				return c, fmt.Errorf("unsupported pane record")
			}
			if _, err := strconv.ParseUint(j.PaneID[1:], 10, 32); err != nil {
				return c, fmt.Errorf("unsupported pane record")
			}
			c.paneID = j.PaneID
			return c, nil
		case "h", "v":
			c.horizontal = j.Type == "h"
			if len(j.Children) < 2 {
				return c, fmt.Errorf("unsupported pane record")
			}
			for _, child := range j.Children {
				cell, err := convert(child, depth+1)
				if err != nil {
					return c, err
				}
				c.children = append(c.children, cell)
			}
			return c, nil
		}
		return c, fmt.Errorf("unsupported pane record")
	}
	return convert(*doc.Root, 0)
}

type layoutParser struct {
	text  string
	pos   int
	panes int
}

func (p *layoutParser) number() (int, error) {
	start := p.pos
	for p.pos < len(p.text) && p.text[p.pos] >= '0' && p.text[p.pos] <= '9' {
		p.pos++
	}
	if start == p.pos || p.pos-start > 9 {
		return 0, fmt.Errorf("unsupported pane geometry")
	}
	return strconv.Atoi(p.text[start:p.pos])
}

func (p *layoutParser) expect(ch byte) error {
	if p.pos >= len(p.text) || p.text[p.pos] != ch {
		return fmt.Errorf("unsupported pane record")
	}
	p.pos++
	return nil
}

func (p *layoutParser) cell(depth int) (layoutCell, error) {
	var c layoutCell
	if depth > 32 {
		return c, fmt.Errorf("unsupported layout size")
	}
	var err error
	if c.w, err = p.number(); err != nil {
		return c, err
	}
	if err = p.expect('x'); err != nil {
		return c, err
	}
	if c.h, err = p.number(); err != nil {
		return c, err
	}
	for _, field := range []*int{&c.x, &c.y} {
		if err = p.expect(','); err != nil {
			return c, err
		}
		if *field, err = p.number(); err != nil {
			return c, err
		}
	}
	if c.w < 1 || c.h < 1 || p.pos >= len(p.text) {
		return c, fmt.Errorf("unsupported pane geometry")
	}
	switch p.text[p.pos] {
	case ',':
		p.pos++
		id, err := p.number()
		if err != nil {
			return c, err
		}
		p.panes++
		if p.panes > 128 {
			return c, fmt.Errorf("unsupported layout size")
		}
		c.paneID = "%" + strconv.Itoa(id)
		return c, nil
	case '{', '[':
		c.horizontal = p.text[p.pos] == '{'
		closing := byte('}')
		if !c.horizontal {
			closing = ']'
		}
		p.pos++
		for {
			child, err := p.cell(depth + 1)
			if err != nil {
				return c, err
			}
			c.children = append(c.children, child)
			if p.pos < len(p.text) && p.text[p.pos] == ',' {
				p.pos++
				continue
			}
			if err = p.expect(closing); err != nil {
				return c, err
			}
			break
		}
		if len(c.children) < 2 {
			return c, fmt.Errorf("unsupported pane record")
		}
		return c, nil
	}
	return c, fmt.Errorf("unsupported pane record")
}

func toCaptureNode(c layoutCell, resources map[string]string, depth int) (map[string]any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("unsupported layout size")
	}
	if c.paneID != "" {
		return map[string]any{"type": "pane", "resource_id": resources[c.paneID]}, nil
	}
	first := c.children[0]
	rest := c
	rest.children = c.children[1:]
	total, size := c.w, first.w
	direction := "horizontal"
	if !c.horizontal {
		total, size, direction = c.h, first.h, "vertical"
	}
	if total < 3 {
		return nil, fmt.Errorf("unsupported non-slicing or extreme pane layout")
	}
	if c.horizontal {
		rest.x, rest.w = first.x+first.w+1, c.w-first.w-1
	} else {
		rest.y, rest.h = first.y+first.h+1, c.h-first.h-1
	}
	ratio := float64(size) / float64(total-1)
	if ratio < 0.05 || ratio > 0.95 {
		return nil, fmt.Errorf("unsupported non-slicing or extreme pane layout")
	}
	left, err := toCaptureNode(first, resources, depth+1)
	if err != nil {
		return nil, err
	}
	if len(rest.children) == 1 {
		rest = rest.children[0]
	}
	right, err := toCaptureNode(rest, resources, depth+1)
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": "split", "direction": direction, "ratio": ratio, "children": []any{left, right}}, nil
}
