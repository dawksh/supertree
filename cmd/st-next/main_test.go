package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/workspace"
)

func invokeCLI(t *testing.T, args ...string) (int, []byte, []byte) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return code, stdout.Bytes(), stderr.Bytes()
}

func TestCLIInspectAndRegistryInvocationAgree(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	t.Setenv("ST_STATE", filepath.Join(base, "state"))
	t.Setenv("ST_WORKTREE_ROOT", filepath.Join(base, "trees"))
	t.Setenv("ST_NEXT_SOCKET", "")
	socketDir, err := os.MkdirTemp("/tmp", "st-next-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	t.Setenv("ST_NEXT_TMUX_SOCKET", filepath.Join(socketDir, "sock"))
	if out, err := exec.Command("git", "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	code, out, stderr := invokeCLI(t, "inspect", "--json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("inspect: code=%d, stderr=%s", code, stderr)
	}
	var direct workspace.Snapshot
	if err := json.Unmarshal(out, &direct); err != nil || len(direct.Workspaces) != 1 {
		t.Fatalf("inspect result: %s, %v", out, err)
	}
	code, out, stderr = invokeCLI(t, "invoke", "workspace.inspect", `{"workspace_id":"`+direct.Workspaces[0].ID+`"}`)
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("invoke: code=%d, stderr=%s", code, stderr)
	}
	var throughRegistry workspace.Snapshot
	if err := json.Unmarshal(out, &throughRegistry); err != nil {
		t.Fatal(err)
	}
	direct.CapturedAt, throughRegistry.CapturedAt = "", ""
	if !reflect.DeepEqual(direct, throughRegistry) {
		t.Fatalf("CLI alias differs from registered invocation:\n%#v\n%#v", direct, throughRegistry)
	}
	code, out, stderr = invokeCLI(t, "commands", "--json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("commands: code=%d, stderr=%s", code, stderr)
	}
	var list commandList
	if err := json.Unmarshal(out, &list); err != nil || len(list.Commands) != 2 {
		t.Fatalf("command discovery: %s, %v", out, err)
	}
	for _, definition := range list.Commands {
		if definition.Version != 1 || len(definition.ArgumentSchema) == 0 || len(definition.ResultSchema) == 0 || definition.SensitiveArguments == nil {
			t.Fatalf("incomplete command schema: %#v", definition)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "state")); !os.IsNotExist(err) {
		t.Fatal("CLI wrote state during inspection")
	}
}

func TestCLIErrorsAreStructuredAndLeaveStdoutEmpty(t *testing.T) {
	for _, args := range [][]string{
		{"inspect", "--wat", "--json"},
		{"inspect", "extra", "--json"},
		{"commands", "--workspace", "x", "--json"},
		{"invoke", "workspace.inspect", `{"workspace_id":null}`},
		{"invoke", "workspace.inspect", `{`},
		{"invoke", "missing"},
		{"invoke"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, out, stderr := invokeCLI(t, args...)
			var failure struct {
				SchemaVersion int `json:"schema_version"`
				Error         struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if code == 0 || len(out) != 0 || json.Unmarshal(stderr, &failure) != nil || failure.SchemaVersion != 1 || failure.Error.Code == "" {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, out, stderr)
			}
		})
	}
}

func TestDetachedDaemonLifecycleAndCrashRecovery(t *testing.T) {
	base := t.TempDir()
	binary := filepath.Join(base, "st-next")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s %v", out, err)
	}
	t.Setenv("ST_STATE", filepath.Join(base, "state"))
	t.Setenv("ST_WORKTREE_ROOT", filepath.Join(base, "trees"))
	t.Setenv("ST_NEXT_SOCKET", "")
	t.Setenv("ST_NEXT_TMUX_SOCKET", filepath.Join("/tmp", "st-no-server-"+strconv.Itoa(os.Getpid())))
	t.Chdir(base)
	if out, err := exec.Command("git", "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	call := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
		return out
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		exec.CommandContext(ctx, binary, "daemon", "stop").Run()
	})
	var stopped struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(call("daemon", "status", "--json"), &stopped); err != nil || stopped.Running {
		t.Fatalf("initial status: %#v %v", stopped, err)
	}
	if _, err := os.Stat(filepath.Join(base, "state")); !os.IsNotExist(err) {
		t.Fatal("status created daemon state")
	}
	var started struct {
		PID     int    `json:"pid"`
		Epoch   string `json:"epoch"`
		Running bool   `json:"running"`
	}
	if err := json.Unmarshal(call("daemon", "start", "--json"), &started); err != nil || !started.Running || started.PID == 0 {
		t.Fatalf("start: %#v %v", started, err)
	}
	var duplicate struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(call("daemon", "start", "--json"), &duplicate); err != nil || duplicate.PID != started.PID {
		t.Fatalf("start was not idempotent: %#v %v", duplicate, err)
	}
	var before workspace.Snapshot
	if err := json.Unmarshal(call("inspect", "--json"), &before); err != nil || before.Epoch != started.Epoch || len(before.Workspaces) != 1 {
		t.Fatalf("CLI did not use daemon: %#v %v", before, err)
	}
	var generic workspace.Snapshot
	if err := json.Unmarshal(call("invoke", "workspace.inspect", `{}`), &generic); err != nil {
		t.Fatal(err)
	}
	before.CapturedAt, generic.CapturedAt = "", ""
	if !reflect.DeepEqual(before, generic) {
		t.Fatal("online CLI alias differs from generic socket invocation")
	}
	var list commandList
	if err := json.Unmarshal(call("commands", "--json"), &list); err != nil || len(list.Commands) != 5 {
		t.Fatalf("online command discovery: %#v %v", list, err)
	}
	var offline workspace.Snapshot
	if err := json.Unmarshal(call("--offline", "inspect", "--json"), &offline); err != nil || offline.Epoch != "" {
		t.Fatalf("explicit offline inspection used daemon: %#v %v", offline, err)
	}
	// SIGKILL only the child created by this test: prove recovery from stale
	// socket/lock files rather than testing only orderly shutdown.
	if err := syscall.Kill(started.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	var restarted struct {
		Epoch string `json:"epoch"`
	}
	if err := json.Unmarshal(call("daemon", "start", "--json"), &restarted); err != nil || restarted.Epoch == started.Epoch {
		t.Fatalf("crash restart: %#v %v", restarted, err)
	}
	var after workspace.Snapshot
	if err := json.Unmarshal(call("inspect", "--json"), &after); err != nil || len(after.Workspaces) != 1 || after.Workspaces[0].ID != before.Workspaces[0].ID {
		t.Fatalf("crash lost workspace identity: %#v %v", after, err)
	}
	// A changed config must not prevent inspecting or stopping the old daemon.
	t.Setenv("ST_WORKTREE_ROOT", filepath.Join(base, "changed-root"))
	call("daemon", "stop", "--json")
	if err := json.Unmarshal(call("daemon", "status", "--json"), &stopped); err != nil || stopped.Running {
		t.Fatalf("stop did not finish: %#v %v", stopped, err)
	}
	call("daemon", "stop") // Already stopped is successful and leaves state.
	if _, err := os.Stat(filepath.Join(base, "state", "next", "state.db")); err != nil {
		t.Fatal("stop removed durable state")
	}
}
