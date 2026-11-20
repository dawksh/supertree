package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/agent"
	"github.com/dawksh/supertree/internal/workspace"
)

func TestPairingLivesInNormalAgentTabAndResumesExactConversation(t *testing.T) {
	realNvim, _ := exec.LookPath("nvim")
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	base := workspace.CanonicalPath(t.TempDir())
	sockets, err := os.MkdirTemp("/tmp", "st-pair-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	tools := filepath.Join(base, "bin")
	os.MkdirAll(tools, 0700)
	binary := filepath.Join(tools, "st-next")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	source, err := os.ReadFile(filepath.Join(root, "bin", "st"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(tools, "st"), source, 0700)
	if out, err := exec.Command("cp", "-R", filepath.Join(root, "bin", "st-lib"), tools).CombinedOutput(); err != nil {
		t.Fatalf("modules: %s %v", out, err)
	}
	home := filepath.Join(base, "home")
	os.MkdirAll(filepath.Join(home, ".local", "bin"), 0700)
	testExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	guard, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, ".local", "bin", "tmux"), []byte("#!/bin/sh\nexec "+quote(guard)+" -S \"$ST_NEXT_TMUX_SOCKET\" \"$@\"\n"), 0700)
	stub := "#!/bin/sh\nif [ \"${1:-}\" != app-server ]; then printf 'LEGACY_READY\\n'; while IFS= read -r line; do [ \"$line\" != /exit ] || exit 0; done; exit 0; fi\nexport SUPERTREE_PROVIDER_HELPER=1\nexport SUPERTREE_PROVIDER_FIXTURE=" + quote(base) + "\nexec " + quote(testExe) + " -test.run '^TestNativeProviderHelper$' -- \"$@\"\n"
	os.WriteFile(filepath.Join(home, ".local", "bin", "codex"), []byte(stub), 0700)
	editorStub := "#!/bin/sh\n"
	if realNvim != "" {
		editorStub += "case \"$*\" in *--headless*) exec " + quote(realNvim) + " \"$@\";; esac\n"
	}
	os.WriteFile(filepath.Join(home, ".local", "bin", "nvim"), []byte(editorStub+"exec sleep 120\n"), 0700)
	t.Setenv("HOME", home)
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	t.Setenv("ST_STATE", filepath.Join(base, "state"))
	t.Setenv("ST_WORKTREE_ROOT", filepath.Join(base, "trees"))
	t.Setenv("ST_NEXT_SOCKET", filepath.Join(sockets, "core.sock"))
	t.Setenv("ST_NEXT_TMUX_SOCKET", filepath.Join(sockets, "tmux.sock"))
	t.Setenv("ST_NEXT_ACTOR", "human:test")
	t.Setenv("ST_NEXT_REQUEST_ID", "")
	config := filepath.Join(base, "config")
	t.Setenv("ST_CONFIG", config)
	writeConfig := func(mode string) {
		os.WriteFile(config, []byte("ST_HARNESS=codex\nST_WINDOWS='agent vim shell'\nST_AGENT_MODE="+mode+"\nST_SIDEBAR=0\nST_SHELL=/bin/bash\n"), 0600)
	}
	writeConfig("terminal")
	repo := filepath.Join(base, "repo")
	os.Mkdir(repo, 0700)
	exec.Command("git", "init", "-q", "-b", "main", repo).Run()
	exec.Command("git", "-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "initial").Run()
	t.Chdir(repo)
	command := func(program string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, program, args...).CombinedOutput()
	}
	call := func(args ...string) []byte {
		t.Helper()
		out, err := command(binary, args...)
		if err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
		return out
	}
	tmux := func(args ...string) string {
		t.Helper()
		out, err := command("tmux", append([]string{"-S", os.Getenv("ST_NEXT_TMUX_SOCKET")}, args...)...)
		if err != nil {
			t.Fatalf("tmux %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		command(binary, "daemon", "stop")
		command("tmux", "-S", os.Getenv("ST_NEXT_TMUX_SOCKET"), "kill-server")
	})
	// Start with an existing ordinary CLI. Inspecting it must not replace it.
	command(filepath.Join(tools, "st"), "open") // attach fails without a TTY, after windows exist
	session := "repo/main"
	agentPane := tmux("display-message", "-p", "-t", "="+session+":codex", "#{pane_id}")
	before := tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}")
	call("daemon", "start", "--json")
	if out, err := command(binary, "pair", "status"); err == nil || !strings.Contains(string(out), "no paired agent") {
		t.Fatalf("ordinary CLI became a native agent: %s %v", out, err)
	}
	if tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}") != before {
		t.Fatal("inspection replaced a live CLI")
	}
	// Human exits the ordinary CLI and explicitly resumes its exact thread in
	// the same terminal. No production command injects input into the old CLI.
	tmux("send-keys", "-t", agentPane, "-l", "/exit")
	tmux("send-keys", "-t", agentPane, "Enter")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tmux("display-message", "-p", "-t", agentPane, "#{pane_current_command}") == "bash" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	writeConfig("paired")
	tmux("send-keys", "-t", agentPane, "-l", quote(filepath.Join(tools, "st"))+" pair run --thread thread-fixture-exact")
	tmux("send-keys", "-t", agentPane, "Enter")
	wait := func(state string) workspace.Agent {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			out, err := command(binary, "pair", "status")
			var a workspace.Agent
			if err == nil && json.Unmarshal(out, &a) == nil && a.State == state {
				return a
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("agent did not reach %s; terminal: %s", state, tmux("capture-pane", "-p", "-t", agentPane))
		return workspace.Agent{}
	}
	a := wait("idle")
	if !a.Primary || a.ThreadID != "thread-fixture-exact" {
		t.Fatalf("not a primary exact thread: %#v", a)
	}
	// Everyday sharing targets the primary agent in these same original tabs.
	file := filepath.Join(repo, "sample.txt")
	os.WriteFile(file, []byte("not selected\nselected code\n"), 0600)
	exec.Command("git", "add", "sample.txt").Run()
	exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "fixture").Run()
	call("task", "add", "--title", "Human goal added after the agent started")
	var preview agent.ShareResult
	json.Unmarshal(call("share", "--file", file, "--start-line", "2", "--end-line", "2", "--preview", "--json"), &preview)
	if preview.Status != "preview" || preview.Delivery != nil || !strings.Contains(preview.Text, "selected code") || strings.Contains(preview.Text, "not selected") {
		t.Fatalf("preview changed selected code: %#v", preview)
	}
	if _, err := os.Stat(filepath.Join(base, "counter")); !os.IsNotExist(err) {
		t.Fatal("preview sent a provider turn")
	}
	t.Setenv("ST_NEXT_REQUEST_ID", "share-once")
	var shared, repeated agent.ShareResult
	shareArgs := []string{"share", "--file", file, "--start-line", "2", "--end-line", "2", "--message", "Explain selected code", "--json"}
	json.Unmarshal(call(shareArgs...), &shared)
	wait("idle")
	json.Unmarshal(call(shareArgs...), &repeated)
	if shared.Status != "sent" || shared.Delivery == nil || repeated.Delivery == nil || shared.Delivery.ID != repeated.Delivery.ID {
		t.Fatal("share retry delivered twice or lost its receipt")
	}
	actual, _ := os.ReadFile(filepath.Join(base, "delivery"))
	if string(actual) != shared.Delivery.Text || !strings.Contains(string(actual), "Human goal added") || !strings.Contains(string(actual), "selected code") || strings.Contains(string(actual), "not selected") {
		t.Fatalf("shared code/goal differs from receipt: %s", actual)
	}
	counter, _ := os.ReadFile(filepath.Join(base, "counter"))
	if string(counter) != "1" {
		t.Fatal("share retry reached provider again")
	}
	t.Setenv("ST_NEXT_REQUEST_ID", "")
	os.WriteFile(file, []byte("not selected\nhuman edited source\n"), 0600)
	if out, err := command(binary, "share", "--context", preview.ContextIDs[0], "--message", "use reviewed snapshot", "--json"); err == nil || !strings.Contains(string(out), "stale") {
		t.Fatalf("stale reviewed context reached provider: %s %v", out, err)
	}
	call("share", "--diff", "--message", "Review this tracked change", "--json")
	wait("idle")
	actual, _ = os.ReadFile(filepath.Join(base, "delivery"))
	if !strings.Contains(string(actual), "+human edited source") {
		t.Fatal("tracked diff did not reach normal agent tab")
	}
	shellPane := tmux("display-message", "-p", "-t", "="+session+":shell", "#{pane_id}")
	tmux("send-keys", "-t", shellPane, "-l", "printf 'SHELL_CONTEXT\\n'")
	tmux("send-keys", "-t", shellPane, "Enter")
	// Explicit pane IDs, also used by the shell CLI's --output shortcut.
	sourceArgs := agent.ShareArgs{WorkspaceID: a.WorkspaceID, PaneID: shellPane, Message: "Explain this output", Source: &agent.ShareSource{Kind: "output"}}
	encoded, _ := json.Marshal(sourceArgs)
	call("invoke", "context.share", string(encoded))
	wait("idle")
	actual, _ = os.ReadFile(filepath.Join(base, "delivery"))
	if !strings.Contains(string(actual), "SHELL_CONTEXT") {
		t.Fatal("existing shell output was not shared")
	}
	if realNvim != "" {
		t.Setenv("PAIR_SOURCE_FILE", file)
		lua := `lua local done=false; vim.notify=function(msg,level) if level==vim.log.levels.ERROR then error(msg) end; if msg:find('Shared with',1,true) then done=true end end; assert(vim.fn.exists(':SupertreeAsk')==2,'paired editor did not load bridge'); vim.cmd.edit(vim.fn.fnameescape(vim.env.PAIR_SOURCE_FILE)); vim.cmd('2,2SupertreeAsk Explain selected code from Vim'); assert(vim.wait(8000,function() return done end),'editor delivery timed out'); vim.cmd('qa!')`
		out, err := command(filepath.Join(tools, "st"), "_run", "nvim", "--headless", "-u", "NONE", "-c", lua)
		if err != nil {
			t.Fatalf("native editor sharing: %s %v", out, err)
		}
		wait("idle")
		actual, _ = os.ReadFile(filepath.Join(base, "delivery"))
		if !strings.Contains(string(actual), "Explain selected code from Vim") || !strings.Contains(string(actual), "human edited source") {
			t.Fatal("Vim selection did not reach normal agent tab")
		}
	}
	if tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}") != before {
		t.Fatal("pairing created a second terminal or replaced another program")
	}
	var listed struct {
		Processes []workspace.Process `json:"processes"`
	}
	json.Unmarshal(call("process", "list"), &listed)
	if len(listed.Processes) != 3 {
		t.Fatalf("expected original three terminals: %#v", listed.Processes)
	}
	for _, p := range listed.Processes {
		if p.ID == a.ProcessID && (p.PaneID != agentPane || p.Source != "hosted") {
			t.Fatalf("worker not in original agent tab: %#v", p)
		}
	}
	params, _ := json.Marshal(agent.BindArgs{WorkspaceID: a.WorkspaceID, PaneID: agentPane})
	if out, err := command(binary, "invoke", "agent.bind", string(params)); err == nil || !strings.Contains(string(out), "already running") {
		t.Fatalf("duplicate primary binding allowed: %s %v", out, err)
	}
	call("pair", "send", "--message", "need-approval")
	a = wait("waiting")
	q := a.Questions[0]
	call("question", "answer", q.ID, "--revision", fmt.Sprint(q.Revision), "--answer", "decline")
	wait("idle")
	call("pair", "send", "--message", "wait-for-interrupt")
	wait("running")
	call("pair", "interrupt")
	a = wait("interrupted")
	call("daemon", "stop")
	call("daemon", "start", "--json")
	if next := wait("interrupted"); next.ID != a.ID {
		t.Fatal("core restart changed primary identity")
	}
	if tmux("list-panes", "-a", "-F", "#{pane_id} #{pane_pid}") != before {
		t.Fatal("core restart changed live tabs")
	}
	command(filepath.Join(tools, "st"), "down", "main")
	command(filepath.Join(tools, "st"), "open")
	agentPane = tmux("display-message", "-p", "-t", "="+session+":codex", "#{pane_id}")
	resumed := wait("idle")
	if resumed.ID != a.ID || resumed.ThreadID != a.ThreadID || resumed.ProcessID != a.ProcessID {
		t.Fatalf("reopen lost exact identity: %#v", resumed)
	}
	log, _ := os.ReadFile(filepath.Join(base, "calls"))
	if strings.Contains(string(log), "thread/start") || strings.Count(string(log), "thread/resume") != 2 {
		t.Fatalf("migration or reopen guessed a conversation: %s", log)
	}
}
