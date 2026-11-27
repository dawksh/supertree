package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerProviderHelper(t *testing.T) {
	if os.Getenv("SUPERTREE_WORKER_PROVIDER") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var pending json.RawMessage
	turn := "turn-worker"
	thread := "thread-worker"
	for scanner.Scan() {
		var msg providerMessage
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			os.Exit(2)
		}
		reply := func(id json.RawMessage, result any) { encoder.Encode(map[string]any{"id": id, "result": result}) }
		complete := func() {
			encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed"}}})
		}
		switch msg.Method {
		case "initialize":
			reply(msg.ID, map[string]any{})
		case "initialized":
		case "thread/start", "thread/resume":
			reply(msg.ID, map[string]any{"thread": map[string]any{"id": thread}, "model": "fixture"})
		case "turn/start":
			pending = msg.ID
			encoder.Encode(map[string]any{"id": 900, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": thread, "turnId": turn, "command": "fixture"}})
		case "":
			if string(msg.ID) == "900" {
				reply(pending, map[string]any{"turn": map[string]any{"id": turn}})
				complete()
			}
		default:
			fmt.Fprintln(os.Stderr, "unknown fixture method", msg.Method)
			os.Exit(3)
		}
	}
	os.Exit(0)
}
func TestWorkerAnswersApprovalWhileTurnStartIsPendingAndCheckpointsIdentity(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "st-worker-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	providerExe := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nexport SUPERTREE_WORKER_PROVIDER=1\nexec " + quote(exe) + " -test.run '^TestWorkerProviderHelper$'\n"
	if err := os.WriteFile(providerExe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "checkpoint")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	options := WorkerOptions{ID: "agent_worker", WorkspaceID: "ws_worker", Directory: dir, Socket: filepath.Join(dir, "worker.sock"), StateFile: filepath.Join(stateDir, "state.json"), Executable: providerExe}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunWorker(ctx, options, nil, io.Discard, io.Discard) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker shutdown hung")
		}
	}()
	call := func(req workerRequest) (workerResponse, error) {
		callCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		return workerCall(callCtx, options.Socket, req)
	}
	wait := func(state string) workerResponse {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			reply, err := call(workerRequest{Method: "inspect"})
			if err == nil && reply.Agent.State == state {
				return reply
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("worker did not reach %s", state)
		return workerResponse{}
	}
	wait("idle")
	sent := make(chan error, 1)
	go func() {
		_, err := call(workerRequest{Method: "send", DeliveryID: "delivery_once", Text: "needs approval", Actor: "human"})
		sent <- err
	}()
	waiting := wait("waiting")
	if len(waiting.Agent.Questions) != 1 {
		t.Fatal("pending approval not exposed")
	}
	if _, err := call(workerRequest{Method: "answer", QuestionID: waiting.Agent.Questions[0].ID, Answer: "accept", Actor: "agent:worker"}); err == nil || !strings.Contains(err.Error(), "human") {
		t.Fatal("agent-labeled client could approve its own provider request")
	}
	if _, err := call(workerRequest{Method: "interrupt", ExpectedTurnID: "older-turn", Actor: "human"}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal("stale interrupt targeted another turn")
	}
	if _, err := call(workerRequest{Method: "answer", QuestionID: waiting.Agent.Questions[0].ID, Answer: "decline", Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	idle := wait("idle")
	if len(idle.Agent.Questions) != 0 {
		t.Fatal("completed turn left pending approval")
	}
	duplicate, err := call(workerRequest{Method: "send", DeliveryID: "delivery_once", Text: "needs approval", Actor: "human"})
	if err != nil || duplicate.TurnID != "turn-worker" {
		t.Fatal("delivery retry did not return its existing receipt")
	}
	if _, err := call(workerRequest{Method: "send", DeliveryID: "delivery_once", Text: "different input", Actor: "human"}); err == nil || !strings.Contains(err.Error(), "different text") {
		t.Fatal("delivery identity reused for different input")
	}
	data, err := os.ReadFile(options.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint workerFile
	if json.Unmarshal(data, &checkpoint) != nil || checkpoint.Agent.ThreadID != "thread-worker" || checkpoint.Receipts["delivery_once"].TurnID != "turn-worker" {
		t.Fatal("native identity or delivery receipt was not checkpointed")
	}
	// Move the whole storage directory atomically: a late checkpoint can race a
	// remove-file/mkdir sequence and recreate the file before mkdir. The worker
	// never recreates this directory, so the fault remains deterministic.
	if err := os.Rename(stateDir, stateDir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateDir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := call(workerRequest{Method: "send", DeliveryID: "cannot-persist", Text: "must not reach provider", Actor: "human"}); err == nil || !(strings.Contains(err.Error(), "save pending delivery") || strings.Contains(err.Error(), "not ready")) {
		t.Fatal("provider accepted input without a durable pending receipt")
	}
	failed := wait("failed")
	if len(failed.Agent.Capabilities) != 0 || !strings.Contains(failed.Agent.Error, "checkpoint") {
		t.Fatal("checkpoint failure did not disable further sends")
	}
}

func TestWorkerRejectsCorruptCheckpointBeforeStartingProvider(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	err := RunWorker(context.Background(), WorkerOptions{ID: "existing", StateFile: state, Executable: "must-not-start"}, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid native session checkpoint") {
		t.Fatalf("corrupt checkpoint could start a new conversation: %v", err)
	}
}

func TestWorkerUnavailableProviderDoesNotAdvertiseNativeCapabilities(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "st-unavailable-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	executable, err := exec.LookPath("false")
	if err != nil {
		t.Fatal(err)
	}
	options := WorkerOptions{ID: "agent_unavailable", WorkspaceID: "ws_unavailable", Socket: filepath.Join(dir, "worker.sock"), StateFile: filepath.Join(dir, "state.json"), Executable: executable}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWorker(ctx, options, nil, io.Discard, io.Discard) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("failed provider worker did not stop")
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		callCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		reply, err := workerCall(callCtx, options.Socket, workerRequest{Method: "inspect"})
		stop()
		if err == nil && reply.Agent.State == "failed" {
			if reply.Agent.Error == "" || reply.Agent.ThreadID != "" || len(reply.Agent.Capabilities) != 0 {
				t.Fatalf("unavailable provider advertised native state: %#v", reply.Agent)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("unavailable provider did not surface a failed state")
}
