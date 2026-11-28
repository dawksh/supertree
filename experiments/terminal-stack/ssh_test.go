package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// Opt-in only: the target and its private experiment host must be explicitly
// prepared by the operator. There are no default remote hosts or credentials.
func TestOptionalPlainSSHDisconnectAndReattach(t *testing.T) {
	target, dir, binary := os.Getenv("ST_STACK_SSH_TARGET"), os.Getenv("ST_STACK_SSH_STATE"), os.Getenv("ST_STACK_SSH_BIN")
	if target == "" {
		t.Skip("operator-supplied SSH target required")
	}
	if !strings.HasPrefix(dir, "/tmp/st-terminal-stack.") || !strings.HasPrefix(binary, "/tmp/st-terminal-stack.") {
		t.Fatal("SSH proof requires an explicitly prepared /tmp/st-terminal-stack.* namespace")
	}
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "ForwardAgent=no", "-S", "none"}
	inspect := func() Response {
		t.Helper()
		args := append(append([]string{}, sshArgs...), "--", target, shellQuote(binary)+" inspect -state "+shellQuote(dir))
		out, err := exec.Command("ssh", args...).Output()
		if err != nil {
			t.Fatal(err)
		}
		var r Response
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("remote inspection: %v", err)
		}
		return r
	}
	before := inspect()
	if len(before.Frames) != 3 {
		t.Fatal("SSH proof needs shell, real nvim, hidden-output terminal")
	}
	attach := func() *Terminal {
		t.Helper()
		args := append(append([]string{}, sshArgs...), "-tt", "--", target, "TERM=xterm-256color "+shellQuote(binary)+" attach -state "+shellQuote(dir))
		outer := startTest(t, exec.Command("ssh", args...), 120, 30)
		waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "terminal proof") })
		return outer
	}
	outer := attach()
	inputText(t, outer, "\x1b2")
	time.Sleep(200 * time.Millisecond)
	message := fmt.Sprintf("SSH-%x-界-e\u0301-👩\u200d💻", time.Now().UnixNano())
	// This is an explicitly prepared test editor; reset only its scratch
	// buffer so a repeated run cannot pass on stale text from an earlier run.
	inputText(t, outer, "\x1b")
	time.Sleep(150 * time.Millisecond)
	inputText(t, outer, "ggdGi"+message)
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), message) })
	_ = outer.cmd.Process.Kill()
	waitFrame(t, outer, func(f Frame) bool { return f.Exited })
	outer.Close()
	time.Sleep(200 * time.Millisecond)
	check := func() {
		t.Helper()
		after := inspect()
		for i, f := range after.Frames {
			if f.PID != before.Frames[i].PID || f.Exited {
				t.Fatalf("SSH disconnect replaced/stopped terminal %d", i)
			}
		}
		if after.Frames[2].OutputBytes <= before.Frames[2].OutputBytes {
			t.Fatal("hidden output stopped while SSH client disconnected")
		}
	}
	check()
	outer = attach()
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), message) })
	if err := outer.Resize(100, 28); err != nil {
		t.Fatal(err)
	}
	inputText(t, outer, "\x1b2\x1bz")
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), message) })
	deadline := time.Now().Add(8 * time.Second)
	for inspect().Frames[1].Width != 100 {
		if time.Now().After(deadline) {
			t.Fatal("SSH resize did not reach editor")
		}
		time.Sleep(50 * time.Millisecond)
	}
	inputText(t, outer, "\x1bq")
	f := waitFrame(t, outer, func(f Frame) bool { return f.Exited })
	if f.ExitCode != 0 || f.AltScreen {
		t.Fatalf("SSH detach did not restore terminal: exit=%d alt=%t", f.ExitCode, f.AltScreen)
	}
	check()
}
