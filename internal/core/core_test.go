package core_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/workspace"
)

func git(t *testing.T, path string, args ...string) {
	t.Helper()
	if data, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, data)
	}
}

func fixture(t *testing.T) (workspace.Options, string) {
	t.Helper()
	base := workspace.CanonicalPath(t.TempDir())
	git(t, base, "init", "-q", "-b", "main")
	git(t, base, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-q", "--allow-empty", "-m", "initial")
	sockets, err := os.MkdirTemp("/tmp", "st-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	return workspace.Options{StateDir: filepath.Join(base, "state"), WorktreeRoot: filepath.Join(base, "trees"), Directory: base, TmuxSocket: filepath.Join(sockets, "tmux")}, base
}

func TestRealWorktreeMoveAndRestartPreserveIdentity(t *testing.T) {
	options, repo := fixture(t)
	linked := filepath.Join(options.WorktreeRoot, "feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", linked)
	ctx := context.Background()
	c, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := c.Snapshot("")
	var id, repoID string
	for _, w := range before.Workspaces {
		if w.Path == linked {
			id, repoID = w.ID, w.RepositoryID
		}
	}
	moved := filepath.Join(options.WorktreeRoot, "moved 雪")
	git(t, repo, "worktree", "move", linked, moved)
	if err := c.Refresh(ctx, moved, core.Origin{Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	after, err := c.Snapshot(id)
	if err != nil || len(after.Workspaces) != 1 || after.Workspaces[0].Path != moved || after.Workspaces[0].RepositoryID != repoID {
		t.Fatalf("move lost identity: %#v %v", after, err)
	}
	c.Close()
	c, err = core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	restarted, err := c.Snapshot(id)
	if err != nil || restarted.Workspaces[0].Path != moved || restarted.Epoch == before.Epoch {
		t.Fatalf("restart: %#v %v", restarted, err)
	}
	ack, _, cancel, err := c.Subscribe(core.Cursor{Epoch: before.Epoch, After: before.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if !ack.ResnapshotRequired || ack.Snapshot.Epoch != restarted.Epoch {
		t.Fatal("previous daemon's cursor did not require resnapshot")
	}
}

func TestEventReplayGapsSlowConsumersAndUnchangedRefresh(t *testing.T) {
	options, repo := fixture(t)
	c, err := core.Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	initial, err := c.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	ack, slow, cancel, err := c.Subscribe(core.Cursor{})
	if err != nil || !ack.ResnapshotRequired {
		t.Fatalf("initial snapshot missing: %#v %v", ack, err)
	}
	defer cancel()
	if err := c.Refresh(context.Background(), repo, core.Origin{Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := c.Snapshot("")
	if unchanged.Sequence != initial.Sequence {
		t.Fatal("timestamp-only refresh emitted a change")
	}
	for i := 0; i < core.HistoryLimit+2; i++ {
		git(t, repo, "symbolic-ref", "HEAD", fmt.Sprintf("refs/heads/test-%d", i))
		if err := c.Refresh(context.Background(), repo, core.Origin{Actor: "agent:test", RequestID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			ack, replay, close, err := c.Subscribe(core.Cursor{Epoch: initial.Epoch, After: initial.Sequence})
			if err != nil || ack.ResnapshotRequired {
				t.Fatalf("recent cursor could not replay: %#v %v", ack, err)
			}
			event := <-replay
			close()
			if event.Sequence != initial.Sequence+1 || event.Actor != "agent:test" || event.RequestID != "0" || len(event.WorkspaceIDs) != 1 {
				t.Fatalf("incorrect event attribution or sequence: %#v", event)
			}
		}
	}
	// Slow subscribers lose their queue rather than blocking other clients.
	count := 0
	for range slow {
		count++
	}
	if count != core.SubscriberLimit {
		t.Fatalf("unexpected slow consumer queue: %d", count)
	}
	ack, _, close, err := c.Subscribe(core.Cursor{Epoch: initial.Epoch, After: initial.Sequence})
	if err != nil || !ack.ResnapshotRequired || ack.Snapshot.Sequence != ack.Sequence {
		t.Fatalf("history gap did not return a fresh snapshot: %#v %v", ack, err)
	}
	close()
	latest, _ := c.Snapshot("")
	ack, _, close, err = c.Subscribe(core.Cursor{Epoch: latest.Epoch, After: latest.Sequence + 1})
	if err != nil || !ack.ResnapshotRequired {
		t.Fatalf("future cursor accepted: %#v %v", ack, err)
	}
	close()
	if _, _, _, err := c.Subscribe(core.Cursor{After: -1}); err == nil {
		t.Fatal("negative cursor accepted")
	}
}

func TestMainCheckoutRenameRetainsIdentityAndMissingRepository(t *testing.T) {
	base := workspace.CanonicalPath(t.TempDir())
	repo := filepath.Join(base, "original")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	options := workspace.Options{StateDir: filepath.Join(base, "state"), WorktreeRoot: filepath.Join(base, "trees"), Directory: repo, TmuxSocket: filepath.Join("/tmp", "st-missing-tmux")}
	c, err := core.Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first, _ := c.Snapshot("")
	id, repoID := first.Workspaces[0].ID, first.Workspaces[0].RepositoryID
	moved := filepath.Join(base, "renamed")
	if err := os.Rename(repo, moved); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(context.Background(), moved, core.Origin{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(context.Background(), "", core.Origin{Actor: "system"}); err != nil {
		t.Fatal(err)
	}
	after, err := c.Snapshot(id)
	if err != nil || after.Workspaces[0].Path != moved || after.Workspaces[0].RepositoryID != repoID || len(after.Warnings) != 0 {
		t.Fatalf("main checkout rename failed: %#v %v", after, err)
	}
	if err := os.RemoveAll(moved); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(context.Background(), "", core.Origin{Actor: "system"}); err != nil {
		t.Fatal(err)
	}
	missing, err := c.Snapshot(id)
	if err != nil || missing.Workspaces[0].State != "missing" || len(missing.Warnings) != 1 {
		t.Fatalf("missing repository vanished: %#v %v", missing, err)
	}
}
