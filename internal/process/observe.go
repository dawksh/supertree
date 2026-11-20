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

func (m *Manager) observe(ctx context.Context, items []saved, panes []tmux.Pane, workspaces []workspace.Workspace) ([]saved, error) {
	for _, pane := range panes {
		if pane.ProcessID != "" || pane.Sidebar || pane.Label == "" || pane.Label == "Supertree" {
			continue
		}
		var owner workspace.Workspace
		path := workspace.CanonicalPath(pane.Directory)
		if pane.TreeDirectory != "" {
			path = workspace.CanonicalPath(pane.TreeDirectory)
		}
		for _, w := range workspaces {
			if w.State == "available" && within(w.Path, path) && len(w.Path) > len(owner.Path) {
				owner = w
			}
		}
		if owner.ID == "" {
			continue
		}
		identity := paneIdentity(m.client.Socket, pane)
		if slices.ContainsFunc(items, func(item saved) bool { return item.PaneIdentity == identity }) {
			continue
		}
		kind := pane.Role
		if kind == "vim" {
			kind = "editor"
		}
		if kind == "" {
			switch pane.WindowName {
			case "nvim", "vim":
				kind = "editor"
			case "shell":
				kind = "shell"
			default:
				kind = "terminal"
				if pane.WindowName == pane.AgentWindow || slices.Contains([]string{"codex", "claude", "opencode", "agent"}, pane.WindowName) {
					kind = "agent"
				}
			}
		}
		name := pane.WindowName
		if slices.ContainsFunc(items, func(item saved) bool { return item.Process.WorkspaceID == owner.ID && item.Process.Name == name }) {
			name += " " + pane.ID
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
		if pane.ProcessID == "" && paneIdentity(m.client.Socket, pane) == item.PaneIdentity {
			return pane, nil
		}
	}
	return tmux.Pane{}, conflict("existing terminal no longer exists or was replaced")
}
