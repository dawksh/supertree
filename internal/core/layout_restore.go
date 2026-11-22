package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/tmux"
)

type restoreArgs struct {
	WorkspaceID      string `json:"workspace_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	ExpectedLayout   string `json:"expected_layout"`
}
type restoreResult struct {
	WorkspaceID string `json:"workspace_id"`
	Revision    int64  `json:"revision"`
	WindowID    string `json:"window_id"`
	Layout      string `json:"observed_layout"`
}

type layoutLiveArgs struct {
	WorkspaceID string `json:"workspace_id"`
	ProcessID   string `json:"process_id"`
}
type layoutLiveResult struct {
	WorkspaceID string `json:"workspace_id"`
	ProcessID   string `json:"process_id"`
	Revision    int64  `json:"revision"`
	WindowID    string `json:"window_id"`
	Layout      string `json:"expected_layout"`
}

func (c *Core) registerLayoutLive(r *command.Registry) error {
	return command.Register(r, command.Definition{Name: "layout.live", Version: 1, Description: "Inspect the live window containing a saved managed process.", Effects: []string{"read"}, Repeatable: true}, func(ctx context.Context, a layoutLiveArgs) (layoutLiveResult, error) {
		c.refresh.Lock()
		defer c.refresh.Unlock()
		fail := func(s string) (layoutLiveResult, error) {
			return layoutLiveResult{}, &command.Error{Code: "conflict", Message: s}
		}
		if a.WorkspaceID == "" || a.ProcessID == "" {
			return fail("workspace and anchor process are required")
		}
		if _, err := c.workspace(a.WorkspaceID); err != nil {
			return layoutLiveResult{}, err
		}
		docs, err := c.store.Documents(ctx, "layout")
		if err != nil {
			return layoutLiveResult{}, err
		}
		var root layoutNode
		var rev int64
		for _, doc := range docs {
			if doc.ID == a.WorkspaceID {
				rev = doc.Revision
				if err = json.Unmarshal(doc.Data, &root); err != nil {
					return layoutLiveResult{}, err
				}
				break
			}
		}
		var contains func(layoutNode) bool
		contains = func(n layoutNode) bool {
			if n.Type == "pane" && n.ResourceID == a.ProcessID {
				return true
			}
			for _, ch := range n.Children {
				if contains(ch) {
					return true
				}
			}
			return false
		}
		if rev == 0 || !contains(root) {
			return fail("selected process is not in the saved layout")
		}
		snap, err := c.Snapshot(a.WorkspaceID)
		if err != nil {
			return layoutLiveResult{}, err
		}
		paneID := ""
		for _, p := range snap.Processes {
			if p.ID == a.ProcessID && p.WorkspaceID == a.WorkspaceID && p.Source != "observed" && p.State == "running" {
				paneID = p.PaneID
			}
		}
		if paneID == "" {
			return fail("anchor must be a running managed process")
		}
		client := tmux.Client{Socket: c.options.TmuxSocket}
		panes, err := client.Panes(ctx)
		if err != nil {
			return layoutLiveResult{}, err
		}
		window := ""
		count := 0
		for _, p := range panes {
			if p.ProcessID == a.ProcessID {
				count++
				if p.ID == paneID && !p.Dead {
					window = p.WindowID
				}
			}
		}
		if count != 1 || window == "" {
			return fail("anchor pane identity changed")
		}
		live, err := client.Run(ctx, "display-message", "-p", "-t", paneID, "#{window_id}\t#{window_zoomed_flag}\t#{window_layout}")
		if err != nil {
			return layoutLiveResult{}, err
		}
		parts := strings.Split(live, "\t")
		if len(parts) != 3 || parts[0] != window || parts[1] != "0" || parts[2] == "" {
			return fail("anchor window is zoomed or changed")
		}
		return layoutLiveResult{a.WorkspaceID, a.ProcessID, rev, window, parts[2]}, nil
	})
}

func (c *Core) registerLayoutRestore(r *command.Registry) error {
	return command.Register(r, command.Definition{Name: "layout.restore", Version: 1, Description: "Explicitly restore a saved layout in its current tmux window.", Effects: []string{"layout.restore"}}, func(ctx context.Context, args restoreArgs) (restoreResult, error) {
		c.refresh.Lock()
		defer c.refresh.Unlock()
		fail := func(msg string) (restoreResult, error) {
			return restoreResult{}, &command.Error{Code: "conflict", Message: msg}
		}
		if args.WorkspaceID == "" || args.ExpectedRevision == nil || args.ExpectedLayout == "" {
			return restoreResult{}, &command.Error{Code: "invalid_arguments", Message: "workspace_id, expected_revision and expected_layout are required"}
		}
		if _, err := c.workspace(args.WorkspaceID); err != nil {
			return restoreResult{}, err
		}
		docs, err := c.store.Documents(ctx, "layout")
		if err != nil {
			return restoreResult{}, err
		}
		var node layoutNode
		var revision int64
		for _, doc := range docs {
			if doc.ID == args.WorkspaceID {
				revision = doc.Revision
				if err := json.Unmarshal(doc.Data, &node); err != nil {
					return restoreResult{}, err
				}
				break
			}
		}
		if revision != *args.ExpectedRevision {
			return fail("saved layout revision changed")
		}
		if revision == 0 {
			return fail("saved layout is missing")
		}
		snapshot, err := c.Snapshot(args.WorkspaceID)
		if err != nil {
			return restoreResult{}, err
		}
		allowed := map[string]bool{}
		for _, p := range snapshot.Processes {
			if p.WorkspaceID == args.WorkspaceID && p.Source != "observed" && p.State == "running" {
				allowed[p.ID] = true
			}
		}
		var ids []string
		seen := map[string]bool{}
		var collect func(layoutNode) error
		collect = func(n layoutNode) error {
			switch n.Type {
			case "pane":
				if !allowed[n.ResourceID] || seen[n.ResourceID] {
					return fmt.Errorf("resource must be a unique managed process in this workspace")
				}
				seen[n.ResourceID] = true
				ids = append(ids, n.ResourceID)
			case "split":
				if len(n.Children) != 2 || (n.Direction != "horizontal" && n.Direction != "vertical") || n.Ratio < 0.05 || n.Ratio > 0.95 {
					return fmt.Errorf("invalid split")
				}
				for _, child := range n.Children {
					if err := collect(child); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("unsupported or empty layout")
			}
			return nil
		}
		if err := collect(node); err != nil {
			return fail(err.Error())
		}
		client := tmux.Client{Socket: c.options.TmuxSocket}
		panes, err := client.Panes(ctx)
		if err != nil {
			return restoreResult{}, err
		}
		found := make([]tmux.Pane, len(ids))
		for i, id := range ids {
			count := 0
			for _, p := range panes {
				if p.ProcessID == id {
					found[i] = p
					count++
				}
			}
			if count != 1 || found[i].Dead || (i > 0 && found[i].WindowID != found[0].WindowID) {
				return fail("resources must be live, uniquely tagged panes in the same window")
			}
		}
		window := found[0].WindowID
		n := 0
		for _, p := range panes {
			if p.WindowID == window {
				n++
			}
		}
		if n != len(ids) {
			return fail("window contains other panes; restore refused")
		}
		size, err := client.Run(ctx, "display-message", "-p", "-t", window, "#{window_width} #{window_height} #{window_zoomed_flag}")
		if err != nil {
			return restoreResult{}, err
		}
		var w, h, zoom int
		if _, err := fmt.Sscanf(size, "%d %d %d", &w, &h, &zoom); err != nil || w < 2 || h < 2 {
			return fail("cannot determine window size")
		}
		if zoom != 0 {
			return fail("unsupported: zoomed window")
		}
		current, err := client.Run(ctx, "display-message", "-p", "-t", window, "#{window_layout}")
		if err != nil {
			return restoreResult{}, err
		}
		if current != args.ExpectedLayout {
			return fail("live tmux layout changed")
		}
		index := 0
		var build func(layoutNode, int, int, int, int) (string, error)
		build = func(n layoutNode, w, h, x, y int) (string, error) {
			if w < 1 || h < 1 {
				return "", fmt.Errorf("saved layout does not fit window")
			}
			if n.Type == "pane" {
				id := found[index].ID
				index++
				if !strings.HasPrefix(id, "%") {
					return "", fmt.Errorf("invalid pane ID")
				}
				v, e := strconv.Atoi(id[1:])
				if e != nil || v < 0 {
					return "", fmt.Errorf("invalid pane ID")
				}
				return fmt.Sprintf("%dx%d,%d,%d,%d", w, h, x, y, v), nil
			}
			horizontal := n.Direction == "horizontal"
			total := w
			if !horizontal {
				total = h
			}
			first := int(math.Round(float64(total-1) * n.Ratio))
			second := total - 1 - first
			if first < 1 || second < 1 {
				return "", fmt.Errorf("saved layout does not fit window")
			}
			aw, ah, bw, bh, bx, by := w, h, w, h, x, y
			if horizontal {
				aw = first
				bw = second
				bx = x + first + 1
			} else {
				ah = first
				bh = second
				by = y + first + 1
			}
			a, e := build(n.Children[0], aw, ah, x, y)
			if e != nil {
				return "", e
			}
			b, e := build(n.Children[1], bw, bh, bx, by)
			if e != nil {
				return "", e
			}
			open, close := "{", "}"
			if !horizontal {
				open, close = "[", "]"
			}
			return fmt.Sprintf("%dx%d,%d,%d%s%s,%s%s", w, h, x, y, open, a, b, close), nil
		}
		body, err := build(node, w, h, 0, 0)
		if err != nil {
			return fail(err.Error())
		}
		var sum uint16
		for _, b := range []byte(body) {
			sum = (sum >> 1) | (sum << 15)
			sum += uint16(b)
		}
		layout := fmt.Sprintf("%04x,%s", sum, body)
		// Tmux assigns geometry by pane order, not layout-string IDs.
		order, err := client.Run(ctx, "list-panes", "-t", window, "-F", "#{pane_id}")
		if err != nil {
			return restoreResult{}, err
		}
		var expectedOrder []string
		for _, p := range found {
			expectedOrder = append(expectedOrder, p.ID)
		}
		if order != strings.Join(expectedOrder, "\n") {
			return fail("unsupported pane order; restore does not reorder panes")
		}
		// Compare and apply inside one tmux command queue, so an external layout
		// change between preflight and application is not overwritten.
		escaped := strings.NewReplacer(",", "#,", "}", "#}").Replace(current)
		condition := "#{&&:#{==:#{window_layout}," + escaped + "},#{==:#{window_zoomed_flag},0}}"
		apply := "select-layout -t " + window + " '" + layout + "' ; display-message -p ST_LAYOUT_APPLIED"
		status, err := client.Run(ctx, "if-shell", "-F", "-t", window, condition, apply, "display-message -p ST_LAYOUT_CHANGED")
		if err != nil {
			return restoreResult{}, &command.Error{Code: "partial_failure", Message: "layout restore may have changed tmux; inspect before retrying: " + err.Error()}
		}
		if status == "ST_LAYOUT_CHANGED" {
			return fail("live tmux layout changed during restore")
		}
		if status != "ST_LAYOUT_APPLIED" {
			return restoreResult{}, &command.Error{Code: "partial_failure", Message: "layout restore status unknown; inspect tmux before retrying"}
		}
		observed, err := client.Run(ctx, "display-message", "-p", "-t", window, "#{window_layout}")
		if err != nil || observed != layout {
			return restoreResult{}, &command.Error{Code: "partial_failure", Message: "layout restore could not be verified; inspect tmux before retrying"}
		}
		return restoreResult{args.WorkspaceID, revision, window, observed}, nil
	})
}
