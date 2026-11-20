package process

import (
	"context"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

// Host records a worker which the invoking agent tab launches itself. It never
// respawns a pane, sends terminal input, or launches an additional window.
func (m *Manager) Host(ctx context.Context, workspaceID, paneID, agentID string) (workspace.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, err := m.available(workspaceID)
	if err != nil {
		return workspace.Process{}, err
	}
	panes, err := m.client.Panes(ctx)
	if err != nil {
		return workspace.Process{}, err
	}
	var pane tmux.Pane
	for _, p := range panes {
		if p.ID == paneID {
			pane = p
			break
		}
	}
	if pane.ID == "" || pane.Sidebar || pane.Dead || !within(w.Path, workspace.CanonicalPath(pane.Directory)) {
		return workspace.Process{}, conflict("run pairing in this checkout's agent tab")
	}
	if pane.Role != "agent" && pane.WindowName != "codex" && pane.WindowName != "agent" && (pane.AgentWindow == "" || pane.WindowName != pane.AgentWindow) {
		return workspace.Process{}, conflict("run st pair run in the agent tab after exiting its terminal CLI")
	}
	id := "host_" + agentID
	if pane.ProcessID != "" && pane.ProcessID != id {
		return workspace.Process{}, conflict("agent tab is already registered to another process")
	}
	items, err := m.load(ctx)
	if err != nil {
		return workspace.Process{}, err
	}
	item := saved{Process: workspace.Process{ID: id, WorkspaceID: workspaceID, Name: "codex", Kind: "agent", Source: "hosted", Directory: w.Path, Command: []string{}, EnvironmentKeys: []string{}, CreatedAt: now()}}
	for _, old := range items {
		if old.Process.ID == id {
			item = old
			if old.Process.PaneID != paneID {
				if live, err := m.live(ctx, old); err == nil && !live.Dead {
					return old.Process, conflict("pairing is still hosted in another terminal")
				}
			}
		}
	}
	apply(&item.Process, pane)
	item.PaneIdentity = paneIdentity(m.client.Socket, pane)
	if _, err := m.client.Run(ctx, "set-option", "-p", "-t", pane.ID, "@st_next_process", id); err != nil {
		return item.Process, err
	}
	if err := m.save(ctx, &item); err != nil {
		if pane.ProcessID == "" {
			_, _ = m.client.Run(context.WithoutCancel(ctx), "set-option", "-pu", "-t", pane.ID, "@st_next_process")
		}
		return item.Process, err
	}
	return presentation(item.Process), nil
}
