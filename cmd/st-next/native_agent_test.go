package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/workspace"
)

// This subprocess implements the checked native protocol without accounts,
// network access, model spending, or touching a developer's Codex conversations.
func TestNativeProviderHelper(t *testing.T) {
	if os.Getenv("SUPERTREE_PROVIDER_HELPER") != "1" {
		return
	}
	dir := os.Getenv("SUPERTREE_PROVIDER_FIXTURE")
	encoder := json.NewEncoder(os.Stdout)
	thread := "thread-fixture-exact"
	turn := ""
	pendingTurn := ""
	notify := func(method string, params any) {
		_ = encoder.Encode(map[string]any{"method": method, "params": params})
	}
	complete := func(status, text string) {
		if text != "" {
			notify("item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": turn, "itemId": "message-" + turn, "delta": text})
			notify("item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"id": "message-" + turn, "type": "agentMessage", "text": text}})
		}
		notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": status}})
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			os.Exit(3)
		}
		if msg.Method == "" {
			if string(msg.ID) == "900" {
				var answer struct {
					Decision string `json:"decision"`
				}
				json.Unmarshal(msg.Result, &answer)
				if answer.Decision != "accept" && answer.Decision != "decline" {
					os.Exit(4)
				}
				turn = pendingTurn
				complete("completed", "Human decision received: "+answer.Decision)
			}
			continue
		}
		log, _ := os.OpenFile(filepath.Join(dir, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		fmt.Fprintln(log, msg.Method)
		log.Close()
		reply := func(result any) { _ = encoder.Encode(map[string]any{"id": msg.ID, "result": result}) }
		switch msg.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "native-protocol-fixture"})
		case "initialized":
		case "thread/start", "thread/resume":
			var args map[string]any
			json.Unmarshal(msg.Params, &args)
			if msg.Method == "thread/resume" && args["threadId"] != thread {
				os.Exit(5)
			}
			reply(map[string]any{"thread": map[string]any{"id": thread}, "model": "fixture-model"})
		case "turn/start":
			var args struct {
				ThreadID string `json:"threadId"`
				Input    []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			json.Unmarshal(msg.Params, &args)
			if args.ThreadID != thread || len(args.Input) != 1 {
				os.Exit(6)
			}
			if strings.Contains(args.Input[0].Text, "interface-probe") {
				data, err := exec.Command(os.Getenv("SUPERTREE_PROVIDER_CLI"), "inspect", "--json").Output()
				if err != nil {
					os.Exit(7)
				}
				os.WriteFile(filepath.Join(dir, "interface"), data, 0600)
			}
			data, _ := os.ReadFile(filepath.Join(dir, "counter"))
			count, _ := strconv.Atoi(string(data))
			count++
			os.WriteFile(filepath.Join(dir, "counter"), []byte(strconv.Itoa(count)), 0600)
			turn = fmt.Sprintf("turn-%d", count)
			os.WriteFile(filepath.Join(dir, "delivery"), []byte(args.Input[0].Text), 0600)
			reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
			notify("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn}})
			if strings.Contains(args.Input[0].Text, "need-approval") {
				pendingTurn = turn
				encoder.Encode(map[string]any{"id": 900, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": thread, "turnId": turn, "command": "echo fixture", "reason": "Human decision requested"}})
			} else if !strings.Contains(args.Input[0].Text, "wait-for-interrupt") {
				complete("completed", "I received the selected context and will keep the human involved.")
			}
		case "turn/interrupt":
			reply(map[string]any{})
			complete("interrupted", "")
		default:
			encoder.Encode(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32601, "message": "unsupported fixture method"}})
		}
	}
	os.Exit(0)
}

func TestNativePairingDeliveryQuestionsInterruptAndRestart(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	base := t.TempDir()
	sockets, err := os.MkdirTemp("/tmp", "st-native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	binary := filepath.Join(base, "st-next")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	testExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(base, "tools")
	os.MkdirAll(tools, 0700)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	stub := "#!/bin/sh\nexport SUPERTREE_PROVIDER_HELPER=1\nexport SUPERTREE_PROVIDER_FIXTURE=" + quote(base) + "\nexport SUPERTREE_PROVIDER_CLI=" + quote(binary) + "\nexec " + quote(testExe) + " -test.run '^TestNativeProviderHelper$' -- \"$@\"\n"
	os.WriteFile(filepath.Join(tools, "codex"), []byte(stub), 0700)
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	t.Setenv("ST_STATE", filepath.Join(base, "state"))
	t.Setenv("ST_WORKTREE_ROOT", filepath.Join(base, "trees"))
	t.Setenv("ST_NEXT_SOCKET", filepath.Join(sockets, "core.sock"))
	t.Setenv("ST_NEXT_TMUX_SOCKET", filepath.Join(sockets, "tmux.sock"))
	t.Setenv("ST_NEXT_ACTOR", "human:test")
	t.Setenv("ST_NEXT_REQUEST_ID", "")
	repo := filepath.Join(base, "repo")
	os.Mkdir(repo, 0700)
	t.Chdir(repo)
	if out, err := exec.Command("git", "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git: %s %v", out, err)
	}
	os.WriteFile(filepath.Join(repo, "failing.go"), []byte("failing test context\n"), 0600)
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
		exec.Command(binary, "daemon", "stop").Run()
		exec.Command("tmux", "-S", filepath.Join(sockets, "tmux.sock"), "kill-server").Run()
	})
	call("daemon", "start", "--json")
	var task workspace.Task
	json.Unmarshal(call("task", "add", "--title", "Investigate the failing test"), &task)
	var capture workspace.ContextItem
	json.Unmarshal(call("context", "add", "--file", "failing.go", "--task", task.ID), &capture)
	var a workspace.Agent
	json.Unmarshal(call("agent", "start", "--name", "pair", "--task", task.ID), &a)
	waitAgent := func(state string) workspace.Agent {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			var result struct {
				Agents []workspace.Agent `json:"agents"`
			}
			json.Unmarshal(call("agent", "list"), &result)
			for _, item := range result.Agents {
				if item.ID == a.ID {
					if item.State == state {
						return item
					}
					if item.State == "failed" {
						t.Fatalf("native worker failed: %s", item.Error)
					}
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("agent did not reach %s", state)
		return workspace.Agent{}
	}
	a = waitAgent("idle")
	threadID := a.ThreadID
	if threadID != "thread-fixture-exact" {
		t.Fatal("provider identity was inferred instead of native")
	}
	var delivery workspace.Delivery
	json.Unmarshal(call("agent", "send", a.ID, "--message", "interface-probe Look at the failing test", "--context", capture.ID), &delivery)
	if delivery.Status != "sent" || delivery.TurnID == "" {
		t.Fatalf("delivery not acknowledged: %#v", delivery)
	}
	a = waitAgent("idle")
	interfaceData, err := os.ReadFile(filepath.Join(base, "interface"))
	if err != nil {
		t.Fatal(err)
	}
	var shared workspace.Snapshot
	if json.Unmarshal(interfaceData, &shared) != nil || len(shared.Workspaces) != 1 || shared.Workspaces[0].ID != a.WorkspaceID {
		t.Fatal("agent subprocess did not reach its own shared workspace interface")
	}
	actual, _ := os.ReadFile(filepath.Join(base, "delivery"))
	if !strings.Contains(string(actual), "failing test context") || !strings.Contains(string(actual), task.Title) {
		t.Fatal("explicit context/goal did not reach the provider")
	}
	var receipt workspace.Delivery
	params := fmt.Sprintf(`{"delivery_id":%q}`, delivery.ID)
	json.Unmarshal(call("invoke", "delivery.inspect", params), &receipt)
	if receipt.Text != string(actual) {
		t.Fatal("delivery audit differs from exact provider input")
	}
	os.WriteFile(filepath.Join(repo, "failing.go"), []byte("human edited this meanwhile\n"), 0600)
	if out, err := exec.Command(binary, "agent", "send", a.ID, "--message", "stale", "--context", capture.ID).CombinedOutput(); err == nil || !strings.Contains(string(out), "stale") {
		t.Fatal("stale context reached the provider")
	}
	call("agent", "send", a.ID, "--message", "need-approval")
	a = waitAgent("waiting")
	if len(a.Questions) != 1 {
		t.Fatal("native approval did not become shared input")
	}
	q := a.Questions[0]
	call("question", "answer", q.ID, "--revision", fmt.Sprint(q.Revision), "--answer", "decline")
	a = waitAgent("idle")
	call("agent", "send", a.ID, "--message", "wait-for-interrupt")
	a = waitAgent("running")
	call("agent", "interrupt", a.ID)
	a = waitAgent("interrupted")
	call("daemon", "stop")
	call("daemon", "start", "--json")
	a = waitAgent("interrupted")
	if a.ThreadID != threadID {
		t.Fatal("core restart changed native thread")
	}
	log, _ := os.ReadFile(filepath.Join(base, "calls"))
	if strings.Count(string(log), "thread/start") != 1 || strings.Count(string(log), "thread/resume") != 0 || strings.Count(string(log), "initialize") != 2 {
		t.Fatalf("core restart unexpectedly relaunched the provider: %s", log)
	}
	call("process", "stop", a.ProcessID)
	call("process", "restart", a.ProcessID)
	a = waitAgent("idle")
	if a.ThreadID != threadID {
		t.Fatal("worker restart attached to another conversation")
	}
	log, _ = os.ReadFile(filepath.Join(base, "calls"))
	if strings.Count(string(log), "thread/resume") != 1 {
		t.Fatalf("worker did not resume exact thread: %s", log)
	}
	var tasks struct {
		Tasks []workspace.Task `json:"tasks"`
	}
	json.Unmarshal(call("task", "list"), &tasks)
	if len(tasks.Tasks) != 1 || tasks.Tasks[0].Status != "open" {
		t.Fatal("agent turn completion incorrectly marked the shared goal successful")
	}
}
