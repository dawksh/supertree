package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/workspace"
)

func TestCLILayoutPaneDiagnostics(t *testing.T) {
	if os.Getenv("SUPERTREE_TEST_SANDBOX") == "" {
		t.Fatal("run through scripts/test-local.sh")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	base := t.TempDir()
	binary := filepath.Join(base, "st-next")
	build := exec.Command("go", "build", "-p=1", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	t.Chdir(base)
	t.Setenv("ST_STATE", filepath.Join(base, "state"))
	t.Setenv("ST_WORKTREE_ROOT", filepath.Join(base, "trees"))
	t.Setenv("ST_NEXT_SOCKET", "")
	socket := filepath.Join(base, "tmux.sock")
	t.Setenv("ST_NEXT_TMUX_SOCKET", socket)
	if out, err := exec.Command("git", "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	cli := func(args ...string) (string, string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		err := cmd.Run()
		return out.String(), stderr.String(), err
	}
	ok := func(args ...string) string {
		t.Helper()
		out, stderr, err := cli(args...)
		if err != nil || stderr != "" {
			t.Fatalf("%v: stdout=%s stderr=%s err=%v", args, out, stderr, err)
		}
		return out
	}
	tmux := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { cli("daemon", "stop"); exec.Command("tmux", "-S", socket, "kill-server").Run() })
	ok("daemon", "start", "--json")
	var snap workspace.Snapshot
	if err := json.Unmarshal([]byte(ok("inspect", "--json")), &snap); err != nil || len(snap.Workspaces) != 1 {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	ws := snap.Workspaces[0].ID
	invoke := func(name string, args any) string {
		t.Helper()
		data, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return ok("invoke", name, string(data))
	}
	var p workspace.Process
	if err := json.Unmarshal([]byte(invoke("process.start", map[string]any{"workspace_id": ws, "name": "anchor", "command": []string{"sleep", "120"}})), &p); err != nil || p.ID == "" || p.PaneID == "" {
		t.Fatalf("process: %+v %v", p, err)
	}
	capture := map[string]any{"workspace_id": ws, "process_id": p.ID, "expected_revision": 0}
	invoke("layout.capture", capture)
	saved := invoke("layout.inspect", map[string]any{"workspace_id": ws})
	capture["expected_revision"] = 1
	foreign := tmux("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", p.PaneID, "exec sleep 120")
	state := func() (string, string) {
		t.Helper()
		return tmux("list-panes", "-t", p.PaneID, "-F", "#{pane_id} #{pane_pid} #{pane_current_command} #{@st_next_process} #{window_layout}"), invoke("layout.inspect", map[string]any{"workspace_id": ws})
	}
	fail := func(name string, args any, message string) {
		t.Helper()
		before, layoutBefore := state()
		data, _ := json.Marshal(args)
		out, stderr, err := cli("invoke", name, string(data))
		var failure struct {
			SchemaVersion int `json:"schema_version"`
			Error         struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err == nil || out != "" || json.Unmarshal([]byte(stderr), &failure) != nil || failure.SchemaVersion != 1 || failure.Error.Code != "conflict" || !strings.Contains(failure.Error.Message, message) || !strings.Contains(failure.Error.Message, "inspect tmux") || strings.Contains(failure.Error.Message, "register it first") {
			t.Fatalf("%s: stdout=%q stderr=%q err=%v", name, out, stderr, err)
		}
		// Exercise the documented read-only inspection remedy against the same window.
		listing := tmux("list-panes", "-t", p.PaneID, "-F", "#{pane_id} #{pane_current_command} #{@st_next_process}")
		if !strings.Contains(listing, p.PaneID) || !strings.Contains(listing, foreign) {
			t.Fatalf("inspection missing panes: %q", listing)
		}
		all := tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_current_command} #{@st_next_process}")
		if !strings.Contains(all, p.PaneID) || !strings.Contains(all, foreign) {
			t.Fatalf("server inspection missing panes: %q", all)
		}
		ok("process", "list", "--workspace", ws)
		after, layoutAfter := state()
		if before != after || !reflect.DeepEqual(json.RawMessage(layoutBefore), json.RawMessage(layoutAfter)) || saved != layoutAfter {
			t.Fatalf("%s or inspection mutated panes/layout: before=%q after=%q saved=%s after-layout=%s", name, before, after, saved, layoutAfter)
		}
	}
	fail("layout.capture", capture, "foreign or ambiguous pane")
	live := tmux("display-message", "-p", "-t", p.PaneID, "#{window_layout}")
	fail("layout.restore", map[string]any{"workspace_id": ws, "expected_revision": 1, "expected_layout": live}, "other panes")
	tmux("set-option", "-p", "-t", foreign, "@st_next_process", p.ID)
	fail("layout.capture", capture, "foreign or ambiguous pane")
}
