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
