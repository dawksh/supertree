package process

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

func paneIdentity(socket string, p tmux.Pane) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{socket, p.ServerPID, p.ID, p.PID}, "\x00"))))
}

func presentation(p workspace.Process) workspace.Process {
	if p.Source == "" {
		p.Source = "registered"
		if p.Owned {
			p.Source = "managed"
		}
	}
	p.Actions = []string{"process.attach", "process.output"}
	if p.Source != "observed" {
		p.Actions = append(p.Actions, "process.stop", "process.move")
	}
	if p.Owned {
		p.Actions = append(p.Actions, "process.restart")
	}
	if p.Source == "observed" {
		p.Actions = append(p.Actions, "process.register")
	}
	return p
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Borrowing changes placement, not the terminal's source tab or workspace.
// Prefer the paired holder; another pane in the home session also works if the
// user closed that holder. Never use the destination session as the source.
func observationSource(pane tmux.Pane, panes []tmux.Pane) tmux.Pane {
	if pane.HomePane == "" {
		return pane
	}
	result := pane
	result.TreeDirectory, result.AgentWindow = "", ""
	if pane.HomeWindow != "" {
		result.WindowName = pane.HomeWindow
	}
	for _, home := range panes {
		if home.ID == pane.HomePane && home.LentPane == pane.ID {
			result.TreeDirectory, result.AgentWindow = home.TreeDirectory, home.AgentWindow
			return result
		}
	}
	for _, home := range panes {
		if pane.HomeSession != "" && home.SessionName == pane.HomeSession {
			result.TreeDirectory, result.AgentWindow = home.TreeDirectory, home.AgentWindow
			break
		}
	}
	return result
}

func (m *Manager) observe(ctx context.Context, items []saved, panes []tmux.Pane, workspaces []workspace.Workspace) ([]saved, error) {
	for _, pane := range panes {
		if pane.ProcessID != "" || pane.Sidebar || pane.LentPane != "" || pane.Label == "" || pane.Label == "Supertree" {
			continue
		}
		identity := paneIdentity(m.client.Socket, pane)
		index := slices.IndexFunc(items, func(item saved) bool { return item.PaneIdentity == identity })
		// Retain ordinary observations as before. Only an explicit loan gives
		// source metadata with which to repair a previously wrong placement.
		if index >= 0 && (items[index].Process.Source != "observed" || pane.HomePane == "") {
			continue
		}
		source := observationSource(pane, panes)
		var owner workspace.Workspace
		path := workspace.CanonicalPath(pane.Directory)
		if source.TreeDirectory != "" {
			path = workspace.CanonicalPath(source.TreeDirectory)
		} else if pane.HomePane != "" && index >= 0 {
			// With no surviving home metadata, retain known ownership even if
			// the shell has since changed directory outside its source tree.
			for _, w := range workspaces {
				if w.ID == items[index].Process.WorkspaceID {
					path = w.Path
					break
				}
			}
		}
		for _, w := range workspaces {
			if w.State == "available" && within(w.Path, path) && len(w.Path) > len(owner.Path) {
				owner = w
			}
		}
		if owner.ID == "" {
			continue
		}
		kind := source.Role
		if kind == "vim" {
			kind = "editor"
		}
		if kind == "" {
			switch source.WindowName {
			case "nvim", "vim":
				kind = "editor"
			case "shell":
				kind = "shell"
			default:
				kind = "terminal"
				if source.WindowName == source.AgentWindow || slices.Contains([]string{"codex", "claude", "opencode", "agent"}, source.WindowName) {
					kind = "agent"
				}
			}
		}
		name := source.WindowName
		if slices.ContainsFunc(items, func(item saved) bool {
			return item.PaneIdentity != identity && item.Process.WorkspaceID == owner.ID && item.Process.Name == name
		}) {
			name += " " + pane.ID
		}
		if index >= 0 {
			item := &items[index]
			if item.Process.WorkspaceID != owner.ID || item.Process.Name != name || item.Process.Kind != kind {
				item.Process.WorkspaceID, item.Process.Name, item.Process.Kind = owner.ID, name, kind
				if err := m.save(ctx, item); err != nil {
					return nil, err
				}
			}
			continue
		}
		item := saved{Process: workspace.Process{ID: "observed_" + identity[:32], WorkspaceID: owner.ID, Name: name, Kind: kind, Source: "observed", Directory: pane.Directory, Command: []string{}, EnvironmentKeys: []string{}, CreatedAt: now()}, PaneIdentity: identity}
		apply(&item.Process, pane)
		if err := m.save(ctx, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (m *Manager) live(ctx context.Context, item saved) (tmux.Pane, error) {
	if item.Process.Source != "observed" {
		return m.client.Tagged(ctx, item.Process.PaneID, item.Process.ID)
	}
	panes, err := m.client.Panes(ctx)
	if err != nil {
		return tmux.Pane{}, err
	}
	for _, pane := range panes {
		if pane.ProcessID == "" && pane.LentPane == "" && paneIdentity(m.client.Socket, pane) == item.PaneIdentity {
			return pane, nil
		}
	}
	return tmux.Pane{}, conflict("existing terminal no longer exists or was replaced")
}
