package core

import (
	"context"
	"fmt"
	"sort"
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
		root, err := captureTree(geometry, 0)
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

// Split only along complete, one-cell tmux dividers. This rejects overlapping,
// non-slicing, and otherwise unrepresentable geometry instead of losing panes.
func captureTree(p []capturePane, depth int) (map[string]any, error) {
	if len(p) == 0 || len(p) > 128 || depth > 32 {
		return nil, fmt.Errorf("unsupported layout size")
	}
	if len(p) == 1 {
		return map[string]any{"type": "pane", "resource_id": p[0].resource}, nil
	}
	minX, minY, maxX, maxY := p[0].x, p[0].y, p[0].x+p[0].w, p[0].y+p[0].h
	for _, q := range p {
		if q.x < minX {
			minX = q.x
		}
		if q.y < minY {
			minY = q.y
		}
		if q.x+q.w > maxX {
			maxX = q.x + q.w
		}
		if q.y+q.h > maxY {
			maxY = q.y + q.h
		}
	}
	for _, horizontal := range []bool{true, false} {
		cuts := []int{}
		for _, q := range p {
			if horizontal {
				cuts = append(cuts, q.x+q.w)
			} else {
				cuts = append(cuts, q.y+q.h)
			}
		}
		sort.Ints(cuts)
		for _, cut := range cuts {
			var left, right []capturePane
			valid := true
			for _, q := range p {
				start, end := q.x, q.x+q.w
				if !horizontal {
					start, end = q.y, q.y+q.h
				}
				if end <= cut {
					left = append(left, q)
				} else if start >= cut+1 {
					right = append(right, q)
				} else {
					valid = false
					break
				}
			}
			if !valid || len(left) == 0 || len(right) == 0 {
				continue
			}
			l, e := captureTree(left, depth+1)
			if e != nil {
				continue
			}
			rr, e := captureTree(right, depth+1)
			if e != nil {
				continue
			}
			direction := "horizontal"
			ratio := float64(cut-minX) / float64(maxX-minX-1)
			if !horizontal {
				direction = "vertical"
				ratio = float64(cut-minY) / float64(maxY-minY-1)
			}
			if ratio < 0.05 || ratio > 0.95 {
				continue
			}
			return map[string]any{"type": "split", "direction": direction, "ratio": ratio, "children": []any{l, rr}}, nil
		}
	}
	return nil, fmt.Errorf("unsupported non-slicing or extreme pane layout")
}
