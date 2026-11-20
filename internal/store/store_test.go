package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestDurableIdentityImportMoveCopyMissingAndRestart(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	s, err := store.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	path := filepath.Join(base, "original")
	w := workspace.Workspace{ID: "ws_original", RepositoryID: "repo_original", Path: path,
		RepositoryPath: path, IdentityKey: "worktree-inode", RepositoryKey: "repo-inode", State: "available", SessionIDs: []string{}}
	apply := func(items ...workspace.Workspace) workspace.Snapshot {
		t.Helper()
		snapshot := workspace.Snapshot{Workspaces: items}
		if err := s.Reconcile(ctx, &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	first := apply(w)
	if first.Workspaces[0].ID != w.ID {
		t.Fatal("initial import changed an existing ID")
	}
	w.ID, w.RepositoryID, w.Path, w.RepositoryPath = "ws_new_path", "repo_new_path", filepath.Join(base, "moved"), filepath.Join(base, "moved")
	moved := apply(w)
	if moved.Workspaces[0].ID != "ws_original" || moved.Workspaces[0].RepositoryID != "repo_original" {
		t.Fatalf("move changed identities: %#v", moved)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	restarted := apply(w)
	if restarted.Workspaces[0].ID != "ws_original" {
		t.Fatal("restart lost identity")
	}
	paths, err := s.Repositories(ctx)
	if err != nil || len(paths) != 1 || paths[0] != w.RepositoryPath {
		t.Fatalf("repository move not remembered: %v %v", paths, err)
	}
	// Reusing a path with a different Git administrative directory creates a new
	// identity, even if its deterministic first-stage candidate ID collides.
	w.ID, w.RepositoryID, w.IdentityKey, w.RepositoryKey = "ws_original", "repo_original", "new-git-dir", "new-repo-dir"
	replaced := apply(w)
	if len(replaced.Workspaces) != 2 {
		t.Fatalf("old workspace was forgotten: %#v", replaced)
	}
	for _, item := range replaced.Workspaces {
		if item.State == "available" && (item.ID == "ws_original" || item.RepositoryID == "repo_original") {
			t.Fatal("a different checkout inherited the previous identity")
		}
	}
	missing := apply()
	for _, item := range missing.Workspaces {
		if item.State != "missing" || len(item.SessionIDs) != 0 {
			t.Fatalf("missing workspace not observable: %#v", item)
		}
	}
	for _, file := range []string{filepath.Join(base, "next"), filepath.Join(base, "next", "state.db")} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("state exposed to other users: %s %v", file, err)
		}
	}
}

func TestCanceledReconcileDoesNotCommit(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = s.Reconcile(ctx, &workspace.Snapshot{Workspaces: []workspace.Workspace{{ID: "ws_1", RepositoryID: "repo_1", Path: "/missing", RepositoryPath: "/missing"}}})
	if err == nil {
		t.Fatal("canceled refresh succeeded")
	}
	paths, err := s.Repositories(context.Background())
	if err != nil || len(paths) != 0 {
		t.Fatalf("canceled refresh leaked metadata: %v %v", paths, err)
	}
}
