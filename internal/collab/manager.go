// Package collab stores shared goals, captured context, and questions. Capturing
// never delivers context to an agent; delivery is always a separate command.
package collab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	stdhash "hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/workspace"
	"golang.org/x/sys/unix"
)

const TextLimit = 32 << 10

type Manager struct {
	mu      sync.Mutex
	store   *store.Store
	resolve func(string) (workspace.Workspace, error)
	output  func(context.Context, process.OutputArgs) (process.Output, error)
}

func New(s *store.Store, resolve func(string) (workspace.Workspace, error), output func(context.Context, process.OutputArgs) (process.Output, error)) *Manager {
	return &Manager{store: s, resolve: resolve, output: output}
}
func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func invalid(message string) error {
	return &command.Error{Code: "invalid_arguments", Message: message}
}
func notFound(kind string) error {
	return &command.Error{Code: "not_found", Message: kind + " not found"}
}
func hash(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }
func load[T any](ctx context.Context, s *store.Store, kind string) ([]T, error) {
	docs, err := s.Documents(ctx, kind)
	if err != nil {
		return nil, err
	}
	result := []T{}
	for _, doc := range docs {
		var value T
		if err := json.Unmarshal(doc.Data, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

type TaskArgs struct {
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Notes       string `json:"notes,omitempty"`
}
type TaskUpdate struct {
	TaskID           string  `json:"task_id"`
	Title            string  `json:"title,omitempty"`
	Notes            *string `json:"notes,omitempty"`
	Status           string  `json:"status,omitempty"`
	ExpectedRevision int64   `json:"expected_revision"`
}
type CaptureArgs struct {
	WorkspaceID string `json:"workspace_id"`
	TaskID      string `json:"task_id,omitempty"`
	Kind        string `json:"kind"`
	Title       string `json:"title,omitempty"`
	Path        string `json:"path,omitempty"`
	StartLine   int    `json:"start_line,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
	ProcessID   string `json:"process_id,omitempty"`
	Text        string `json:"text,omitempty"`
}
type ContextTarget struct {
	ContextID        string `json:"context_id"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}
type ExportArgs struct {
	WorkspaceID string   `json:"workspace_id"`
	TaskID      string   `json:"task_id,omitempty"`
	ContextIDs  []string `json:"context_ids"`
	AllowStale  bool     `json:"allow_stale,omitempty"`
}
type Export struct {
	Text            string   `json:"text"`
	ContextIDs      []string `json:"context_ids"`
	SourceRevisions []string `json:"source_revisions"`
}
type AskArgs struct {
	Recipient   string   `json:"recipient,omitempty"`
	ContextIDs  []string `json:"context_ids,omitempty"`
	WorkspaceID string   `json:"workspace_id"`
	TaskID      string   `json:"task_id,omitempty"`
	Prompt      string   `json:"prompt"`
	Options     []string `json:"options,omitempty"`
}
type AnswerArgs struct {
	QuestionID       string `json:"question_id"`
	Answer           string `json:"answer"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type Snapshot struct {
	Tasks     []workspace.Task
	Contexts  []workspace.ContextItem
	Questions []workspace.Question
}

func (m *Manager) Task(ctx context.Context, id, ws string) (workspace.Task, error) {
	tasks, err := load[workspace.Task](ctx, m.store, "task")
	if err != nil {
		return workspace.Task{}, err
	}
	for _, t := range tasks {
		if t.ID == id && t.WorkspaceID == ws {
			return t, nil
		}
	}
	return workspace.Task{}, notFound("task")
}

func (m *Manager) ActiveTask(ctx context.Context, ws string) (workspace.Task, error) {
	tasks, err := load[workspace.Task](ctx, m.store, "task")
	if err != nil {
		return workspace.Task{}, err
	}
	var active workspace.Task
	for _, task := range tasks {
		if task.WorkspaceID == ws && task.Status == "open" && task.UpdatedAt > active.UpdatedAt {
			active = task
		}
	}
	return active, nil
}
func (m *Manager) CreateTask(ctx context.Context, args TaskArgs) (workspace.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.resolve(args.WorkspaceID); err != nil {
		return workspace.Task{}, err
	}
	if strings.TrimSpace(args.Title) == "" || len(args.Title) > 200 || len(args.Notes) > TextLimit {
		return workspace.Task{}, invalid("task needs a title up to 200 bytes and notes up to 32 KiB")
	}
	task := workspace.Task{ID: process.ID("task"), WorkspaceID: args.WorkspaceID, Title: args.Title, Notes: args.Notes, Status: "open", Revision: 1, Actor: command.Caller(ctx).Actor, UpdatedAt: timestamp()}
	_, err := m.store.PutDocument(ctx, "task", task.ID, task, 0)
	return task, err
}
func (m *Manager) UpdateTask(ctx context.Context, args TaskUpdate) (workspace.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tasks, err := load[workspace.Task](ctx, m.store, "task")
	if err != nil {
		return workspace.Task{}, err
	}
	for _, task := range tasks {
		if task.ID == args.TaskID {
			if task.Revision != args.ExpectedRevision {
				return task, &command.Error{Code: "conflict", Message: "task changed; inspect it before updating"}
			}
			if len(args.Title) > 200 || args.Notes != nil && len(*args.Notes) > TextLimit {
				return task, invalid("task text exceeds its limit")
			}
			if args.Title != "" {
				task.Title = args.Title
			}
			if args.Notes != nil {
				task.Notes = *args.Notes
			}
			if args.Status != "" {
				if args.Status != "open" && args.Status != "done" {
					return task, invalid("task status must be open or done")
				}
				task.Status = args.Status
			}
			task.Revision++
			task.Actor, task.UpdatedAt = command.Caller(ctx).Actor, timestamp()
			_, err = m.store.PutDocument(ctx, "task", task.ID, task, args.ExpectedRevision)
			return task, err
		}
	}
	return workspace.Task{}, notFound("task")
}
func (m *Manager) Capture(ctx context.Context, args CaptureArgs) (workspace.ContextItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item := workspace.ContextItem{ID: process.ID("ctx"), WorkspaceID: args.WorkspaceID, TaskID: args.TaskID, Kind: args.Kind, Title: args.Title, Path: args.Path, StartLine: args.StartLine, EndLine: args.EndLine, ProcessID: args.ProcessID, Text: args.Text, Revision: 1, Actor: command.Caller(ctx).Actor, CapturedAt: timestamp()}
	if args.TaskID != "" {
		if _, err := m.Task(ctx, args.TaskID, args.WorkspaceID); err != nil {
			return item, err
		}
	}
	if err := m.capture(ctx, &item); err != nil {
		return item, err
	}
	_, err := m.store.PutDocument(ctx, "context", item.ID, item, 0)
	return item, err
}
func (m *Manager) capture(ctx context.Context, item *workspace.ContextItem) error {
	w, err := m.resolve(item.WorkspaceID)
	if err != nil {
		return err
	}
	item.Stale, item.Truncated = false, false
	switch item.Kind {
	case "note":
		if strings.TrimSpace(item.Text) == "" || len(item.Text) > TextLimit {
			return invalid("note must contain 1–32768 bytes")
		}
		item.SourceRevision = hash(item.Text)
	case "file":
		path := item.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(w.Path, path)
		}
		path = workspace.CanonicalPath(path)
		rel, err := filepath.Rel(w.Path, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return invalid("context file must be inside the selected checkout")
		}
		data, err := readTextFile(path)
		if err != nil {
			return err
		}
		if strings.ContainsRune(string(data), 0) {
			return invalid("context file must be text")
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if item.StartLine == 0 {
			item.StartLine = 1
		}
		if item.EndLine == 0 {
			item.EndLine = len(lines)
		}
		if item.StartLine < 1 || item.EndLine < item.StartLine || item.EndLine > len(lines) {
			return invalid("file line range is outside the file")
		}
		item.Path = rel
		item.Text = strings.Join(lines[item.StartLine-1:item.EndLine], "\n")
		item.SourceRevision = hash(string(data))
	case "diff":
		text, revision, truncated, err := diff(ctx, w.Path)
		if err != nil {
			return err
		}
		item.Text, item.SourceRevision, item.Truncated = text, revision, truncated
	case "output":
		out, err := m.output(ctx, process.OutputArgs{ProcessID: item.ProcessID, Lines: 200})
		if err != nil {
			return err
		}
		// The process must belong to this workspace, even when an explicit ID
		// was copied from a different workspace's inspection result.
		docs, err := m.store.Documents(ctx, "process")
		if err != nil {
			return err
		}
		owned := false
		for _, doc := range docs {
			if doc.ID == item.ProcessID {
				var saved struct {
					Process workspace.Process `json:"process"`
				}
				if json.Unmarshal(doc.Data, &saved) == nil && saved.Process.WorkspaceID == item.WorkspaceID {
					owned = true
				}
			}
		}
		if !owned {
			return invalid("process is not in the selected workspace")
		}
		item.Text, item.SourceRevision = out.Text, hash(out.Text)
	default:
		return invalid("context kind must be note, file, diff, or output")
	}
	if len(item.Text) > TextLimit {
		item.Text = item.Text[:TextLimit]
		item.Truncated = true
	}
	if item.Title == "" {
		item.Title = item.Kind
		if item.Path != "" {
			item.Title = item.Path
		}
	}
	if len(item.Title) > 200 {
		return invalid("context title exceeds 200 bytes")
	}
	return nil
}
func readTextFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, invalid("context source must be a regular text file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, invalid("context file exceeds 1 MiB")
	}
	return data, nil
}

type diffCapture struct {
	hash   stdhash.Hash
	prefix bytes.Buffer
	size   int
}

func (c *diffCapture) Write(data []byte) (int, error) {
	n := len(data)
	_, _ = c.hash.Write(data)
	if c.prefix.Len() < TextLimit {
		_, _ = c.prefix.Write(data[:min(n, TextLimit-c.prefix.Len())])
	}
	c.size += n
	return n, nil
}
func diff(ctx context.Context, path string) (string, string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"-C", path, "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--"}
	if exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--verify", "HEAD").Run() != nil {
		args = []string{"-C", path, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--"}
	}
	capture := &diffCapture{hash: sha256.New()}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = capture
	err := cmd.Run()
	return capture.prefix.String(), fmt.Sprintf("%x", capture.hash.Sum(nil)), capture.size > TextLimit, err
}
func (m *Manager) RefreshContext(ctx context.Context, args ContextTarget) (workspace.ContextItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := load[workspace.ContextItem](ctx, m.store, "context")
	if err != nil {
		return workspace.ContextItem{}, err
	}
	for _, item := range items {
		if item.ID == args.ContextID {
			if args.ExpectedRevision != 0 && args.ExpectedRevision != item.Revision {
				return item, &command.Error{Code: "conflict", Message: "context changed; inspect it before recapturing"}
			}
			old := item.Revision
			if err := m.capture(ctx, &item); err != nil {
				return item, err
			}
			item.Revision++
			item.CapturedAt, item.Actor = timestamp(), command.Caller(ctx).Actor
			_, err := m.store.PutDocument(ctx, "context", item.ID, item, old)
			return item, err
		}
	}
	return workspace.ContextItem{}, notFound("context")
}
func (m *Manager) Snapshot(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tasks, err := load[workspace.Task](ctx, m.store, "task")
	if err != nil {
		return Snapshot{}, err
	}
	items, err := load[workspace.ContextItem](ctx, m.store, "context")
	if err != nil {
		return Snapshot{}, err
	}
	questions, err := load[workspace.Question](ctx, m.store, "question")
	if err != nil {
		return Snapshot{}, err
	}
	diffHashes := map[string]string{}
	for i := range items {
		item := &items[i]
		w, err := m.resolve(item.WorkspaceID)
		if err != nil {
			item.Stale = true
			continue
		}
		switch item.Kind {
		case "file":
			path := workspace.CanonicalPath(filepath.Join(w.Path, item.Path))
			rel, e := filepath.Rel(w.Path, path)
			if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				item.Stale = true
				continue
			}
			data, e := readTextFile(path)
			item.Stale = e != nil || hash(string(data)) != item.SourceRevision
		case "diff":
			if revision, ok := diffHashes[w.Path]; ok {
				item.Stale = revision != item.SourceRevision
			} else {
				_, revision, _, e := diff(ctx, w.Path)
				if e != nil {
					revision = ""
				}
				diffHashes[w.Path] = revision
				item.Stale = e != nil || revision != item.SourceRevision
			}
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].UpdatedAt < tasks[j].UpdatedAt })
	sort.Slice(items, func(i, j int) bool { return items[i].CapturedAt < items[j].CapturedAt })
	sort.Slice(questions, func(i, j int) bool { return questions[i].CreatedAt < questions[j].CreatedAt })
	return Snapshot{tasks, items, questions}, nil
}
func (m *Manager) Export(ctx context.Context, args ExportArgs) (Export, error) {
	if _, err := m.resolve(args.WorkspaceID); err != nil {
		return Export{}, err
	}
	if len(args.ContextIDs) > 16 {
		return Export{}, invalid("deliver at most 16 context items at once")
	}
	snapshot, err := m.Snapshot(ctx)
	if err != nil {
		return Export{}, err
	}
	result := Export{ContextIDs: []string{}, SourceRevisions: []string{}}
	if args.TaskID != "" {
		task, err := m.Task(ctx, args.TaskID, args.WorkspaceID)
		if err != nil {
			return result, err
		}
		result.Text = "Shared goal: " + task.Title + "\n" + task.Notes + "\n\n"
	}
	seen := map[string]bool{}
	for _, id := range args.ContextIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		found := false
		for _, item := range snapshot.Contexts {
			if item.ID != id || item.WorkspaceID != args.WorkspaceID {
				continue
			}
			found = true
			if item.Stale && !args.AllowStale {
				return result, &command.Error{Code: "stale_context", Message: "recapture stale context before delivery: " + id}
			}
			result.Text += fmt.Sprintf("<captured-context id=%q kind=%q title=%q source=%q start_line=%d end_line=%d process_id=%q revision=%q captured_at=%q stale=%t truncated=%t>\n%s\n</captured-context>\n\n", item.ID, item.Kind, item.Title, item.Path, item.StartLine, item.EndLine, item.ProcessID, item.SourceRevision, item.CapturedAt, item.Stale, item.Truncated, item.Text)
			result.ContextIDs = append(result.ContextIDs, item.ID)
			result.SourceRevisions = append(result.SourceRevisions, item.SourceRevision)
		}
		if !found {
			return result, notFound("context")
		}
	}
	return result, nil
}
func (m *Manager) Ask(ctx context.Context, args AskArgs) (workspace.Question, error) {
	if args.Recipient == "" {
		args.Recipient = "agent"
		if strings.HasPrefix(command.Caller(ctx).Actor, "agent:") {
			args.Recipient = "human"
		}
	}
	if args.Recipient != "human" && args.Recipient != "agent" {
		return workspace.Question{}, invalid("question recipient must be human or agent")
	}
	if len(args.ContextIDs) > 0 {
		exported, err := m.Export(ctx, ExportArgs{WorkspaceID: args.WorkspaceID, TaskID: args.TaskID, ContextIDs: args.ContextIDs})
		if err != nil {
			return workspace.Question{}, err
		}
		args.ContextIDs = exported.ContextIDs
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.resolve(args.WorkspaceID); err != nil {
		return workspace.Question{}, err
	}
	if args.TaskID != "" {
		if _, err := m.Task(ctx, args.TaskID, args.WorkspaceID); err != nil {
			return workspace.Question{}, err
		}
	}
	if strings.TrimSpace(args.Prompt) == "" || len(args.Prompt) > 8000 || len(args.Options) > 12 {
		return workspace.Question{}, invalid("question needs a prompt up to 8000 bytes and at most 12 options")
	}
	for _, option := range args.Options {
		if len(option) > 500 {
			return workspace.Question{}, invalid("question option exceeds 500 bytes")
		}
	}
	if args.Options == nil {
		args.Options = []string{}
	}
	q := workspace.Question{ID: process.ID("question"), WorkspaceID: args.WorkspaceID, TaskID: args.TaskID, Kind: "question", Prompt: args.Prompt, Options: args.Options, Status: "open", Revision: 1, Actor: command.Caller(ctx).Actor, CreatedAt: timestamp(), Recipient: args.Recipient, ContextIDs: args.ContextIDs}
	_, err := m.store.PutDocument(ctx, "question", q.ID, q, 0)
	return q, err
}
func (m *Manager) Answer(ctx context.Context, args AnswerArgs) (workspace.Question, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := load[workspace.Question](ctx, m.store, "question")
	if err != nil {
		return workspace.Question{}, err
	}
	if strings.TrimSpace(args.Answer) == "" || len(args.Answer) > 8000 {
		return workspace.Question{}, invalid("answer must contain 1–8000 bytes")
	}
	for _, q := range items {
		if q.ID == args.QuestionID {
			if q.Status != "open" || q.Revision != args.ExpectedRevision {
				return q, &command.Error{Code: "conflict", Message: "question was already answered or changed"}
			}
			old := q.Revision
			q.Revision++
			q.Status, q.Answer, q.AnsweredBy = "answered", args.Answer, command.Caller(ctx).Actor
			_, err := m.store.PutDocument(ctx, "question", q.ID, q, old)
			return q, err
		}
	}
	return workspace.Question{}, notFound("question")
}
