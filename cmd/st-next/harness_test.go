package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHarnessLaunchHelper(t *testing.T) {
	if os.Getenv("SUPERTREE_HARNESS_HELPER") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	dir, _ := os.Getwd()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"args": args, "directory": dir})
	os.Exit(0)
}

func TestRealHarnessMCPSharedWorkspaceRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	base := t.TempDir()
	sockets, err := os.MkdirTemp("/tmp", "st-mcp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	binary := filepath.Join(base, "st-next")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	repo := filepath.Join(base, "repo with #'quotes")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("ST_STATE", filepath.Join(base, "state"))
	t.Setenv("ST_WORKTREE_ROOT", filepath.Join(base, "trees"))
	t.Setenv("ST_NEXT_SOCKET", filepath.Join(sockets, "core.sock"))
	t.Setenv("ST_NEXT_TMUX_SOCKET", filepath.Join(sockets, "tmux.sock"))
	t.Setenv("ST_NEXT_ACTOR", "human:test")
	t.Setenv("ST_NEXT_REQUEST_ID", "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := func(program string, args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, program, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %s %v", program, args, out, err)
		}
		return out
	}
	cli := func(args ...string) []byte { return command(binary, args...) }
	tmux := func(args ...string) string {
		return strings.TrimSpace(string(command("tmux", append([]string{"-S", filepath.Join(sockets, "tmux.sock")}, args...)...)))
	}
	t.Cleanup(func() {
		exec.Command(binary, "daemon", "stop").Run()
		exec.Command("tmux", "-S", filepath.Join(sockets, "tmux.sock"), "kill-server").Run()
	})
	command("git", "init", "-q", "-b", "main")
	command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "initial")
	tmux("-f", "/dev/null", "new-session", "-d", "-s", "supertree/main", "-n", "codex", "-c", repo, "sleep 120")
	tmux("set-option", "-t", "=supertree/main:", "@supertree_label", "repo/main")
	for _, name := range []string{"nvim", "shell"} {
		tmux("new-window", "-d", "-t", "=supertree/main", "-n", name, "-c", repo, "sleep 120")
	}
	before := tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}")
	active := tmux("display-message", "-p", "-t", "=supertree/main:", "#{window_id}")
	cli("daemon", "start", "--json")
	cli("task", "add", "--title", "Human goal", "--notes", "Keep the actual harness interface")
	file := filepath.Join(repo, "example.go")
	os.WriteFile(file, []byte("original source\n"), 0600)
	var capture workspace.ContextItem
	if err := json.Unmarshal(cli("context", "add", "--file", file), &capture); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "real-harness-fixture", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(binary, "mcp", "--harness", "codex")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	available := map[string]bool{}
	for _, tool := range tools.Tools {
		available[tool.Name] = true
	}
	for _, name := range []string{"task_create", "context_export", "question_ask", "question_answer", "process_start", "view_select"} {
		if !available[name] {
			t.Fatalf("missing harness tool %s", name)
		}
	}
	for _, name := range []string{"agent_bind", "view_present", "view_close"} {
		if available[name] {
			t.Fatalf("human-client action exposed to harness: %s", name)
		}
	}
	tool := func(name string, args map[string]any, wantError bool) []byte {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("%s: unexpected error=%v: %#v", name, result.IsError, result.Content)
		}
		return []byte(result.Content[0].(*mcp.TextContent).Text)
	}
	if !strings.Contains(string(tool("task_list", map[string]any{}, false)), "Human goal") {
		t.Fatal("harness cannot see human goal")
	}
	var task workspace.Task
	json.Unmarshal(tool("task_create", map[string]any{"title": "Agent subtask"}, false), &task)
	if task.Actor != "agent:codex" || task.WorkspaceID == "" {
		t.Fatalf("harness lost actor attribution or worktree default: %#v", task)
	}
	if !strings.Contains(string(cli("task", "list")), task.ID) {
		t.Fatal("human cannot see harness update")
	}
	tool("task_update", map[string]any{"task_id": task.ID, "status": "done", "expected_revision": task.Revision + 1}, true)
	tool("task_update", map[string]any{"task_id": task.ID, "status": "done", "expected_revision": task.Revision}, false)
	if !strings.Contains(string(tool("context_export", map[string]any{"context_ids": []string{capture.ID}}, false)), "original source") {
		t.Fatal("harness cannot read reviewed human context")
	}
	os.WriteFile(file, []byte("human changed source\n"), 0600)
	tool("context_export", map[string]any{"context_ids": []string{capture.ID}}, true)
	var question workspace.Question
	json.Unmarshal(tool("question_ask", map[string]any{"prompt": "Which behavior should we preserve?"}, false), &question)
	cli("question", "answer", question.ID, "--revision", fmt.Sprint(question.Revision), "--answer", "Keep the current shortcuts")
	if !strings.Contains(string(tool("question_list", map[string]any{}, false)), "Keep the current shortcuts") {
		t.Fatal("harness cannot read human answer")
	}
	var process workspace.Process
	json.Unmarshal(tool("process_start", map[string]any{"name": "test watcher", "kind": "tests", "command": []string{"sleep", "120"}}, false), &process)
	if process.SessionID != tmux("display-message", "-p", "-t", "=supertree/main:", "#{session_id}") {
		t.Fatal("harness process escaped the normal worktree session")
	}
	tool("process_output", map[string]any{"process_id": process.ID}, false)
	tool("process_stop", map[string]any{"process_id": process.ID}, false)
	var view workspace.View
	json.Unmarshal(cli("invoke", "view.open", `{"workspace_id":"`+task.WorkspaceID+`"}`), &view)
	tool("view_select", map[string]any{"view_id": view.ID, "expected_revision": view.Revision}, false)
	if before != tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}") || active != tmux("display-message", "-p", "-t", "=supertree/main:", "#{window_id}") {
		t.Fatal("harness tools replaced a program or changed terminal focus")
	}
	// Every tool reconnects to the core; restarting it keeps the real harness
	// connection and terminal programs in place.
	cli("daemon", "stop")
	cli("daemon", "start", "--json")
	tool("task_list", map[string]any{}, false)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if before != tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}") {
		t.Fatal("disconnecting MCP stopped a workspace program")
	}

	// Verify both real harness launch shapes without starting a provider,
	// changing global config, using an account, or spending model tokens.
	realCodex, _ := exec.LookPath("codex")
	testExe, _ := os.Executable()
	toolsDir := filepath.Join(base, "tools")
	os.Mkdir(toolsDir, 0700)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	for _, provider := range []string{"codex", "claude"} {
		script := "#!/bin/sh\nexport SUPERTREE_HARNESS_HELPER=1\nexec " + quote(testExe) + " -test.run '^TestHarnessLaunchHelper$' -- \"$@\"\n"
		os.WriteFile(filepath.Join(toolsDir, provider), []byte(script), 0700)
	}
	t.Setenv("PATH", toolsDir+":"+os.Getenv("PATH"))
	for _, provider := range []string{"codex", "claude"} {
		var launch struct {
			Args      []string `json:"args"`
			Directory string   `json:"directory"`
		}
		if err := json.Unmarshal(cli("harness", provider, "--", "--continue"), &launch); err != nil {
			t.Fatal(err)
		}
		if launch.Directory != repo || launch.Args[len(launch.Args)-1] != "--continue" {
			t.Fatalf("launch changed cwd or resume args: %#v", launch)
		}
		if provider == "codex" {
			if launch.Args[0] != "-c" || !strings.Contains(launch.Args[1], "mcp_servers.supertree=") || !strings.Contains(launch.Args[1], "core.sock") {
				t.Fatalf("missing launch-scoped Codex override: %#v", launch)
			}
			if realCodex != "" {
				// Parse the real override without a model session or user config.
				os.MkdirAll(filepath.Join(base, "codex-home"), 0700)
				cmd := exec.CommandContext(ctx, realCodex, "-c", launch.Args[1], "mcp", "get", "supertree", "--json")
				cmd.Env = append(os.Environ(), "CODEX_HOME="+filepath.Join(base, "codex-home"))
				if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), binary) {
					t.Fatalf("real Codex rejected per-launch MCP config: %s %v", out, err)
				}
			}
		} else {
			var config map[string]map[string]map[string]any
			if launch.Args[0] != "--mcp-config" || json.Unmarshal([]byte(launch.Args[1]), &config) != nil || config["mcpServers"]["supertree"]["command"] != binary {
				t.Fatalf("missing launch-scoped Claude override: %#v", launch)
			}
		}
	}
}
