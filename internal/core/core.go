// Package core reconciles discovery with durable state and publishes sequenced
// changes. CLI and socket clients use the same command registry.
package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

const HistoryLimit = 256
const SubscriberLimit = 32

type InspectArgs struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type CommandList struct {
	SchemaVersion int                  `json:"schema_version"`
	Commands      []command.Definition `json:"commands"`
}

func Registry(inspect func(context.Context, InspectArgs) (workspace.Snapshot, error)) (*command.Registry, error) {
	r := command.New()
	if err := command.Register(r, command.Definition{Name: "command.list", Version: 1,
		Description: "Discover workspace commands and their argument and result schemas.",
		Effects:     []string{"read"}, Repeatable: true}, func(_ context.Context, _ struct{}) (CommandList, error) {
		return CommandList{SchemaVersion: command.SchemaVersion, Commands: r.Definitions()}, nil
	}); err != nil {
		return nil, err
	}
	err := command.Register(r, command.Definition{Name: "workspace.inspect", Version: 1,
		Description: "Inspect workspaces and tmux sessions, optionally selecting one workspace.",
		Effects:     []string{"read"}, Repeatable: true}, inspect)
	return r, err
}

func Select(snapshot workspace.Snapshot, id string) (workspace.Snapshot, error) {
	if id == "" {
		return snapshot, nil
	}
	for _, w := range snapshot.Workspaces {
		if w.ID == id {
			snapshot.Workspaces = []workspace.Workspace{w}
			snapshot.Sessions = slices.Clone(snapshot.Sessions)
			snapshot.Processes = slices.Clone(snapshot.Processes)
			snapshot.Processes = slices.DeleteFunc(snapshot.Processes, func(p workspace.Process) bool { return p.WorkspaceID != id })
			snapshot.Views = filterViews(snapshot.Views, id)
			return selectSessions(snapshot, w), nil
		}
	}
	return snapshot, &command.Error{Code: "not_found", Message: "workspace not found: " + id}
}

func selectSessions(snapshot workspace.Snapshot, w workspace.Workspace) workspace.Snapshot {
	sessions := snapshot.Sessions[:0]
	for _, session := range snapshot.Sessions {
		if slices.Contains(w.SessionIDs, session.ID) {
			sessions = append(sessions, session)
		}
	}
	snapshot.Sessions = sessions
	return snapshot
}

type Origin = command.Origin

type Event struct {
	SchemaVersion int      `json:"schema_version"`
	Epoch         string   `json:"epoch"`
	Sequence      int64    `json:"sequence"`
	Type          string   `json:"type"`
	CapturedAt    string   `json:"captured_at"`
	WorkspaceIDs  []string `json:"workspace_ids"`
	Actor         string   `json:"actor"`
	RequestID     string   `json:"request_id,omitempty"`
}

type Cursor struct {
	Epoch       string `json:"epoch,omitempty"`
	After       int64  `json:"after,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type Subscription struct {
	Epoch              string             `json:"epoch"`
	Sequence           int64              `json:"sequence"`
	ResnapshotRequired bool               `json:"resnapshot_required"`
	Snapshot           workspace.Snapshot `json:"snapshot"`
}

type subscriber struct {
	workspaceID string
	events      chan Event
}

type Core struct {
	options   workspace.Options
	store     *store.Store
	refresh   sync.Mutex
	mu        sync.Mutex
	state     workspace.Snapshot
	history   []Event
	clients   map[*subscriber]bool
	processes *process.Manager
	views     map[string]viewLease
}

func Open(ctx context.Context, options workspace.Options) (*Core, error) {
	s, err := store.Open(options.StateDir)
	if err != nil {
		return nil, err
	}
	var token [16]byte
	_, _ = rand.Read(token[:])
	c := &Core{options: options, store: s, clients: make(map[*subscriber]bool),
		views: make(map[string]viewLease),
		state: workspace.Snapshot{Epoch: fmt.Sprintf("%x", token)}}
	c.processes = process.New(s, tmux.Client{Socket: options.TmuxSocket}, func(id string) (workspace.Workspace, error) {
		snapshot, err := c.Snapshot(id)
		if err != nil {
			return workspace.Workspace{}, err
		}
		if len(snapshot.Workspaces) != 1 {
			return workspace.Workspace{}, &command.Error{Code: "invalid_arguments", Message: "workspace ID is required"}
		}
		return snapshot.Workspaces[0], nil
	})
	if err := c.Refresh(ctx, options.Directory, Origin{Actor: "system"}); err != nil {
		s.Close()
		return nil, err
	}
	return c, nil
}

func (c *Core) Close() error { return c.store.Close() }

// Refresh serializes discovery and its database commit. Slow subprocesses run
// without holding the state mutex, so subscribers and cached reads stay usable.
func (c *Core) Refresh(ctx context.Context, directory string, origin Origin) error {
	c.refresh.Lock()
	defer c.refresh.Unlock()
	return c.refreshState(ctx, directory, origin)
}

func (c *Core) refreshState(ctx context.Context, directory string, origin Origin) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	options := c.options
	paths, err := c.store.Repositories(ctx)
	if err != nil {
		return err
	}
	options.Repositories = paths
	options.Directory = directory
	next, err := workspace.Discover(ctx, options)
	if err != nil {
		return err
	}
	if err := c.store.Reconcile(ctx, &next); err != nil {
		return err
	}
	next.Processes, err = c.processes.Snapshot(ctx)
	if err != nil {
		return err
	}
	// Administrative identity keys are persistence details, never client state.
	for i := range next.Workspaces {
		next.Workspaces[i].IdentityKey, next.Workspaces[i].RepositoryKey = "", ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	next.Views = c.viewSnapshot()
	next.Epoch, next.Sequence = c.state.Epoch, c.state.Sequence
	old, comparable := c.state, next
	old.CapturedAt, comparable.CapturedAt = "", ""
	if !reflect.DeepEqual(old, comparable) {
		next.Sequence++
		if origin.Actor == "" {
			origin.Actor = "system"
		}
		event := Event{command.SchemaVersion, next.Epoch, next.Sequence, "workspace.changed", next.CapturedAt,
			changedWorkspaces(c.state, next), origin.Actor, origin.RequestID}
		c.history = append(c.history, event)
		if len(c.history) > HistoryLimit {
			c.history = c.history[len(c.history)-HistoryLimit:]
		}
		for client := range c.clients {
			if client.workspaceID != "" && len(event.WorkspaceIDs) > 0 && !slices.Contains(event.WorkspaceIDs, client.workspaceID) {
				continue
			}
			select {
			case client.events <- event:
			default:
				// Never block reconciliation on a slow consumer. The transport
				// reports a gap and the next connection must resnapshot.
				close(client.events)
				delete(c.clients, client)
			}
		}
	}
	c.state = next
	return nil
}

func changedWorkspaces(old, next workspace.Snapshot) []string {
	previous := make(map[string]workspace.Workspace)
	for _, w := range old.Workspaces {
		previous[w.ID] = w
	}
	ids := []string{}
	for _, w := range next.Workspaces {
		if before, ok := previous[w.ID]; !ok || !reflect.DeepEqual(before, w) {
			ids = append(ids, w.ID)
		}
		delete(previous, w.ID)
	}
	for id := range previous {
		ids = append(ids, id)
	}
	beforeProcesses := map[string]workspace.Process{}
	for _, p := range old.Processes {
		beforeProcesses[p.ID] = p
	}
	for _, p := range next.Processes {
		if before, ok := beforeProcesses[p.ID]; !ok || !reflect.DeepEqual(before, p) {
			if !slices.Contains(ids, p.WorkspaceID) {
				ids = append(ids, p.WorkspaceID)
			}
		}
		delete(beforeProcesses, p.ID)
	}
	for _, p := range beforeProcesses {
		if !slices.Contains(ids, p.WorkspaceID) {
			ids = append(ids, p.WorkspaceID)
		}
	}
	slices.Sort(ids)
	return ids
}

func (c *Core) Snapshot(id string) (workspace.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Select(c.state, id)
}

// Subscribe takes a snapshot/cursor and attaches a bounded queue under the same
// lock as publication. No event can slip between snapshot and subscription.
func (c *Core) Subscribe(cursor Cursor) (Subscription, <-chan Event, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cursor.After < 0 {
		return Subscription{}, nil, nil, &command.Error{Code: "invalid_arguments", Message: "after must be nonnegative"}
	}
	snapshot, err := Select(c.state, cursor.WorkspaceID)
	if err != nil {
		return Subscription{}, nil, nil, err
	}
	ack := Subscription{Epoch: c.state.Epoch, Sequence: c.state.Sequence, Snapshot: snapshot}
	gap := cursor.Epoch != c.state.Epoch || cursor.After > c.state.Sequence
	if len(c.history) > 0 && cursor.After < c.history[0].Sequence-1 {
		gap = true
	}
	replay := []Event{}
	if !gap {
		for _, event := range c.history {
			if event.Sequence > cursor.After && (cursor.WorkspaceID == "" || len(event.WorkspaceIDs) == 0 || slices.Contains(event.WorkspaceIDs, cursor.WorkspaceID)) {
				replay = append(replay, event)
			}
		}
	}
	if gap || len(replay) > SubscriberLimit {
		ack.ResnapshotRequired = true
		replay = nil
	}
	client := &subscriber{cursor.WorkspaceID, make(chan Event, SubscriberLimit)}
	for _, event := range replay {
		client.events <- event
	}
	c.clients[client] = true
	return ack, client.events, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.clients[client] {
			delete(c.clients, client)
			close(client.events)
		}
	}, nil
}

func (c *Core) Poll(ctx context.Context, interval time.Duration, report func(error)) {
	kick := make(chan struct{}, 1)
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var watch sync.WaitGroup
	watch.Add(1)
	go func() { defer watch.Done(); c.watchTmux(watchCtx, kick) }()
	defer watch.Wait()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			return
		case <-ticker.C:
		case <-kick:
		}
		if err := c.Refresh(ctx, "", Origin{Actor: "system"}); err != nil && ctx.Err() == nil {
			report(err)
		}
	}
}

func (c *Core) watchTmux(ctx context.Context, kick chan<- struct{}) {
	client := tmux.Client{Socket: c.options.TmuxSocket}
	for ctx.Err() == nil {
		snapshot, _ := c.Snapshot("")
		if len(snapshot.Sessions) > 0 {
			_ = client.Watch(ctx, snapshot.Sessions[0].ID, func() {
				select {
				case kick <- struct{}{}:
				default:
				}
			})
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
