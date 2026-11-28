package collab_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dawksh/supertree/internal/collab"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestSharedGoalContextFreshnessQuestionsAndRestart(t *testing.T) {
	ctx := context.Background()
	base := workspace.CanonicalPath(t.TempDir())
	repo := filepath.Join(base, "checkout")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	git("init", "-q", "-b", "main")
	path := filepath.Join(repo, "sample.go")
	if err := os.WriteFile(path, []byte("first\nselected\nthird\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "sample.go")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial")
	options := workspace.Options{StateDir: filepath.Join(base, "state"), WorktreeRoot: filepath.Join(base, "trees"), Directory: repo, TmuxSocket: filepath.Join(base, "absent-tmux")}
	c, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Close() }()
	var registry *command.Registry
	setup := func() {
		registry, _ = core.Registry(func(ctx context.Context, args core.InspectArgs) (workspace.Snapshot, error) {
			return c.Snapshot(args.WorkspaceID)
		})
		if err := c.RegisterCollaboration(registry); err != nil {
			t.Fatal(err)
		}
	}
	setup()
	call := func(actor, name string, args any) (any, error) {
		data, _ := json.Marshal(args)
		return registry.Invoke(command.WithOrigin(ctx, command.Origin{Actor: actor}), name, data)
	}
	s, _ := c.Snapshot("")
	ws := s.Workspaces[0].ID
	value, err := call("human", "task.create", collab.TaskArgs{WorkspaceID: ws, Title: "Fix the failing test", Notes: "Keep the human editing"})
	if err != nil {
		t.Fatal(err)
	}
	task := value.(workspace.Task)
	value, err = call("human", "context.capture", collab.CaptureArgs{WorkspaceID: ws, TaskID: task.ID, Kind: "file", Path: "sample.go", StartLine: 2, EndLine: 2})
	if err != nil {
		t.Fatal(err)
	}
	item := value.(workspace.ContextItem)
	if item.Text != "selected" || item.Stale || item.Actor != "human" {
		t.Fatalf("wrong capture: %#v", item)
	}
	value, err = call("agent:review", "context.export", collab.ExportArgs{WorkspaceID: ws, TaskID: task.ID, ContextIDs: []string{item.ID}})
	if err != nil || !strings.Contains(value.(collab.Export).Text, "Fix the failing test") {
		t.Fatalf("missing shared goal: %v", err)
	}
	if err := os.WriteFile(path, []byte("first\nhuman changed this\nthird\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err = call("human", "context.list", core.InspectArgs{WorkspaceID: ws})
	if err != nil || !value.(core.Contexts).Contexts[0].Stale {
		t.Fatal("human edit was not detected")
	}
	if _, err := call("agent:review", "context.export", collab.ExportArgs{WorkspaceID: ws, ContextIDs: []string{item.ID}}); err == nil {
		t.Fatal("stale context exported without explicit override")
	}
	value, err = call("human", "context.refresh", collab.ContextTarget{ContextID: item.ID, ExpectedRevision: item.Revision})
	if err != nil {
		t.Fatal(err)
	}
	fresh := value.(workspace.ContextItem)
	if fresh.ID != item.ID || fresh.Revision <= item.Revision || fresh.Text != "human changed this" {
		t.Fatal("recapture did not retain identity/freshness")
	}
	outside := filepath.Join(base, "outside")
	os.WriteFile(outside, []byte("private"), 0600)
	os.Symlink(outside, filepath.Join(repo, "escape"))
	if _, err := call("human", "context.capture", collab.CaptureArgs{WorkspaceID: ws, Kind: "file", Path: "escape"}); err == nil {
		t.Fatal("symlink outside checkout was captured")
	}
	value, err = call("human", "context.capture", collab.CaptureArgs{WorkspaceID: ws, Kind: "diff"})
	if err != nil || !strings.Contains(value.(workspace.ContextItem).Text, "human changed this") {
		t.Fatalf("tracked diff capture failed: %v", err)
	}
	value, err = call("agent:review", "question.ask", collab.AskArgs{WorkspaceID: ws, TaskID: task.ID, Prompt: "Keep the public interface?", Options: []string{"yes", "no"}})
	if err != nil {
		t.Fatal(err)
	}
	q := value.(workspace.Question)
	value, err = call("human", "question.answer", collab.AnswerArgs{QuestionID: q.ID, ExpectedRevision: q.Revision, Answer: "yes"})
	if err != nil || value.(workspace.Question).AnsweredBy != "human" {
		t.Fatal("answer attribution missing")
	}
	if _, err := call("agent:other", "question.answer", collab.AnswerArgs{QuestionID: q.ID, ExpectedRevision: q.Revision, Answer: "no"}); err == nil {
		t.Fatal("concurrent participant overwrote an answer")
	}
	empty := ""
	if _, err := call("human", "task.update", collab.TaskUpdate{TaskID: task.ID, Notes: &empty, ExpectedRevision: task.Revision}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, err = core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	setup()
	s, _ = c.Snapshot(ws)
	if len(s.Tasks) != 1 || s.Tasks[0].Notes != "" || len(s.Contexts) != 2 || s.Contexts[0].Stale || len(s.Questions) != 1 || s.Questions[0].Answer != "yes" {
		t.Fatalf("shared state did not survive restart: tasks=%d contexts=%d questions=%d", len(s.Tasks), len(s.Contexts), len(s.Questions))
	}
}
