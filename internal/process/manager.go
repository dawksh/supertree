// Package process owns persistent named terminals. tmux owns their lifetimes;
// reconnecting the core never launches another copy of a program.
package process

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

type StartArgs struct {
	WorkspaceID string            `json:"workspace_id"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind,omitempty"`
	Command     []string          `json:"command"`
	Directory   string            `json:"directory,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}
type Target struct {
	ProcessID        string `json:"process_id"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}
type RegisterArgs struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"`
	PaneID      string `json:"pane_id"`
}
type MoveArgs struct {
	ProcessID        string `json:"process_id"`
	TargetID         string `json:"target_id,omitempty"`
	Direction        string `json:"direction,omitempty"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}
type Attachment struct {
	ProcessID string `json:"process_id"`
	Socket    string `json:"socket"`
	SessionID string `json:"session_id"`
	WindowID  string `json:"window_id"`
	PaneID    string `json:"pane_id"`
}
type OutputArgs struct {
	ProcessID string `json:"process_id"`
	Lines     int    `json:"lines,omitempty"`
}
type Output struct {
	ProcessID  string `json:"process_id"`
	Text       string `json:"text"`
	CapturedAt string `json:"captured_at"`
}
type saved struct {
	Process     workspace.Process `json:"process"`
	Environment map[string]string `json:"environment"`
	RequestKey  string            `json:"request_key"`
	RequestHash string            `json:"request_hash"`
}
type Manager struct {
	mu       sync.Mutex
	store    *store.Store
	client   tmux.Client
	resolve  func(string) (workspace.Workspace, error)
	Defaults map[string]string
}

func New(s *store.Store, client tmux.Client, resolve func(string) (workspace.Workspace, error)) *Manager {
	return &Manager{store: s, client: client, resolve: resolve}
}
func ID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s_%x", prefix, b)
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func invalid(message string) error {
	return &command.Error{Code: "invalid_arguments", Message: message}
}
func conflict(message string) error { return &command.Error{Code: "conflict", Message: message} }
func ValidName(name string) bool {
	return len(name) > 0 && len(name) <= 64 && strings.TrimSpace(name) == name && strings.IndexFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" ._-", r)
	}) < 0
}
func (m *Manager) load(ctx context.Context) ([]saved, error) {
	docs, err := m.store.Documents(ctx, "process")
	if err != nil {
		return nil, err
	}
	items := []saved{}
	for _, doc := range docs {
		var item saved
		if err := json.Unmarshal(doc.Data, &item); err != nil {
			return nil, err
		}
		item.Process.Revision = doc.Revision
		items = append(items, item)
	}
	return items, nil
}
func (m *Manager) save(ctx context.Context, item *saved) error {
	item.Process.UpdatedAt = now()
	revision, err := m.store.PutDocument(ctx, "process", item.Process.ID, item, item.Process.Revision)
	if err == nil {
		item.Process.Revision = revision
	}
	return err
}
func find(items []saved, target Target) (saved, error) {
	for _, item := range items {
		if item.Process.ID == target.ProcessID {
			if target.ExpectedRevision != 0 && target.ExpectedRevision != item.Process.Revision {
				return saved{}, conflict("process changed; inspect before retrying")
			}
			return item, nil
		}
	}
	return saved{}, &command.Error{Code: "not_found", Message: "process not found: " + target.ProcessID}
}
func (m *Manager) available(id string) (workspace.Workspace, error) {
	w, err := m.resolve(id)
	if err != nil {
		return w, err
	}
	if w.State != "available" || w.Kind == "bare" {
		return w, conflict("workspace is not an available checkout")
	}
	return w, nil
}
func unique(items []saved, ws, name string) error {
	for _, item := range items {
		if item.Process.WorkspaceID == ws && item.Process.Name == name {
			return conflict("process name already exists; restart it or choose another name")
		}
	}
	return nil
}

func (m *Manager) Start(ctx context.Context, args StartArgs) (workspace.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	environment := map[string]string{}
	for key, value := range m.Defaults {
		environment[key] = value
	}
	for key, value := range args.Environment {
		environment[key] = value
	}
	args.Environment = environment
	if !ValidName(args.Name) {
		return workspace.Process{}, invalid("name must be 1–64 letters, digits, spaces, dots, underscores or hyphens")
	}
	if len(args.Command) == 0 || args.Command[0] == "" {
		return workspace.Process{}, invalid("command must contain an executable and optional argument array")
	}
	for _, arg := range args.Command {
		if strings.ContainsRune(arg, 0) {
			return workspace.Process{}, invalid("command contains a NUL byte")
		}
	}
	for key, value := range args.Environment {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(key) || strings.ContainsRune(value, 0) {
			return workspace.Process{}, invalid("invalid environment name or value")
		}
	}
	if args.Kind == "" {
		args.Kind = "terminal"
	}
	if !slices.Contains([]string{"terminal", "shell", "editor", "agent", "tests", "server"}, args.Kind) {
		return workspace.Process{}, invalid("unknown process kind")
	}
	w, err := m.available(args.WorkspaceID)
	if err != nil {
		return workspace.Process{}, err
	}
	if args.Directory == "" {
		args.Directory = w.Path
	}
	if !filepath.IsAbs(args.Directory) {
		args.Directory = filepath.Join(w.Path, args.Directory)
	}
	args.Directory = workspace.CanonicalPath(args.Directory)
	rel, err := filepath.Rel(w.Path, args.Directory)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return workspace.Process{}, invalid("process directory must be inside its workspace")
	}
	info, err := os.Stat(args.Directory)
	if err != nil || !info.IsDir() {
		return workspace.Process{}, invalid("process directory is unavailable")
	}
	items, err := m.load(ctx)
	if err != nil {
		return workspace.Process{}, err
	}
	origin := command.Caller(ctx)
	key := ""
	if origin.RequestID != "" {
		key = origin.Actor + "\x00" + origin.RequestID
	}
	encoded, _ := json.Marshal(args)
	hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if key != "" {
		for _, item := range items {
			if item.RequestKey == key {
				if item.RequestHash != hash {
					return workspace.Process{}, conflict("request ID was already used with different process arguments")
				}
				return item.Process, nil
			}
		}
	}
	if err := unique(items, args.WorkspaceID, args.Name); err != nil {
		return workspace.Process{}, err
	}
	keys := []string{}
	for key := range args.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	item := saved{Process: workspace.Process{ID: ID("proc"), WorkspaceID: w.ID, Name: args.Name, Kind: args.Kind, Command: args.Command, EnvironmentKeys: keys, Directory: args.Directory, State: "starting", ExitCode: -1, Owned: true, CreatedAt: now()}, Environment: args.Environment, RequestKey: key, RequestHash: hash}
	if err := m.save(ctx, &item); err != nil {
		return item.Process, err
	}
	err = m.launch(ctx, &item)
	return item.Process, err
}

func (m *Manager) launch(ctx context.Context, item *saved) error {
	p := &item.Process
	sum := sha256.Sum256([]byte(p.WorkspaceID))
	session := fmt.Sprintf("st-next-%x", sum[:8])
	reserved := "stp_" + strings.TrimPrefix(p.ID, "proc_")
	dir := strings.ReplaceAll(p.Directory, "#", "##") + string(filepath.Separator)
	args := []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=" + session, "-n", reserved, "-c", dir, "exec sleep 86400"}
	if _, err := m.client.Run(ctx, "-N", "has-session", "-t", "="+session); err != nil {
		args = []string{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", session, "-n", reserved, "-c", dir, "-x", "120", "-y", "36", "exec sleep 86400"}
	}
	pane, err := m.client.Run(ctx, args...)
	if err == nil {
		p.PaneID = pane
		_, err = m.client.Run(ctx, "set-option", "-p", "-t", pane, "@st_next_process", p.ID)
	}
	if err == nil {
		_, err = m.client.Run(ctx, "set-option", "-p", "-t", pane, "remain-on-exit", "on")
	}
	if err == nil {
		err = m.save(ctx, item)
	}
	if err == nil {
		_, err = m.client.Run(ctx, "respawn-pane", "-k", "-t", pane, "-c", dir, tmux.ShellCommand(p.Command, item.Environment))
	}
	if err == nil {
		_, _ = m.client.Run(ctx, "rename-window", "-t", pane, p.Name)
		live, lookup := m.client.Tagged(ctx, pane, p.ID)
		if lookup != nil {
			err = lookup
		} else {
			apply(p, live)
		}
	}
	if err != nil {
		p.State, p.Error = "failed", err.Error()
		// This pane was created by this launch, never an adopted human pane.
		if pane != "" {
			_, _ = m.client.Run(context.WithoutCancel(ctx), "kill-pane", "-t", pane)
		}
	}
	saveErr := m.save(context.WithoutCancel(ctx), item)
	if err != nil {
		return err
	}
	return saveErr
}
func apply(p *workspace.Process, live tmux.Pane) {
	p.PaneID, p.WindowID, p.SessionID = live.ID, live.WindowID, live.SessionID
	p.State, p.ExitCode, p.Error = "running", -1, ""
	if live.Dead {
		p.State, p.ExitCode = "exited", live.ExitCode
	}
}

func (m *Manager) Snapshot(ctx context.Context) ([]workspace.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := m.load(ctx)
	if err != nil {
		return nil, err
	}
	panes, err := m.client.Panes(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]tmux.Pane{}
	for _, pane := range panes {
		if pane.ProcessID != "" {
			byID[pane.ProcessID] = pane
		}
	}
	result := []workspace.Process{}
	for i := range items {
		item := &items[i]
		before := item.Process
		if live, ok := byID[item.Process.ID]; ok {
			apply(&item.Process, live)
		} else if !slices.Contains([]string{"stopped", "failed", "missing"}, item.Process.State) {
			item.Process.State = "missing"
			item.Process.Error = "terminal no longer exists; restart explicitly"
		}
		if !reflect.DeepEqual(before, item.Process) {
			if err := m.save(ctx, item); err != nil {
				return nil, err
			}
		}
		result = append(result, item.Process)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].WorkspaceID == result[j].WorkspaceID {
			return result[i].Name < result[j].Name
		}
		return result[i].WorkspaceID < result[j].WorkspaceID
	})
	return result, nil
}

func (m *Manager) Register(ctx context.Context, args RegisterArgs) (workspace.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidName(args.Name) {
		return workspace.Process{}, invalid("invalid process name")
	}
	if args.Kind == "" {
		args.Kind = "terminal"
	}
	if !slices.Contains([]string{"terminal", "shell", "editor", "agent", "tests", "server"}, args.Kind) {
		return workspace.Process{}, invalid("unknown process kind")
	}
	w, err := m.available(args.WorkspaceID)
	if err != nil {
		return workspace.Process{}, err
	}
	items, err := m.load(ctx)
	if err != nil {
		return workspace.Process{}, err
	}
	if err := unique(items, w.ID, args.Name); err != nil {
		return workspace.Process{}, err
	}
	panes, err := m.client.Panes(ctx)
	if err != nil {
		return workspace.Process{}, err
	}
	for _, pane := range panes {
		if pane.ID == args.PaneID {
			if pane.ProcessID != "" {
				return workspace.Process{}, conflict("pane is already registered")
			}
			rel, err := filepath.Rel(w.Path, workspace.CanonicalPath(pane.Directory))
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return workspace.Process{}, invalid("pane is outside the selected workspace")
			}
			if args.Kind == "" {
				args.Kind = "terminal"
			}
			item := saved{Process: workspace.Process{ID: ID("proc"), WorkspaceID: w.ID, Name: args.Name, Kind: args.Kind, Command: []string{}, EnvironmentKeys: []string{}, Directory: pane.Directory, CreatedAt: now()}}
			apply(&item.Process, pane)
			if _, err := m.client.Run(ctx, "set-option", "-p", "-t", pane.ID, "@st_next_process", item.Process.ID); err != nil {
				return item.Process, err
			}
			if err := m.save(ctx, &item); err != nil {
				_, _ = m.client.Run(ctx, "set-option", "-pu", "-t", pane.ID, "@st_next_process")
				return item.Process, err
			}
			return item.Process, nil
		}
	}
	return workspace.Process{}, &command.Error{Code: "not_found", Message: "pane not found"}
}

func (m *Manager) Stop(ctx context.Context, args Target) (workspace.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := m.load(ctx)
	if err != nil {
		return workspace.Process{}, err
	}
	item, err := find(items, args)
	if err != nil {
		return workspace.Process{}, err
	}
	if item.Process.State == "stopped" {
		return item.Process, nil
	}
	if _, err := m.client.Tagged(ctx, item.Process.PaneID, item.Process.ID); err != nil {
		return item.Process, conflict(err.Error())
	}
	if _, err := m.client.Run(ctx, "kill-pane", "-t", item.Process.PaneID); err != nil {
		return item.Process, err
	}
	item.Process.State, item.Process.Error = "stopped", ""
	err = m.save(context.WithoutCancel(ctx), &item)
	return item.Process, err
}
func (m *Manager) Restart(ctx context.Context, args Target) (workspace.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := m.load(ctx)
	if err != nil {
		return workspace.Process{}, err
	}
	item, err := find(items, args)
	if err != nil {
		return workspace.Process{}, err
	}
	if !item.Process.Owned || len(item.Process.Command) == 0 {
		return item.Process, conflict("registered external panes have no restart definition")
	}
	if pane, err := m.client.Tagged(ctx, item.Process.PaneID, item.Process.ID); err == nil {
		if !pane.Dead {
			return item.Process, conflict("stop the running process before restarting")
		}
		if _, err := m.client.Run(ctx, "kill-pane", "-t", pane.ID); err != nil {
			return item.Process, err
		}
	}
	if _, err := m.available(item.Process.WorkspaceID); err != nil {
		return item.Process, err
	}
	item.Process.State, item.Process.Error = "starting", ""
	if err := m.save(ctx, &item); err != nil {
		return item.Process, err
	}
	err = m.launch(ctx, &item)
	return item.Process, err
}
func (m *Manager) Attach(ctx context.Context, args Target) (Attachment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := m.load(ctx)
	if err != nil {
		return Attachment{}, err
	}
	item, err := find(items, args)
	if err != nil {
		return Attachment{}, err
	}
	p, err := m.client.Tagged(ctx, item.Process.PaneID, item.Process.ID)
	if err != nil {
		return Attachment{}, conflict(err.Error())
	}
	return Attachment{item.Process.ID, m.client.Socket, p.SessionID, p.WindowID, p.ID}, nil
}
func (m *Manager) Move(ctx context.Context, args MoveArgs) (workspace.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if args.Direction == "" {
		args.Direction = "horizontal"
	}
	if args.Direction != "horizontal" && args.Direction != "vertical" {
		return workspace.Process{}, invalid("direction must be horizontal or vertical")
	}
	items, err := m.load(ctx)
	if err != nil {
		return workspace.Process{}, err
	}
	item, err := find(items, Target{args.ProcessID, args.ExpectedRevision})
	if err != nil {
		return workspace.Process{}, err
	}
	source, err := m.client.Tagged(ctx, item.Process.PaneID, item.Process.ID)
	if err != nil {
		return item.Process, conflict(err.Error())
	}
	if args.TargetID == "" {
		_, err = m.client.Run(ctx, "break-pane", "-d", "-s", source.ID)
	} else {
		target, findErr := find(items, Target{ProcessID: args.TargetID})
		if findErr != nil {
			return item.Process, findErr
		}
		if target.Process.WorkspaceID != item.Process.WorkspaceID || target.Process.ID == item.Process.ID {
			return item.Process, invalid("target must be another process in the same workspace")
		}
		live, lookup := m.client.Tagged(ctx, target.Process.PaneID, target.Process.ID)
		if lookup != nil {
			return item.Process, conflict(lookup.Error())
		}
		axis := "-h"
		if args.Direction == "vertical" {
			axis = "-v"
		}
		_, err = m.client.Run(ctx, "join-pane", "-d", axis, "-s", source.ID, "-t", live.ID)
	}
	if err != nil {
		return item.Process, err
	}
	live, err := m.client.Tagged(ctx, source.ID, item.Process.ID)
	if err != nil {
		return item.Process, err
	}
	apply(&item.Process, live)
	err = m.save(ctx, &item)
	return item.Process, err
}
func (m *Manager) Output(ctx context.Context, args OutputArgs) (Output, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if args.Lines == 0 {
		args.Lines = 200
	}
	if args.Lines < 1 || args.Lines > 2000 {
		return Output{}, invalid("lines must be between 1 and 2000")
	}
	items, err := m.load(ctx)
	if err != nil {
		return Output{}, err
	}
	item, err := find(items, Target{ProcessID: args.ProcessID})
	if err != nil {
		return Output{}, err
	}
	if _, err := m.client.Tagged(ctx, item.Process.PaneID, item.Process.ID); err != nil {
		return Output{}, conflict(err.Error())
	}
	text, err := m.client.Run(ctx, "capture-pane", "-p", "-J", "-t", item.Process.PaneID, "-S", fmt.Sprint(-args.Lines))
	if len(text) > 256<<10 {
		text = text[len(text)-(256<<10):]
	}
	return Output{item.Process.ID, text, now()}, err
}
