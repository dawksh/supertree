package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/terminal"
	"github.com/dawksh/supertree/internal/workspace"
)

const terminalIntentKind = "native-terminal-intent-v1"

type TerminalLaunchArgs struct {
	RequestID  string              `json:"request_id"`
	Definition terminal.Definition `json:"definition"`
}
type TerminalActionArgs struct {
	WorkspaceID string          `json:"workspace_id"`
	RequestID   string          `json:"request_id"`
	Target      terminal.Target `json:"target"`
	Force       bool            `json:"force,omitempty"`
}
type terminalIntent struct {
	Version   int             `json:"version"`
	Epoch     string          `json:"epoch"`
	Operation string          `json:"operation"`
	Launch    terminal.Launch `json:"launch"`
	Complete  bool            `json:"complete"`
	Info      terminal.Info   `json:"info"`
}

func (c *Core) terminalDir() string { return filepath.Join(c.options.StateDir, "native-host") }
func (c *Core) registerTerminals(r *command.Registry) error {
	if err := command.Register(r, command.Definition{Name: "terminal.launch", Version: 1, Description: "Launch one native terminal through a durable intent; requires an explicitly started host.", Effects: []string{"terminal.launch"}, SensitiveArguments: []string{"definition.args", "definition.environment"}}, c.launchTerminal); err != nil {
		return err
	}
	if err := command.Register(r, command.Definition{Name: "terminal.inspect", Version: 1, Description: "Inspect native host inventory without starting programs.", Effects: []string{"read"}, Repeatable: true}, func(ctx context.Context, args InspectArgs) (terminal.Status, error) {
		if args.WorkspaceID != "" {
			if _, err := c.workspace(args.WorkspaceID); err != nil {
				return terminal.Status{}, err
			}
		}
		response, err := terminal.Call(ctx, c.terminalDir(), terminal.Request{Op: "hello"})
		if err != nil {
			return terminal.Status{}, err
		}
		result := *response.Status
		if args.WorkspaceID != "" {
			result.Terminals = nil
			for _, info := range response.Status.Terminals {
				if info.WorkspaceID == args.WorkspaceID {
					result.Terminals = append(result.Terminals, info)
				}
			}
		}
		return result, nil
	}); err != nil {
		return err
	}
	if err := command.Register(r, command.Definition{Name: "terminal.stop", Version: 1, Description: "Stop an explicitly selected native terminal session; force is separate.", Effects: []string{"terminal.stop"}}, c.stopTerminal); err != nil {
		return err
	}
	return command.Register(r, command.Definition{Name: "terminal.restart", Version: 1, Description: "Explicitly restart a known stopped/lost native definition as its next generation.", Effects: []string{"terminal.launch"}}, c.restartTerminal)
}

func (c *Core) terminalIntents(ctx context.Context) (map[string]terminalIntent, map[string]int64, error) {
	docs, err := c.store.Documents(ctx, terminalIntentKind)
	if err != nil {
		return nil, nil, err
	}
	values := map[string]terminalIntent{}
	revisions := map[string]int64{}
	for _, doc := range docs {
		var intent terminalIntent
		if json.Unmarshal(doc.Data, &intent) != nil || intent.Version != 1 || intent.Launch.Definition.Validate() != nil || intent.Epoch == "" || (intent.Operation != "ensure" && intent.Operation != "restart") {
			return nil, nil, fmt.Errorf("invalid persisted native launch intent")
		}
		values[doc.ID] = intent
		revisions[doc.ID] = doc.Revision
	}
	return values, revisions, nil
}

func intentID(workspace, request string) string {
	sum := sha256.Sum256([]byte(workspace + "\x00" + request))
	return fmt.Sprintf("launch_%x", sum[:16])
}
func (c *Core) launchTerminal(ctx context.Context, args TerminalLaunchArgs) (terminal.Info, error) {
	c.refresh.Lock()
	defer c.refresh.Unlock()
	if args.RequestID == "" || len(args.RequestID) > 128 {
		return terminal.Info{}, &command.Error{Code: "invalid_arguments", Message: "explicit bounded launch request_id is required"}
	}
	if err := args.Definition.Validate(); err != nil {
		return terminal.Info{}, err
	}
	id := intentID(args.Definition.WorkspaceID, args.RequestID)
	l := terminal.Launch{RequestID: id, TerminalID: "term_" + strings.TrimPrefix(id, "launch_"), Generation: 1, Definition: args.Definition}
	return c.ensureTerminal(ctx, id, l, "ensure")
}

// Runs with refresh held, immediately before an operation may launch a child.
func (c *Core) validateTerminalDirectory(ctx context.Context, definition terminal.Definition) error {
	// A move/removal can precede the background discovery poll. Refresh before
	// authorizing execution rather than trusting an otherwise valid cached path.
	if err := c.refreshState(ctx, c.options.Directory, command.Caller(ctx)); err != nil {
		return err
	}
	w, err := c.workspace(definition.WorkspaceID)
	if err != nil {
		return err
	}
	if w.State != "available" {
		return &command.Error{Code: "workspace_unavailable", Message: "owning worktree is unavailable"}
	}
	dir := workspace.CanonicalPath(definition.Directory)
	root := workspace.CanonicalPath(w.Path)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return &command.Error{Code: "invalid_arguments", Message: "terminal cwd must be inside its owning worktree"}
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return &command.Error{Code: "invalid_arguments", Message: "terminal cwd is unavailable"}
	}
	return nil
}

// ensureTerminal runs with refresh held. Persist before spawn; reconciling an
// old intent across a new epoch can report lost, but can never execute it.
func (c *Core) ensureTerminal(ctx context.Context, id string, l terminal.Launch, operation string) (terminal.Info, error) {
	intents, revisions, err := c.terminalIntents(ctx)
	if err != nil {
		return terminal.Info{}, err
	}
	intent, exists := intents[id]
	if exists {
		a, _ := json.Marshal(intent.Launch)
		b, _ := json.Marshal(l)
		if string(a) != string(b) || intent.Operation != operation {
			return terminal.Info{}, &command.Error{Code: "conflict", Message: "request ID was already used for another terminal operation"}
		}
	} else if err := c.validateTerminalDirectory(ctx, l.Definition); err != nil {
		// Reject a new unsafe launch/restart before recording its intent or
		// contacting the host. Reconciliation of existing receipts is read-only.
		return terminal.Info{}, err
	}
	response, err := terminal.Call(ctx, c.terminalDir(), terminal.Request{Op: "hello"})
	if err != nil {
		return terminal.Info{}, err
	}
	status := response.Status
	if !exists {
		if len(intents) >= terminal.MaxReceipts {
			return terminal.Info{}, &command.Error{Code: "resource_limit", Message: "native launch intent bound reached"}
		}
		intent = terminalIntent{Version: 1, Epoch: status.Epoch, Operation: operation, Launch: l}
		revisions[id], err = c.store.PutDocument(ctx, terminalIntentKind, id, intent, 0)
		if err != nil {
			return terminal.Info{}, err
		}
	}
	var result *terminal.Info
	mayLaunch := true
	for _, info := range status.Terminals {
		if info.TerminalID != l.TerminalID {
			continue
		}
		mayLaunch = info.Generation < l.Generation
		if info.Generation == l.Generation {
			copy := info
			result = &copy
			break
		}
	}
	if intent.Epoch != status.Epoch {
		if result == nil {
			result = &terminal.Info{TerminalID: l.TerminalID, Generation: l.Generation, WorkspaceID: l.Definition.WorkspaceID, RequestID: l.RequestID, HostEpoch: intent.Epoch, State: "lost", Error: "host epoch changed before outcome was reconciled; explicit restart required"}
		}
	} else if result == nil {
		if exists && mayLaunch {
			// A pending intent can outlive a worktree move/removal. Validate
			// again before dispatch, but allow historical receipt lookups after
			// the host has already reached this or a later generation.
			if err := c.validateTerminalDirectory(ctx, l.Definition); err != nil {
				return terminal.Info{}, err
			}
		}
		response, err = terminal.Call(ctx, c.terminalDir(), terminal.Request{Op: operation, Epoch: intent.Epoch, Launch: &l})
		if err != nil {
			return terminal.Info{}, err
		}
		result = response.Info
	}
	if result == nil {
		return terminal.Info{}, &command.Error{Code: "invalid_state", Message: "native host did not return a terminal outcome"}
	}
	intent.Info, intent.Complete = *result, true
	if _, err = c.store.PutDocument(ctx, terminalIntentKind, id, intent, revisions[id]); err != nil {
		return *result, err
	}
	return *result, nil
}

func (c *Core) stopTerminal(ctx context.Context, args TerminalActionArgs) (terminal.Info, error) {
	c.refresh.Lock()
	defer c.refresh.Unlock()
	if _, err := c.workspace(args.WorkspaceID); err != nil {
		return terminal.Info{}, err
	}
	response, err := terminal.Call(ctx, c.terminalDir(), terminal.Request{Op: "hello"})
	if err != nil {
		return terminal.Info{}, err
	}
	found := false
	for _, info := range response.Status.Terminals {
		if info.TerminalID == args.Target.TerminalID && info.WorkspaceID == args.WorkspaceID {
			found = true
		}
	}
	if !found {
		return terminal.Info{}, &command.Error{Code: "not_found", Message: "terminal is not owned by that worktree"}
	}
	response, err = terminal.Call(ctx, c.terminalDir(), terminal.Request{Op: "stop", Epoch: response.Status.Epoch, Target: &args.Target, Force: args.Force})
	if err != nil {
		return terminal.Info{}, err
	}
	return *response.Info, nil
}

func (c *Core) restartTerminal(ctx context.Context, args TerminalActionArgs) (terminal.Info, error) {
	c.refresh.Lock()
	defer c.refresh.Unlock()
	if args.RequestID == "" || len(args.RequestID) > 128 || args.Target.Generation == math.MaxInt64 {
		return terminal.Info{}, &command.Error{Code: "invalid_arguments", Message: "restart requires a fresh bounded request ID and valid generation"}
	}
	if _, err := c.workspace(args.WorkspaceID); err != nil {
		return terminal.Info{}, err
	}
	intents, _, err := c.terminalIntents(ctx)
	if err != nil {
		return terminal.Info{}, err
	}
	for _, intent := range intents {
		l := intent.Launch
		if l.TerminalID == args.Target.TerminalID && l.Generation == args.Target.Generation && l.Definition.WorkspaceID == args.WorkspaceID {
			id := intentID(args.WorkspaceID, args.RequestID)
			l.RequestID = id
			l.Generation++
			return c.ensureTerminal(ctx, id, l, "restart")
		}
	}
	return terminal.Info{}, &command.Error{Code: "not_found", Message: "terminal has no known launch definition for that generation"}
}
