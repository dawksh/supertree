package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/workspace"
)

type BindArgs struct {
	WorkspaceID string `json:"workspace_id"`
	PaneID      string `json:"pane_id"`
	TaskID      string `json:"task_id,omitempty"`
	ThreadID    string `json:"thread_id,omitempty"`
	Model       string `json:"model,omitempty"`
}
type Binding struct {
	Agent     workspace.Agent `json:"agent"`
	Socket    string          `json:"socket"`
	StateFile string          `json:"state_file"`
	Directory string          `json:"directory"`
}
type PrimaryArgs struct {
	WorkspaceID string `json:"workspace_id"`
}

func (m *Manager) Primary(ctx context.Context, args PrimaryArgs) (workspace.Agent, error) {
	items, err := load[record](ctx, m.store, "agent")
	if err != nil {
		return workspace.Agent{}, err
	}
	for _, r := range items {
		if r.Agent.WorkspaceID == args.WorkspaceID && r.Agent.Primary {
			return r.Agent, nil
		}
	}
	return workspace.Agent{}, &command.Error{Code: "not_found", Message: "no paired agent in this worktree; exit the terminal CLI in its agent tab, then run st pair run (use --thread for an existing Codex conversation)"}
}

// Bind supplies an exact, persistent worker identity to the foreground agent
// tab. The core never replaces a running CLI or guesses its conversation ID.
func (m *Manager) Bind(ctx context.Context, args BindArgs) (Binding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !humanActor(command.Caller(ctx).Actor) {
		return Binding{}, &command.Error{Code: "human_required", Message: "binding the primary agent requires a human action"}
	}
	if len(args.ThreadID) > 200 || len(args.Model) > 200 || !strings.HasPrefix(args.PaneID, "%") {
		return Binding{}, &command.Error{Code: "invalid_arguments", Message: "pairing requires an agent pane and bounded thread/model values"}
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return Binding{}, &command.Error{Code: "dependency_missing", Message: "install Codex and sign in before pairing"}
	}
	w, err := m.resolve(args.WorkspaceID)
	if err != nil {
		return Binding{}, err
	}
	if args.TaskID != "" {
		if _, err := m.collab.Task(ctx, args.TaskID, args.WorkspaceID); err != nil {
			return Binding{}, err
		}
	}
	items, err := load[record](ctx, m.store, "agent")
	if err != nil {
		return Binding{}, err
	}
	r := record{Agent: workspace.Agent{ID: process.ID("agent"), WorkspaceID: w.ID, Name: "codex", Provider: "codex", Primary: true, State: "connecting", ThreadID: args.ThreadID, Model: args.Model, TaskID: args.TaskID, Capabilities: []string{}, Messages: []workspace.AgentMessage{}, Questions: []workspace.Question{}}}
	for _, old := range items {
		if old.Agent.WorkspaceID != w.ID || !old.Agent.Primary {
			continue
		}
		r = old
		probe, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		_, err := workerCall(probe, r.Socket, workerRequest{Method: "inspect"})
		cancel()
		if err == nil {
			if _, err := m.processes.Attach(ctx, process.Target{ProcessID: r.Agent.ProcessID}); err != nil {
				return Binding{}, &command.Error{Code: "busy", Message: "the previous agent tab is closing; wait before reconnecting"}
			}
			return Binding{}, &command.Error{Code: "conflict", Message: "the paired agent is already running in its tab; use st pair send or st pair interrupt"}
		}
		started, _ := time.Parse(time.RFC3339Nano, r.StartedAt)
		if r.Agent.State == "connecting" && time.Since(started) < 45*time.Second {
			if _, err := m.processes.Attach(ctx, process.Target{ProcessID: r.Agent.ProcessID}); err == nil {
				return Binding{}, &command.Error{Code: "busy", Message: "agent tab is still connecting; wait before restarting"}
			}
		}
		// The worker may have checkpointed its identity just before the core
		// disconnected, without the next observation reaching the database.
		if data, err := os.ReadFile(filepath.Join(m.stateDir, "next", r.Agent.ID+".json")); err == nil {
			var checkpoint workerFile
			if json.Unmarshal(data, &checkpoint) == nil && checkpoint.Agent.ID == r.Agent.ID && checkpoint.Agent.ThreadID != "" {
				r.Agent.ThreadID = checkpoint.Agent.ThreadID
			}
		}
		if args.ThreadID != "" && r.Agent.ThreadID != "" && args.ThreadID != r.Agent.ThreadID {
			return Binding{}, &command.Error{Code: "conflict", Message: "this worktree already has another exact conversation; refusing to replace it"}
		}
		if args.TaskID != "" {
			r.Agent.TaskID = args.TaskID
		}
		if args.Model != "" {
			r.Agent.Model = args.Model
		}
		break
	}
	dir := fmt.Sprintf("/tmp/supertree-agents-%d", os.Getuid())
	if err := privateDirectory(dir); err != nil {
		return Binding{}, err
	}
	sum := sha256.Sum256([]byte(m.stateDir))
	r.Socket = filepath.Join(dir, fmt.Sprintf("%x-%s.sock", sum[:8], strings.TrimPrefix(r.Agent.ID, "agent_")))
	p, err := m.processes.Host(ctx, w.ID, args.PaneID, r.Agent.ID)
	if err != nil {
		return Binding{}, err
	}
	r.Agent.ProcessID, r.Agent.State, r.Agent.Error = p.ID, "connecting", ""
	r.Agent.Capabilities = []string{}
	r.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := m.save(ctx, &r, r.Agent.Revision); err != nil {
		return Binding{}, err
	}
	return Binding{r.Agent, r.Socket, filepath.Join(m.stateDir, "next", r.Agent.ID+".json"), w.Path}, nil
}
