package stack

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/term"
)

func startTest(t *testing.T, cmd *exec.Cmd, w, h int) *Terminal {
	t.Helper()
	cmd.Env = append(ChildEnvironment(), cmd.Env...)
	tm, err := Start(cmd, w, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tm.Close)
	return tm
}
func waitFrame(t *testing.T, tm *Terminal, predicate func(Frame) bool) Frame {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		f := tm.Snapshot()
		if predicate(f) {
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal condition timed out: %+v\n%s", struct {
				PID, Exit  int
				Exited     bool
				InputError string
			}{f.PID, f.ExitCode, f.Exited, f.InputError}, f.Text())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func inputText(t *testing.T, tm *Terminal, s string) {
	t.Helper()
	if err := tm.Input(nil, s, false); err != nil {
		t.Fatal(err)
	}
}

// TestPTYChild is a private terminal program, not a fake editor/harness. It
// performs real kernel PTY queries and records the exact input it receives.
func TestPTYChild(t *testing.T) {
	mode := os.Getenv("ST_STACK_CHILD")
	if mode == "" {
		return
	}
	if _, err := term.MakeRaw(0); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "query":
		fmt.Print("\x1b[2J\x1b[H\x1b[6n")
		buf := make([]byte, 6)
		if _, err := io.ReadFull(os.Stdin, buf); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("QUERY:%s\r\n", hex.EncodeToString(buf))
	case "paste":
		fmt.Print("\x1b[?2004hPASTE-READY\r\n")
		buf := make([]byte, len("\x1b[200~one\n\x01d\x1b[201~"))
		if _, err := io.ReadFull(os.Stdin, buf); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("PASTE:%s\r\n", hex.EncodeToString(buf))
	case "keys":
		fmt.Print("\x1b[?1hKEYS-READY\r\n")
		buf := make([]byte, 3)
		if _, err := io.ReadFull(os.Stdin, buf); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("KEY:%s\r\n", hex.EncodeToString(buf))
	case "linekeys":
		fmt.Print("LINEKEYS-READY\r\n")
		buf := make([]byte, 2)
		if _, err := io.ReadFull(os.Stdin, buf); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("LINEKEYS:%s\r\n", hex.EncodeToString(buf))
	case "no-reader":
		// Query flooding must not trap the output reader on vt's reply pipe.
		for i := 0; i < 10000; i++ {
			fmt.Print("\x1b[6n")
		}
		fmt.Print("DRAINED-WITHOUT-INPUT-READER\r\n")
		time.Sleep(time.Second)
	case "unicode":
		for _, chunk := range []string{"e", "\u0301", "👩", "\u200d", "💻", "X"} {
			fmt.Print(chunk)
			// The parent acknowledges observation before the next write, so
			// scheduling cannot accidentally batch these into one PTY read.
			var ack [1]byte
			if _, err := io.ReadFull(os.Stdin, ack[:]); err != nil {
				t.Fatal(err)
			}
		}
	case "hostile-unicode":
		fmt.Print("a" + strings.Repeat("\u0301", 20_000) + "\r\nAFTER-HOSTILE-PTY")
	}
}
func childCommand(mode string) *exec.Cmd {
	c := exec.Command(os.Args[0], "-test.run=^TestPTYChild$")
	c.Env = []string{"ST_STACK_CHILD=" + mode}
	return c
}

func TestHiddenQueryPasteAndModeAwareKeys(t *testing.T) {
	t.Run("Ctrl-j stays distinct from Enter", func(t *testing.T) {
		tm := startTest(t, childCommand("linekeys"), 80, 24)
		waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "LINEKEYS-READY") })
		var decoder uv.EventDecoder
		for _, raw := range []string{"\n", "\r"} {
			_, event := decoder.Decode([]byte(raw))
			key := event.(uv.KeyPressEvent).Key()
			if err := tm.Input(&key, "", false); err != nil {
				t.Fatal(err)
			}
		}
		waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "LINEKEYS:0a0d") })
	})
	t.Run("query with no client", func(t *testing.T) {
		tm := startTest(t, childCommand("query"), 80, 24)
		<-tm.exited // No snapshot or attached client is needed for the reply.
		f := waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "QUERY:1b5b313b3152") })
		if f.ExitCode != 0 {
			t.Fatal(f.ExitCode)
		}
	})
	t.Run("bracketed paste", func(t *testing.T) {
		tm := startTest(t, childCommand("paste"), 80, 24)
		waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "PASTE-READY") })
		if err := tm.Input(nil, "one\n\x01d", true); err != nil {
			t.Fatal(err)
		}
		want := "PASTE:" + hex.EncodeToString([]byte("\x1b[200~one\n\x01d\x1b[201~"))
		waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), want) })
	})
	t.Run("outer decode and child application cursor", func(t *testing.T) {
		tm := startTest(t, childCommand("keys"), 80, 24)
		waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "KEYS-READY") })
		var decoder uv.EventDecoder
		_, event := decoder.Decode([]byte("\x1b[A"))
		key := event.(uv.KeyPressEvent).Key()
		if err := tm.Input(&key, "", false); err != nil {
			t.Fatal(err)
		}
		waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "KEY:1b4f41") })
	})
	t.Run("query flood without child reads", func(t *testing.T) {
		tm := startTest(t, childCommand("no-reader"), 80, 24)
		waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "DRAINED-WITHOUT-INPUT-READER") })
	})
}

func TestRealShellJobControlAndResize(t *testing.T) {
	testShellJobControl(t, "/bin/bash", "--noprofile", "--norc", "-i")
}

func TestRealZshJobControlAndResize(t *testing.T) {
	path, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("stock Zsh unavailable; Oh My Zsh remains a manual acceptance case")
	}
	testShellJobControl(t, path, "-f", "-i")
}

func TestOptionalOhMyZshThemeAndGitPlugin(t *testing.T) {
	root := os.Getenv("ST_STACK_OMZ_ROOT")
	if root == "" {
		t.Skip("explicit installed Oh My Zsh path required; never source user dotfiles")
	}
	if _, err := os.Stat(root + "/oh-my-zsh.sh"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	rc := "export ZSH=" + shellQuote(root) + "\nexport ZSH_CACHE_DIR=" + shellQuote(home+"/cache") + "\nexport ZSH_COMPDUMP=" + shellQuote(home+"/.zcompdump") + "\nzstyle ':omz:update' mode disabled\nDISABLE_AUTO_UPDATE=true\nZSH_DISABLE_COMPFIX=true\nZSH_THEME=amuse\nplugins=(git)\nsource \"$ZSH/oh-my-zsh.sh\"\nprintf 'FRAMEWORK:%s:%s\\n' \"${+functions[git_prompt_info]}\" \"${+aliases[gst]}\"\n"
	if err := os.WriteFile(home+"/.zshrc", []byte(rc), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("zsh", "-i")
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "ZDOTDIR=" + home, "HISTFILE=" + home + "/history"}
	tm := startTest(t, cmd, 100, 30)
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "FRAMEWORK:1:1") })
	if err := tm.Input(nil, "printf '%s\\n' 'OMZ-界-e\u0301-👩\u200d💻'", true); err != nil {
		t.Fatal(err)
	}
	inputText(t, tm, "\r")
	waitFrame(t, tm, func(f Frame) bool { return strings.Count(f.Text(), "OMZ-界-e\u0301-👩\u200d💻") >= 2 })
	inputText(t, tm, "sleep 120\r")
	time.Sleep(100 * time.Millisecond)
	inputText(t, tm, "\x1a")
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(strings.ToLower(f.Text()), "suspended") })
	inputText(t, tm, "fg\r")
	time.Sleep(100 * time.Millisecond)
	inputText(t, tm, "\x03")
	inputText(t, tm, "printf 'OMZ-RC:%s\\n' \"$?\"\r")
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "OMZ-RC:130") })
	if err := tm.Resize(110, 32); err != nil {
		t.Fatal(err)
	}
	inputText(t, tm, "printf 'OMZ-SIZE:'; stty size\r")
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "OMZ-SIZE:32 110") })
	inputText(t, tm, "exit 0\r")
	waitFrame(t, tm, func(f Frame) bool { return f.Exited && f.ExitCode == 0 })
}

func testShellJobControl(t *testing.T, executable string, args ...string) {
	t.Helper()
	c := exec.Command(executable, args...)
	c.Env = []string{"PS1=STACK-PROMPT> ", "PROMPT=STACK-PROMPT> ", "PROMPT_COMMAND=", "HISTFILE=" + t.TempDir() + "/history", "ZDOTDIR=" + t.TempDir()}
	tm := startTest(t, c, 80, 24)
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "STACK-PROMPT>") })
	inputText(t, tm, "sleep 120\r")
	time.Sleep(100 * time.Millisecond)
	inputText(t, tm, "\x1a")
	waitFrame(t, tm, func(f Frame) bool {
		text := strings.ToLower(f.Text())
		return strings.Contains(text, "stopped") || strings.Contains(text, "suspended")
	})
	inputText(t, tm, "fg\r")
	time.Sleep(100 * time.Millisecond)
	inputText(t, tm, "\x03")
	inputText(t, tm, "printf 'JOB-RC:%s\\n' \"$?\"\r")
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "JOB-RC:130") })
	inputText(t, tm, "sleep 120 &\r")
	inputText(t, tm, "jobs\r")
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(strings.ToLower(f.Text()), "running") })
	if err := tm.Resize(90, 30); err != nil {
		t.Fatal(err)
	}
	inputText(t, tm, "printf 'SIZE:'; stty size\r")
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "SIZE:30 90") })
	inputText(t, tm, "kill $(jobs -p); wait; exit 7\r")
	f := waitFrame(t, tm, func(f Frame) bool { return f.Exited })
	if f.ExitCode != 7 {
		t.Fatalf("exit status=%d", f.ExitCode)
	}
}

func TestRealNeovimUnsavedBufferSurvivesResizeAndHiddenOutput(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim unavailable; manual editor gate remains open")
	}
	c := exec.Command("nvim", "--clean", "--cmd", "set noswapfile")
	tm := startTest(t, c, 80, 24)
	waitFrame(t, tm, func(f Frame) bool { return f.AltScreen })
	inputText(t, tm, "iUNSAVED-界-e\u0301\x1b")
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "UNSAVED-界-e\u0301") })
	pid := tm.Snapshot().PID
	if err := tm.Resize(40, 18); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "UNSAVED-界-e\u0301") })
	time.Sleep(100 * time.Millisecond) // no client observes this interval
	if err := tm.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	f := waitFrame(t, tm, func(f Frame) bool { return strings.Contains(f.Text(), "UNSAVED-界-e\u0301") })
	if f.PID != pid {
		t.Fatal("editor was replaced")
	}
	escape := uv.Key{Code: uv.KeyEscape}
	if err := tm.Input(&escape, "", false); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, tm, func(f Frame) bool {
		return f.CursorShape == int(uv.CursorBlock) && !strings.Contains(f.Text(), "-- INSERT --")
	})
	inputText(t, tm, ":q!\r")
	waitFrame(t, tm, func(f Frame) bool { return f.Exited && !f.AltScreen })
}

func TestExitRetainsScreenAndStatus(t *testing.T) {
	tm := startTest(t, exec.Command("/bin/sh", "-c", "printf 'final-screen'; exit 23"), 40, 10)
	f := waitFrame(t, tm, func(f Frame) bool { return f.Exited && strings.Contains(f.Text(), "final-screen") })
	if f.ExitCode != 23 {
		t.Fatal(f.ExitCode)
	}
}

func TestCloseHasBoundedLifetime(t *testing.T) {
	tm := startTest(t, exec.Command("sleep", "120"), 40, 10)
	done := make(chan struct{})
	go func() { tm.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("PTY shutdown blocked")
	}
}

func TestClosingFirstTerminalDoesNotDependOnLaterChild(t *testing.T) {
	first := startTest(t, exec.Command("/bin/sh", "-c", "while :; do printf 'noise\\r\\n'; done"), 40, 10)
	second := startTest(t, exec.Command("sleep", "120"), 40, 10)
	done := make(chan struct{})
	go func() { first.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("later child retained a PTY descriptor needed by the first terminal")
	}
	if second.Snapshot().Exited {
		t.Fatal("closing the first terminal stopped another child")
	}
}

func TestNoisyHiddenTerminalDoesNotBlockQuietTerminal(t *testing.T) {
	noisy := startTest(t, exec.Command("/bin/sh", "-c", "while :; do printf 'noise-0123456789\\r\\n'; done"), 80, 24)
	quiet := startTest(t, exec.Command("/bin/sh", "-c", "read line; printf 'ACK:%s' \"$line\""), 80, 24)
	waitFrame(t, noisy, func(f Frame) bool { return f.OutputBytes > 65536 })
	start := time.Now()
	inputText(t, quiet, "still-responsive\n")
	waitFrame(t, quiet, func(f Frame) bool { return strings.Contains(f.Text(), "ACK:still-responsive") })
	t.Logf("quiet input-to-observed-screen while hidden output runs: %s (not outer input-to-paint)", time.Since(start))
	if f := noisy.Snapshot(); f.Scrollback > ScrollbackLines {
		t.Fatal("unbounded scrollback")
	}
}

func TestServeReconnectsWithoutRelaunch(t *testing.T) {
	tm := startTest(t, exec.Command("/bin/sh", "-c", "printf 'persistent-screen'; exec sleep 120"), 40, 10)
	dir, err := os.MkdirTemp("/tmp", "st-stack-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, dir, []*Terminal{tm}) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var original int
	for i := 0; i < 2; i++ {
		deadline := time.Now().Add(3 * time.Second)
		var c *Client
		for {
			c, err = Dial(dir)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		f, err := c.Call(Request{Op: "snapshot"})
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			original = f.Frames[0].PID
		} else if f.Frames[0].PID != original {
			t.Fatal("reconnect relaunched program")
		}
	}
}

func TestOptionalCodexScratchStartup(t *testing.T) {
	if os.Getenv("ST_STACK_HARNESS_PROBE") != "1" {
		t.Skip("opt-in local startup smoke; authenticated harness/approval gate remains manual")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	codexHome := home + "/codex"
	if err := os.Mkdir(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("codex", "--no-daemon", "--no-alt-screen", "--sandbox", "read-only", "--cd", home)
	c.Env = []string{"HOME=" + home, "CODEX_HOME=" + codexHome, "XDG_CONFIG_HOME=" + home + "/.config", "OPENAI_API_KEY=", "CODEX_API_KEY=", "OPENAI_BASE_URL="}
	tm := startTest(t, c, 100, 30)
	f := waitFrame(t, tm, func(f Frame) bool {
		return strings.Contains(f.Text(), "Welcome to Codex") || strings.Contains(f.Text(), "Sign in with ChatGPT")
	})
	if f.Exited {
		t.Fatal("Codex exited before onboarding UI")
	}
	t.Log("Actual Codex onboarding rendered through the candidate. No credentials, model call, or approval interaction was used; authenticated harness gate remains open.")
}
