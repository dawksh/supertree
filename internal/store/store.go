// Package store owns the daemon's durable identities. It never writes Git or
// legacy Supertree metadata. A refresh is committed as one SQLite transaction.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"github.com/dawksh/supertree/internal/workspace"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "next")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// The daemon's lock is held before Open. Restrict our own directory, never
	// change permissions on the legacy state directory.
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("daemon state must be a real directory: %s", dir)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "state.db")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	file.Close()
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err == nil && version > 1 {
		err = fmt.Errorf("unsupported workspace database version: %d", version)
	}
	if err == nil {
		_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS identities (
  kind TEXT NOT NULL, id TEXT NOT NULL, anchor TEXT NOT NULL, path TEXT NOT NULL,
  PRIMARY KEY(kind, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS identity_anchor ON identities(kind, anchor) WHERE anchor != '';
CREATE INDEX IF NOT EXISTS identity_path ON identities(kind, path);
CREATE TABLE IF NOT EXISTS workspaces (id TEXT PRIMARY KEY, data TEXT NOT NULL);
PRAGMA user_version = 1;`)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Repositories(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT path FROM identities WHERE kind = 'repo' ORDER BY path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func identity(ctx context.Context, tx *sql.Tx, kind, candidate, anchor, path string) (string, error) {
	var id string
	var err error
	if anchor != "" {
		err = tx.QueryRowContext(ctx, "SELECT id FROM identities WHERE kind=? AND anchor=?", kind, anchor).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, "SELECT id FROM identities WHERE kind=? AND path=? AND anchor='' ORDER BY rowid DESC LIMIT 1", kind, path).Scan(&id)
		}
	} else {
		err = tx.QueryRowContext(ctx, "SELECT id FROM identities WHERE kind=? AND path=? ORDER BY rowid DESC LIMIT 1", kind, path).Scan(&id)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if errors.Is(err, sql.ErrNoRows) {
		id = candidate // Preserve the IDs already exposed by tranche 1.
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM identities WHERE kind=? AND id=?", kind, id).Scan(&count); err != nil {
			return "", err
		}
		if count != 0 {
			id = fmt.Sprintf("%s_%x", kind, randomID())
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO identities(kind,id,anchor,path) VALUES(?,?,?,?)", kind, id, anchor, path)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE identities SET path=?,anchor=CASE WHEN ?='' THEN anchor ELSE ? END WHERE kind=? AND id=?", path, anchor, anchor, kind, id)
	}
	return id, err
}

func randomID() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand on supported Go versions cannot fail.
	}
	return b
}

// Reconcile replaces path-derived IDs with durable mappings and retains known
// workspaces that disappear from discovery. Missing is observable, not deletion.
func (s *Store) Reconcile(ctx context.Context, snapshot *workspace.Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := make(map[string]bool)
	for i := range snapshot.Workspaces {
		w := &snapshot.Workspaces[i]
		w.RepositoryID, err = identity(ctx, tx, "repo", w.RepositoryID, w.RepositoryKey, w.RepositoryPath)
		if err != nil {
			return err
		}
		w.ID, err = identity(ctx, tx, "ws", w.ID, w.IdentityKey, w.Path)
		if err != nil {
			return err
		}
		seen[w.ID] = true
		data, err := json.Marshal(w)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO workspaces(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", w.ID, string(data)); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,data FROM workspaces ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			rows.Close()
			return err
		}
		if seen[id] {
			continue
		}
		var w workspace.Workspace
		if err := json.Unmarshal([]byte(data), &w); err != nil {
			rows.Close()
			return err
		}
		w.State, w.SessionIDs = "unavailable", []string{}
		if _, err := os.Stat(w.Path); errors.Is(err, os.ErrNotExist) {
			w.State = "missing"
		}
		snapshot.Workspaces = append(snapshot.Workspaces, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	sort.Slice(snapshot.Workspaces, func(i, j int) bool { return snapshot.Workspaces[i].Path < snapshot.Workspaces[j].Path })
	return tx.Commit()
}
