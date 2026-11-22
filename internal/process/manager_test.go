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
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/process"
	"github.com/dawksh/supertree/internal/store"
	"github.com/dawksh/supertree/internal/tmux"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestRealNamedTerminalsLifecycleIsolationAndRecovery(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is unavailable")
	}
	ctx := context.Background()
	base := workspace.CanonicalPath(t.TempDir())
	repo := filepath.Join(base, "repo # 'quote;")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if data, err := exec.Command("git", "-C", repo, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git: %s %v", data, err)
	}
	sockets, err := os.MkdirTemp("/tmp", "st-process-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	client := tmux.Client{Socket: filepath.Join(sockets, "tmux")}
	t.Cleanup(func() { client.Run(ctx, "kill-server") })
	keeper, err := client.Run(ctx, "-f", "/dev/null", "new-session", "-d", "-s", "human", "-c", repo+"/", "-P", "-F", "#{pane_id}", "exec sleep 120")
	if err != nil {
		t.Fatal(err)
	}
	options := workspace.Options{StateDir: filepath.Join(base, "state"), WorktreeRoot: filepath.Join(base, "trees"), Directory: repo, TmuxSocket: client.Socket}
	c, err := core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Close() }()
	r, err := core.Registry(func(ctx context.Context, args core.InspectArgs) (workspace.Snapshot, error) {
		return c.Snapshot(args.WorkspaceID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterProcesses(r); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := c.Snapshot("")
	ws := snapshot.Workspaces[0].ID
	invoke := func(name string, args any) workspace.Process {
		t.Helper()
		raw, _ := json.Marshal(args)
		result, err := r.Invoke(ctx, name, raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return result.(workspace.Process)
	}
	start := process.StartArgs{WorkspaceID: ws, Name: "first agent", Kind: "agent", Command: []string{"/bin/sh", "-c", "printf ready; exec sleep 120"}}
	one := invoke("process.start", start)
	two := invoke("process.start", process.StartArgs{WorkspaceID: ws, Name: "tests", Kind: "tests", Command: []string{"sleep", "120"}})
	if one.PaneID == two.PaneID || one.ID == two.ID || one.State != "running" {
		t.Fatalf("not independent: %#v %#v", one, two)
	}
	active, activeErr := client.Run(ctx, "display-message", "-p", "-t", "=human:", "#{pane_id}")
	if active != keeper {
		t.Fatalf("background launch changed human focus: %q != %q (%v)", active, keeper, activeErr)
	}
	_, err = r.Invoke(ctx, "process.start", mustJSON(start))
	if err == nil {
		t.Fatal("duplicate process name accepted")
	}
	// An actor retry with the same request ID returns the existing process.
	retryArgs := process.StartArgs{WorkspaceID: ws, Name: "retry", Command: []string{"sleep", "120"}}
	retryCtx := command.WithOrigin(ctx, command.Origin{Actor: "agent:test", RequestID: "same-launch"})
	result, err := r.Invoke(retryCtx, "process.start", mustJSON(retryArgs))
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := r.Invoke(retryCtx, "process.start", mustJSON(retryArgs))
	if err != nil || result.(workspace.Process).ID != repeated.(workspace.Process).ID {
		t.Fatalf("retry launched twice: %v", err)
	}
	retryArgs.Command = []string{"sleep", "60"}
	if _, err := r.Invoke(retryCtx, "process.start", mustJSON(retryArgs)); err == nil {
		t.Fatal("request ID reused for different program")
	}
	// Real arguments and environment values survive shell/tmux metacharacters.
	marker := filepath.Join(base, "injected")
	literal := "quote' ; $(touch " + marker + ") # #{session_name}"
	short := invoke("process.start", process.StartArgs{WorkspaceID: ws, Name: "literal", Command: []string{"/bin/sh", "-c", `printf '%s\n' "$ST_LITERAL" "$1"; exit 7`, "sh", literal}, Environment: map[string]string{"ST_LITERAL": literal}})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Refresh(ctx, "", core.Origin{Actor: "test"}); err != nil {
			t.Fatal(err)
		}
		s, _ := c.Snapshot(ws)
		for _, p := range s.Processes {
			if p.ID == short.ID {
				short = p
			}
		}
		if short.State == "exited" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if short.State != "exited" || short.ExitCode != 7 {
		t.Fatalf("exit not retained: %#v", short)
	}
	output, err := r.Invoke(ctx, "process.output", mustJSON(process.OutputArgs{ProcessID: short.ID}))
	if err != nil || !strings.Contains(output.(process.Output).Text, literal) {
		t.Fatalf("literal output lost: %#v %v", output, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("argument or environment was executed as shell code")
	}
	encoded, _ := json.Marshal(short)
	if strings.Contains(string(encoded), `"ST_LITERAL":`) {
		t.Fatal("environment values exposed in inspection")
	}
	moved := invoke("process.move", process.MoveArgs{ProcessID: two.ID, TargetID: one.ID, Direction: "vertical"})
	if moved.ID != two.ID || moved.PaneID != two.PaneID || moved.WindowID != one.WindowID {
		t.Fatalf("move changed process identity: %#v", moved)
	}
	invoke("process.move", process.MoveArgs{ProcessID: two.ID})
	stopped := invoke("process.stop", process.Target{ProcessID: one.ID})
	if stopped.State != "stopped" {
		t.Fatal("stop state not persisted")
	}
	if _, err := client.Tagged(ctx, two.PaneID, two.ID); err != nil {
		t.Fatal("stopping one process closed another")
	}
	if _, err := r.Invoke(ctx, "process.stop", mustJSON(process.Target{ProcessID: two.ID, ExpectedRevision: 1})); err == nil {
		t.Fatal("stale revision accepted")
	}
	beforePID, _ := client.Run(ctx, "display-message", "-p", "-t", two.PaneID, "#{pane_pid}")
	c.Close()
	c, err = core.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	afterPID, _ := client.Run(ctx, "display-message", "-p", "-t", two.PaneID, "#{pane_pid}")
	if beforePID != afterPID {
		t.Fatal("core restart relaunched an existing process")
	}
	r, _ = core.Registry(func(ctx context.Context, args core.InspectArgs) (workspace.Snapshot, error) {
		return c.Snapshot(args.WorkspaceID)
	})
	if err := c.RegisterProcesses(r); err != nil {
		t.Fatal(err)
	}
	restarted := invoke("process.restart", process.Target{ProcessID: one.ID})
	if restarted.ID != one.ID || restarted.State != "running" {
		t.Fatal("explicit restart lost identity")
	}
	registered := invoke("process.register", process.RegisterArgs{WorkspaceID: ws, Name: "human", PaneID: keeper})
	if registered.Owned {
		t.Fatal("adopted pane reported an invented launch definition")
	}
	if _, err := r.Invoke(ctx, "process.restart", mustJSON(process.Target{ProcessID: registered.ID})); err == nil {
		t.Fatal("registered human process was relaunched")
	}
	client.Run(ctx, "kill-server")
	newPane, err := client.Run(ctx, "-f", "/dev/null", "new-session", "-d", "-s", "unrelated", "-c", repo+"/", "-P", "-F", "#{pane_id}", "exec sleep 120")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(ctx, "", core.Origin{Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Invoke(ctx, "process.stop", mustJSON(process.Target{ProcessID: one.ID})); err == nil {
		t.Fatal("stale process stopped a reused pane ID")
	}
	if _, err := client.Run(ctx, "display-message", "-p", "-t", newPane, "#{pane_pid}"); err != nil {
		t.Fatal("unrelated terminal was destroyed")
	}
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func TestLaunchSecretsExcludedFromTmux(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	base := t.TempDir()
	log := filepath.Join(base, "tmux-args")
	bin := filepath.Join(base, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// ShellCommand includes exec/env; use simple quoted paths for this test wrapper.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	wrapper := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + quote(log) + "\nexec " + quote(realTmux) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The socket must fit the Unix socket path limit.
	sockets, err := os.MkdirTemp("", "st-secret-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	client := tmux.Client{Socket: filepath.Join(sockets, "socket")}
	ctx := context.Background()
	defer client.Run(ctx, "kill-server")
	st, err := store.Open(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := process.New(st, client, func(string) (workspace.Workspace, error) {
		return workspace.Workspace{ID: "ws", Path: base, State: "available"}, nil
	})
	secret := "st-secret-do-not-expose-7391"
	p, err := m.Start(ctx, process.StartArgs{WorkspaceID: "ws", Name: "private", Command: []string{"/bin/sh", "-c", "sleep 120", "sh", secret}, Environment: map[string]string{"ST_SECRET": secret}})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := client.Run(ctx, "display-message", "-p", "-t", p.PaneID, "#{pane_start_command}")
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, secret) || strings.Contains(string(args), secret) {
		t.Fatal("secret exposed in tmux arguments or pane metadata")
	}
}
