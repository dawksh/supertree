package process_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

func recoveryWorkspace(t *testing.T) (workspace.Workspace, tmux.Client) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	base := workspace.CanonicalPath(t.TempDir())
	sockets, err := os.MkdirTemp("/tmp", "st-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	client := tmux.Client{Socket: filepath.Join(sockets, "tmux.sock")}
	t.Cleanup(func() {
		client.Run(context.Background(), "kill-server")
		os.RemoveAll(sockets)
	})
	return workspace.Workspace{ID: "ws", Path: base, State: "available"}, client
}

func recoveryManager(t *testing.T, w workspace.Workspace, client tmux.Client) (*process.Manager, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(w.Path, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return process.New(s, client, func(string) (workspace.Workspace, error) { return w, nil }), s
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// The wrapper delegates to the isolated runner's tmux wrapper, never a normal
// socket. Its injected fault is one-shot so subsequent inspection stays real.
func recoveryTmuxWrapper(t *testing.T, script string) {
	t.Helper()
	guard, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	text := "#!/bin/sh\ntask_tmux=" + shellQuote(guard) + "\n" + script + "\nexec \"$task_tmux\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRestartDoesNotLaunchOnUncertainInspection(t *testing.T) {
	for _, failure := range []string{"query failure", "malformed listing"} {
		t.Run(failure, func(t *testing.T) {
			w, client := recoveryWorkspace(t)
			flag := filepath.Join(w.Path, "fail-next-inspection")
			fault := "printf '%s\\n' 'unexpected pane data'; exit 0"
			if failure == "query failure" {
				fault = "printf '%s\\n' 'temporary inspection failure' >&2; exit 1"
			}
			recoveryTmuxWrapper(t, "for task_arg in \"$@\"; do\n"+
				"  if [ \"$task_arg\" = list-panes ] && [ -f "+shellQuote(flag)+" ]; then\n"+
				"    rm -f "+shellQuote(flag)+"\n    "+fault+"\n  fi\ndone")
			m, _ := recoveryManager(t, w, client)
			ctx := context.Background()
			p, err := m.Start(ctx, process.StartArgs{WorkspaceID: w.ID, Name: "original", Command: []string{"sleep", "120"}})
			if err != nil {
				t.Fatal(err)
			}
			before, err := client.Run(ctx, "list-panes", "-a", "-F", "#{pane_id} #{pane_pid} #{@st_next_process}")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(flag, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Restart(ctx, process.Target{ProcessID: p.ID}); err == nil {
				t.Error("restart launched despite an uncertain inspection")
			}
			after, err := client.Run(ctx, "list-panes", "-a", "-F", "#{pane_id} #{pane_pid} #{@st_next_process}")
			if err != nil || before != after {
				t.Fatalf("uncertain restart changed running programs:\nbefore: %s\nafter: %s\n%v", before, after, err)
			}
			// A confirmed missing terminal still permits an explicit restart.
			if _, err := client.Run(ctx, "kill-pane", "-t", p.PaneID); err != nil {
				t.Fatal(err)
			}
			restarted, err := m.Restart(ctx, process.Target{ProcessID: p.ID})
			if err != nil || restarted.ID != p.ID || restarted.State != "running" {
				t.Fatalf("explicit restart of missing terminal failed: %#v %v", restarted, err)
			}
		})
	}
}

type interruptedLaunch struct {
	Workspace workspace.Workspace
	Socket    string
	Args      process.StartArgs
}

func TestInterruptedLaunchChild(t *testing.T) {
	file := os.Getenv("ST_TEST_INTERRUPTED_LAUNCH")
	if file == "" {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var fixture interruptedLaunch
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	m, _ := recoveryManager(t, fixture.Workspace, tmux.Client{Socket: fixture.Socket})
	ctx := command.WithOrigin(context.Background(), command.Origin{Actor: "test:interrupted", RequestID: "one-launch"})
	_, err = m.Start(ctx, fixture.Args)
	t.Fatalf("fault did not terminate launch helper: %v", err)
}

func TestInterruptedLaunchKeepsTerminalWithoutInventingSuccess(t *testing.T) {
	for _, point := range []struct {
		name    string
		trigger string
		started bool
	}{
		{"after tagging placeholder", "@st_next_process", false},
		{"after spawning program", "rename-window", true},
	} {
		t.Run(point.name, func(t *testing.T) {
			w, client := recoveryWorkspace(t)
			flag := filepath.Join(w.Path, "interrupt-next-launch")
			if err := os.WriteFile(flag, nil, 0600); err != nil {
				t.Fatal(err)
			}
			recoveryTmuxWrapper(t, "for task_arg in \"$@\"; do\n"+
				"  if [ \"$task_arg\" = "+shellQuote(point.trigger)+" ] && [ -f "+shellQuote(flag)+" ]; then\n"+
				"    \"$task_tmux\" \"$@\" || exit $?\n"+
				"    rm -f "+shellQuote(flag)+"\n"+
				"    kill -KILL \"$PPID\"\n    exit 0\n  fi\ndone")
			marker := filepath.Join(w.Path, "started")
			args := process.StartArgs{WorkspaceID: w.ID, Name: "interrupted", Command: []string{"/bin/sh", "-c", "printf 'started\\n' >> \"$1\"; exec sleep 120", "sh", marker}}
			fixture := interruptedLaunch{Workspace: w, Socket: client.Socket, Args: args}
			fixturePath := filepath.Join(w.Path, "fixture.json")
			if err := os.WriteFile(fixturePath, mustJSON(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(executable, "-test.run=^TestInterruptedLaunchChild$")
			child.Env = append(os.Environ(), "ST_TEST_INTERRUPTED_LAUNCH="+fixturePath)
			if out, err := child.CombinedOutput(); err == nil || !strings.Contains(err.Error(), "killed") {
				t.Fatalf("helper was not interrupted: %s %v", out, err)
			}
			ctx := context.Background()
			before, err := client.Run(ctx, "list-panes", "-a", "-F", "#{pane_id} #{pane_pid}")
			if err != nil {
				t.Fatal(err)
			}
			if point.started {
				waitForLaunchCount(t, marker, 1)
			}
			m, s := recoveryManager(t, w, client)
			check := func() workspace.Process {
				t.Helper()
				items, err := m.Snapshot(ctx, w)
				if err != nil || len(items) != 1 {
					t.Fatalf("recovery snapshot: %#v %v", items, err)
				}
				p := items[0]
				if p.State != "starting" || p.Error == "" {
					t.Errorf("interrupted launch falsely reported as healthy: %#v", p)
				}
				return p
			}
			p := check()
			if !point.started {
				// Older state has only "starting", without a launch_pending field.
				// Reconciliation must retain the same conservative interpretation.
				docs, err := s.Documents(ctx, "process")
				if err != nil || len(docs) != 1 {
					t.Fatalf("launch documents: %v %v", docs, err)
				}
				var data map[string]json.RawMessage
				if err := json.Unmarshal(docs[0].Data, &data); err != nil {
					t.Fatal(err)
				}
				delete(data, "launch_pending")
				if _, err := s.PutDocument(ctx, "process", docs[0].ID, data, docs[0].Revision); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			m, _ = recoveryManager(t, w, client)
			check()
			retryCtx := command.WithOrigin(ctx, command.Origin{Actor: "test:interrupted", RequestID: "one-launch"})
			repeated, err := m.Start(retryCtx, args)
			if err != nil || repeated.ID != p.ID || repeated.State != "starting" {
				t.Fatalf("retry lost the interrupted launch: %#v %v", repeated, err)
			}
			after, err := client.Run(ctx, "list-panes", "-a", "-F", "#{pane_id} #{pane_pid}")
			if err != nil || before != after {
				t.Fatalf("recovery replaced a terminal: %s -> %s (%v)", before, after, err)
			}
			want := 0
			if point.started {
				want = 1
			}
			waitForLaunchCount(t, marker, want)
			if _, err := m.Restart(ctx, process.Target{ProcessID: p.ID}); err == nil {
				t.Fatal("restart replaced a live interrupted terminal without explicit stop")
			}
			if _, err := m.Stop(ctx, process.Target{ProcessID: p.ID}); err != nil {
				t.Fatal(err)
			}
			if restarted, err := m.Restart(ctx, process.Target{ProcessID: p.ID}); err != nil || restarted.State != "running" {
				t.Fatalf("explicit stop/restart failed: %#v %v", restarted, err)
			}
			waitForLaunchCount(t, marker, want+1)
		})
	}
}

func waitForLaunchCount(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		got := strings.Count(string(data), "started\n")
		if got == want {
			return
		}
		if time.Now().After(deadline) || got > want {
			t.Fatalf("program launch count: got %d, want %d", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
