package stack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompositorClientCrashAndReattachKeepRealEditor(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim unavailable; editor/client gate remains open")
	}
	binary := filepath.Join(t.TempDir(), "spike")
	if out, err := exec.Command("go", "build", "-race", "-o", binary, "./cmd/spike").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	dir, err := os.MkdirTemp("/tmp", "st-stack-client-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	log, err := os.Create(filepath.Join(dir, "host.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	host := exec.Command(binary, "serve", "-state", dir, "-command", "exec /bin/bash --noprofile --norc -i", "-command", "exec nvim --clean --cmd 'set noswapfile'", "-command", "printf 'THIRD-TERMINAL'; exec sleep 120")
	host.Env = ChildEnvironment()
	host.Stdout, host.Stderr = log, log
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Wait() }()
	t.Cleanup(func() {
		if c, err := Dial(dir); err == nil {
			_, _ = c.Call(Request{Op: "stop"})
			c.Close()
		}
		select {
		case err := <-done:
			if err != nil {
				data, _ := os.ReadFile(filepath.Join(dir, "host.log"))
				t.Errorf("host exit: %s %v", data, err)
			}
		case <-time.After(5 * time.Second):
			_ = host.Process.Kill()
			<-done
			t.Error("experiment host did not stop")
		}
	})
	call := func(req Request) Response {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			c, err := Dial(dir)
			if err == nil {
				response, err := c.Call(req)
				c.Close()
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	initial := call(Request{Op: "snapshot"})
	pids := []int{}
	for _, f := range initial.Frames {
		pids = append(pids, f.PID)
	}
	attach := func() *Terminal {
		t.Helper()
		outer := startTest(t, exec.Command(binary, "attach", "-state", dir), 120, 30)
		waitFrame(t, outer, func(f Frame) bool {
			return strings.Contains(f.Text(), "terminal proof") && strings.Contains(f.Text(), "THIRD-TERMINAL")
		})
		return outer
	}
	outer := attach()
	// Ctrl-a belongs to the real shell's line editor by default. Inserting #
	// after it must comment out the command, not arm a workspace prefix.
	inputText(t, outer, "printf 'CTRL-A-OK\\n'")
	inputText(t, outer, "\x01#\r")
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "#printf") })
	// Hide shell (terminal 3), then put the existing editor left of the agent.
	inputText(t, outer, "\x1b3\x1b0")
	waitFrame(t, outer, func(f Frame) bool {
		return strings.Contains(f.Text(), "views [1 2]") && !strings.Contains(f.Text(), "THIRD-TERMINAL")
	})
	if f := call(Request{Op: "snapshot"}).Frames[2]; f.Exited || f.PID != pids[2] {
		t.Fatal("hiding shell stopped/replaced it")
	}
	inputText(t, outer, "\x1b2\x1bH")
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "views [2 1]") })
	inputText(t, outer, "\x1b3")
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "THIRD-TERMINAL") })
	inputText(t, outer, "\x1b0\x1b0\x1b0")
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "all views hidden") })
	inputText(t, outer, "IGNORED-WHILE-HIDDEN")
	inputText(t, outer, "\x1b1\x1b2\x1b3")
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "THIRD-TERMINAL") })
	for _, f := range call(Request{Op: "snapshot"}).Frames {
		if strings.Contains(f.Text(), "IGNORED-WHILE-HIDDEN") || f.Exited {
			t.Fatal("empty layout misrouted input or stopped a terminal")
		}
	}
	inputText(t, outer, "\x1b2")
	// Let the real editor finish its initial terminal negotiation before typing.
	time.Sleep(150 * time.Millisecond)
	inputText(t, outer, "iCLIENT-UNSAVED")
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "CLIENT-UNSAVED") })
	inputText(t, outer, "\x1bL")
	waitFrame(t, outer, func(f Frame) bool {
		return strings.Contains(f.Text(), "views [1 2 3]") && strings.Contains(f.Text(), "CLIENT-UNSAVED")
	})
	inputText(t, outer, "-MOVED")
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "CLIENT-UNSAVED-MOVED") })
	inputText(t, outer, "\x1bz")
	waitFrame(t, outer, func(f Frame) bool {
		return strings.Contains(f.Text(), "CLIENT-UNSAVED") && !strings.Contains(f.Text(), "THIRD-TERMINAL")
	})
	if f := call(Request{Op: "snapshot"}).Frames[1]; f.Width != 120 {
		t.Fatalf("focused resize did not reach editor: %d", f.Width)
	}
	// Kill just the UI process, then close its outer PTY. The host is a separate
	// OS process, so this exercises actual descriptor/lifetime separation.
	_ = outer.cmd.Process.Kill()
	waitFrame(t, outer, func(f Frame) bool { return f.Exited })
	outer.Close()
	for i, f := range call(Request{Op: "snapshot"}).Frames {
		if f.PID != pids[i] || f.Exited {
			t.Fatalf("UI crash stopped/replaced terminal %d", i)
		}
	}
	outer = attach()
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "CLIENT-UNSAVED") })
	inputText(t, outer, "\x1b2")
	// Paste shortcut-shaped text; it must reach the child rather than detach.
	inputText(t, outer, "\x1b[200~-PASTED-\x1bq\x1b[201~")
	waitFrame(t, outer, func(f Frame) bool { return strings.Contains(f.Text(), "-PASTED-") })
	if outer.Snapshot().Exited {
		t.Fatal("pasted prefix detached the client")
	}
	inputText(t, outer, "\x1bq")
	f := waitFrame(t, outer, func(f Frame) bool { return f.Exited })
	if f.ExitCode != 0 || f.AltScreen {
		t.Fatalf("detach did not restore outer screen: exit=%d alt=%t", f.ExitCode, f.AltScreen)
	}
	for i, f := range call(Request{Op: "snapshot"}).Frames {
		if f.PID != pids[i] || f.Exited {
			t.Fatalf("detach stopped/replaced terminal %d", i)
		}
	}
	legacy := startTest(t, exec.Command(binary, "attach", "-state", dir, "-legacy-prefix"), 120, 30)
	waitFrame(t, legacy, func(f Frame) bool { return strings.Contains(f.Text(), "THIRD-TERMINAL") })
	inputText(t, legacy, "\x012\x01z")
	waitFrame(t, legacy, func(f Frame) bool {
		return strings.Contains(f.Text(), "focus 2") && !strings.Contains(f.Text(), "THIRD-TERMINAL")
	})
	inputText(t, legacy, "\x01d")
	waitFrame(t, legacy, func(f Frame) bool { return f.Exited && f.ExitCode == 0 })
}
