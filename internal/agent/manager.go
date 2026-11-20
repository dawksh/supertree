package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dawksh/supertree/internal/collab"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/workspace"
)

type StartArgs struct {
	WorkspaceID string `json:"workspace_id"`
	TaskID      string `json:"task_id,omitempty"`
	Name        string `json:"name"`
	Model       string `json:"model,omitempty"`
	ThreadID    string `json:"thread_id,omitempty"`
}
type Target struct {
	AgentID        string `json:"agent_id"`
	ExpectedTurnID string `json:"expected_turn_id,omitempty"`
}
type SendArgs struct {
	AgentID    string   `json:"agent_id"`
	Message    string   `json:"message"`
	ContextIDs []string `json:"context_ids,omitempty"`
	AllowStale bool     `json:"allow_stale,omitempty"`
}
type DeliveryTarget struct {
	DeliveryID string `json:"delivery_id"`
}
type record struct {
	Agent       workspace.Agent `json:"agent"`
	Socket      string          `json:"socket"`
	RequestKey  string          `json:"request_key"`
	RequestHash string          `json:"request_hash"`
	StartedAt   string          `json:"started_at"`
}
type deliveryRecord struct {
	Delivery   workspace.Delivery `json:"delivery"`
	RequestKey string             `json:"request_key"`
	Hash       string             `json:"hash"`
}
type Manager struct {
	mu        sync.Mutex
	store     *store.Store
	processes *process.Manager
	collab    *collab.Manager
	stateDir  string
	resolve   func(string) (workspace.Workspace, error)
}

func New(s *store.Store, p *process.Manager, c *collab.Manager, stateDir string, resolve func(string) (workspace.Workspace, error)) *Manager {
	return &Manager{store: s, processes: p, collab: c, stateDir: stateDir, resolve: resolve}
}
func load[T any](ctx context.Context, s *store.Store, kind string) ([]T, error) {
	docs, err := s.Documents(ctx, kind)
	if err != nil {
		return nil, err
	}
	items := []T{}
	for _, doc := range docs {
		var item T
		if err := json.Unmarshal(doc.Data, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
func requestKey(ctx context.Context) string {
	origin := command.Caller(ctx)
	if origin.RequestID == "" {
		return ""
	}
	return origin.Actor + "\x00" + origin.RequestID
}
func humanActor(actor string) bool {
	return actor == "human" || strings.HasPrefix(actor, "human:")
}
func argumentHash(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func (m *Manager) save(ctx context.Context, r *record, expected int64) error {
	r.Agent.Revision = expected + 1
	_, err := m.store.PutDocument(ctx, "agent", r.Agent.ID, r, expected)
	return err
}
func (m *Manager) find(ctx context.Context, id string) (record, error) {
	items, err := load[record](ctx, m.store, "agent")
	if err != nil {
		return record{}, err
	}
	for _, r := range items {
		if r.Agent.ID == id {
			return r, nil
		}
	}
	return record{}, &command.Error{Code: "not_found", Message: "agent not found"}
}
func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("agent directory must be private and owned by you: %s", path)
	}
	return nil
}
func (m *Manager) Start(ctx context.Context, args StartArgs) (workspace.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !process.ValidName(args.Name) || len(args.Model) > 200 || len(args.ThreadID) > 200 {
		return workspace.Agent{}, &command.Error{Code: "invalid_arguments", Message: "agent needs a valid process name and model/thread values up to 200 bytes"}
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return workspace.Agent{}, &command.Error{Code: "dependency_missing", Message: "install Codex and sign in before starting a native agent"}
	}
	w, err := m.resolve(args.WorkspaceID)
	if err != nil {
		return workspace.Agent{}, err
	}
	if w.State != "available" || w.Kind == "bare" {
		return workspace.Agent{}, &command.Error{Code: "conflict", Message: "native agent requires an available checkout"}
	}
	if args.TaskID != "" {
		if _, err := m.collab.Task(ctx, args.TaskID, args.WorkspaceID); err != nil {
			return workspace.Agent{}, err
		}
	}
	items, err := load[record](ctx, m.store, "agent")
	if err != nil {
		return workspace.Agent{}, err
	}
	key, hash := requestKey(ctx), argumentHash(args)
	for _, r := range items {
		if key != "" && r.RequestKey == key {
			if r.RequestHash != hash {
				return r.Agent, &command.Error{Code: "conflict", Message: "request ID already used for another agent launch"}
			}
			return r.Agent, nil
		}
		if r.Agent.WorkspaceID == args.WorkspaceID && r.Agent.Name == args.Name {
			return r.Agent, &command.Error{Code: "conflict", Message: "agent name already exists; restart its named process to resume"}
		}
	}
	id := process.ID("agent")
	sum := sha256.Sum256([]byte(m.stateDir))
	dir := fmt.Sprintf("/tmp/supertree-agents-%d", os.Getuid())
	if err := privateDirectory(dir); err != nil {
		return workspace.Agent{}, err
	}
	socket := filepath.Join(dir, fmt.Sprintf("%x-%s.sock", sum[:8], strings.TrimPrefix(id, "agent_")))
	r := record{Agent: workspace.Agent{ID: id, WorkspaceID: args.WorkspaceID, TaskID: args.TaskID, Name: args.Name, Provider: "codex", State: "connecting", Model: args.Model, ThreadID: args.ThreadID, Capabilities: []string{}, Messages: []workspace.AgentMessage{}, Questions: []workspace.Question{}}, Socket: socket, RequestKey: key, RequestHash: hash, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := m.save(ctx, &r, 0); err != nil {
		return r.Agent, err
	}
	exe, err := os.Executable()
	if err != nil {
		return r.Agent, err
	}
	argv := []string{exe, "_agent", "--id", id, "--workspace", args.WorkspaceID, "--task", args.TaskID, "--cwd", w.Path, "--socket", socket, "--state", filepath.Join(m.stateDir, "next", id+".json"), "--model", args.Model, "--thread", args.ThreadID}
	p, err := m.processes.Start(ctx, process.StartArgs{WorkspaceID: args.WorkspaceID, Name: args.Name, Kind: "agent", Command: argv, Environment: map[string]string{"ST_NEXT_ACTOR": "agent:" + id}})
	r.Agent.ProcessID = p.ID
	if err != nil {
		r.Agent.State, r.Agent.Error = "failed", err.Error()
	}
	saveErr := m.save(context.WithoutCancel(ctx), &r, r.Agent.Revision)
	if err != nil {
		return r.Agent, err
	}
	return r.Agent, saveErr
}
func (m *Manager) Snapshot(ctx context.Context) ([]workspace.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := load[record](ctx, m.store, "agent")
	if err != nil {
		return nil, err
	}
	result := []workspace.Agent{}
	for _, r := range items {
		probe, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		reply, err := workerCall(probe, r.Socket, workerRequest{Method: "inspect"})
		cancel()
		next := r.Agent
		if err == nil && reply.Agent.ID == r.Agent.ID && reply.Agent.WorkspaceID == r.Agent.WorkspaceID {
			next = reply.Agent
			next.Name, next.ProcessID, next.TaskID = r.Agent.Name, r.Agent.ProcessID, r.Agent.TaskID
			next.Revision = r.Agent.Revision
		} else if r.Agent.State != "failed" && r.Agent.State != "stopped" {
			started, _ := time.Parse(time.RFC3339Nano, r.StartedAt)
			if r.Agent.State == "connecting" && time.Since(started) < 45*time.Second {
				result = append(result, r.Agent)
				continue
			}
			next.State, next.Error = "unavailable", "agent worker is not connected; inspect its named process and restart explicitly"
		}
		if !reflect.DeepEqual(next, r.Agent) {
			old := r.Agent.Revision
			r.Agent = next
			if err := m.save(ctx, &r, old); err != nil {
				return nil, err
			}
		}
		result = append(result, r.Agent)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func (m *Manager) Send(ctx context.Context, args SendArgs) (workspace.Delivery, error) {
	m.mu.Lock()
	unlock := sync.OnceFunc(m.mu.Unlock)
	defer unlock()
	r, err := m.find(ctx, args.AgentID)
	if err != nil {
		return workspace.Delivery{}, err
	}
	if strings.TrimSpace(args.Message) == "" || len(args.Message) > 32<<10 {
		return workspace.Delivery{}, &command.Error{Code: "invalid_arguments", Message: "message must contain 1–32768 bytes"}
	}
	if args.ContextIDs == nil {
		args.ContextIDs = []string{}
	}
	key, hash := requestKey(ctx), argumentHash(args)
	records, err := load[deliveryRecord](ctx, m.store, "delivery")
	if err != nil {
		return workspace.Delivery{}, err
	}
	if key != "" {
		for _, old := range records {
			if old.RequestKey == key {
				if old.Hash != hash {
					return old.Delivery, &command.Error{Code: "conflict", Message: "delivery request ID was already used with different content"}
				}
				return old.Delivery, nil
			}
		}
	}
	exported, err := m.collab.Export(ctx, collab.ExportArgs{WorkspaceID: r.Agent.WorkspaceID, TaskID: r.Agent.TaskID, ContextIDs: args.ContextIDs, AllowStale: args.AllowStale})
	if err != nil {
		return workspace.Delivery{}, err
	}
	d := deliveryRecord{Delivery: workspace.Delivery{ID: process.ID("delivery"), AgentID: r.Agent.ID, WorkspaceID: r.Agent.WorkspaceID, ContextIDs: exported.ContextIDs, SourceRevisions: exported.SourceRevisions, Text: args.Message + "\n\n" + exported.Text, Status: "pending", Actor: command.Caller(ctx).Actor, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, RequestKey: key, Hash: hash}
	if len(d.Delivery.Text) > 512<<10 {
		return workspace.Delivery{}, &command.Error{Code: "invalid_arguments", Message: "delivery exceeds 512 KiB; select fewer context items"}
	}
	if _, err := m.store.PutDocument(ctx, "delivery", d.Delivery.ID, d, 0); err != nil {
		return d.Delivery, err
	}
	unlock()
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	reply, callErr := workerCall(requestCtx, r.Socket, workerRequest{Method: "send", DeliveryID: d.Delivery.ID, Text: d.Delivery.Text, Actor: d.Delivery.Actor})
	d.Delivery.Status, d.Delivery.TurnID = "sent", reply.TurnID
	if callErr != nil {
		d.Delivery.Status, d.Delivery.Error = "unknown", callErr.Error()
		var coded *command.Error
		if errors.As(callErr, &coded) && (coded.Code == "busy" || coded.Code == "invalid_arguments") {
			d.Delivery.Status = "failed"
		}
	}
	_, err = m.store.PutDocument(context.WithoutCancel(ctx), "delivery", d.Delivery.ID, d, 1)
	// The receipt is the result even if the transport timed out. Callers inspect
	// it instead of silently retrying a possibly accepted provider turn.
	return d.Delivery, err
}
func (m *Manager) Delivery(ctx context.Context, args DeliveryTarget) (workspace.Delivery, error) {
	items, err := m.store.Documents(ctx, "delivery")
	if err != nil {
		return workspace.Delivery{}, err
	}
	for _, doc := range items {
		var r deliveryRecord
		if err := json.Unmarshal(doc.Data, &r); err != nil {
			return workspace.Delivery{}, err
		}
		if r.Delivery.ID == args.DeliveryID {
			if r.Delivery.Status == "unknown" || r.Delivery.Status == "pending" {
				if owner, err := m.find(ctx, r.Delivery.AgentID); err == nil {
					probe, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
					reply, err := workerCall(probe, owner.Socket, workerRequest{Method: "receipt", DeliveryID: r.Delivery.ID})
					cancel()
					if err == nil && reply.TurnID != "" {
						r.Delivery.Status, r.Delivery.TurnID, r.Delivery.Error = "sent", reply.TurnID, ""
						_, _ = m.store.PutDocument(ctx, "delivery", r.Delivery.ID, r, doc.Revision)
					}
				}
			}
			return r.Delivery, nil
		}
	}
	return workspace.Delivery{}, &command.Error{Code: "not_found", Message: "delivery not found"}
}

type DeliveryListArgs struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}
type Deliveries struct {
	Deliveries []workspace.Delivery `json:"deliveries"`
}

func (m *Manager) Deliveries(ctx context.Context, args DeliveryListArgs) (Deliveries, error) {
	if args.Limit == 0 {
		args.Limit = 20
	}
	if args.Limit < 1 || args.Limit > 100 {
		return Deliveries{}, &command.Error{Code: "invalid_arguments", Message: "delivery list limit must be 1–100"}
	}
	items, err := load[deliveryRecord](ctx, m.store, "delivery")
	if err != nil {
		return Deliveries{}, err
	}
	result := []workspace.Delivery{}
	for _, r := range items {
		d := r.Delivery
		if args.WorkspaceID != "" && d.WorkspaceID != args.WorkspaceID || args.AgentID != "" && d.AgentID != args.AgentID {
			continue
		}
		d.Text = ""
		result = append(result, d)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	if len(result) > args.Limit {
		result = result[:args.Limit]
	}
	return Deliveries{result}, nil
}
func (m *Manager) Interrupt(ctx context.Context, args Target) (workspace.Agent, error) {
	r, err := m.find(ctx, args.AgentID)
	if err != nil {
		return workspace.Agent{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reply, err := workerCall(callCtx, r.Socket, workerRequest{Method: "interrupt", Actor: command.Caller(ctx).Actor, ExpectedTurnID: args.ExpectedTurnID})
	if err != nil {
		return r.Agent, err
	}
	reply.Agent.Name, reply.Agent.ProcessID = r.Agent.Name, r.Agent.ProcessID
	return reply.Agent, nil
}
func (m *Manager) Answer(ctx context.Context, args collab.AnswerArgs) (workspace.Question, error) {
	items, err := load[record](ctx, m.store, "agent")
	if err != nil {
		return workspace.Question{}, err
	}
	for _, r := range items {
		for _, q := range r.Agent.Questions {
			if q.ID != args.QuestionID {
				continue
			}
			if q.Kind == "approval" && !humanActor(command.Caller(ctx).Actor) {
				return q, &command.Error{Code: "human_required", Message: "provider approvals require an explicit human response"}
			}
			if args.ExpectedRevision != q.Revision {
				return q, &command.Error{Code: "conflict", Message: "provider question changed"}
			}
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			_, err := workerCall(callCtx, r.Socket, workerRequest{Method: "answer", QuestionID: q.ID, Answer: args.Answer, Actor: command.Caller(ctx).Actor})
			if err != nil {
				return q, err
			}
			q.Status, q.Answer, q.AnsweredBy = "answered", args.Answer, command.Caller(ctx).Actor
			q.Revision++
			return q, nil
		}
	}
	return workspace.Question{}, &command.Error{Code: "not_found", Message: "provider question is no longer pending"}
}
