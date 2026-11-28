package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/dawksh/supertree/internal/terminal"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// A real outer PTY observes the actual client's ANSI, cursor and raw-mode
// cleanup. It is not a replacement editor or fake agent transcript.
type nativePTY struct {
	mu, writeMu                     sync.Mutex
	cmd                             *exec.Cmd
	master, slave                   *os.File
	vt                              *vt.Emulator
	original                        *term.State
	exited, outputDone, repliesDone chan struct{}
	code                            int
	once                            sync.Once
	clientExit, release             string
}

func startNativePTY(t *testing.T, binary, dir string, target terminal.Target, columns, rows int) *nativePTY {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err = pty.Setsize(master, &pty.Winsize{Cols: uint16(columns), Rows: uint16(rows)}); err != nil {
		master.Close()
		slave.Close()
		t.Fatal(err)
	}
	fd, err := unix.FcntlInt(master.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		master.Close()
		slave.Close()
		t.Fatal(err)
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		master.Close()
		slave.Close()
		t.Fatal(err)
	}
	master.Close()
	master = os.NewFile(uintptr(fd), "native-client-test-master")
	original, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	markers := t.TempDir()
	clientExit, release := filepath.Join(markers, "client-exit"), filepath.Join(markers, "release")
	// A terminal's shell/session survives its foreground client's exit. Without
	// this parent Darwin revokes the slave, making post-exit ioctls invalid.
	script := `result=$1; release=$2; shift 2; "$@"; result_code=$?; printf '%s\n' "$result_code" > "$result"; while [ ! -e "$release" ]; do sleep .02; done; exit "$result_code"`
	cmd := exec.Command("/bin/sh", "-c", script, "native-pty-test", clientExit, release, binary, "native", "attach", "--state", dir, "--terminal", target.TerminalID, "--generation", "1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	p := &nativePTY{cmd: cmd, master: master, slave: slave, vt: vt.NewEmulator(columns, rows), original: original, exited: make(chan struct{}), outputDone: make(chan struct{}), repliesDone: make(chan struct{}), code: -1, clientExit: clientExit, release: release}
	p.vt.SetScrollbackSize(256)
	if err = cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		t.Fatal(err)
	}
	go func() {
		defer close(p.repliesDone)
		buf := make([]byte, 4096)
		for {
			n, err := p.vt.Read(buf)
			if n > 0 {
				_ = p.write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(p.outputDone)
		buf := make([]byte, 16384)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				p.mu.Lock()
				_, _ = p.vt.Write(buf[:n])
				p.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		p.code = cmd.ProcessState.ExitCode()
		p.mu.Unlock()
		close(p.exited)
	}()
	t.Cleanup(p.close)
	return p
}
func (p *nativePTY) write(data []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.master.SetWriteDeadline(time.Now().Add(time.Second))
	for len(data) > 0 {
		n, err := p.master.Write(data)
		data = data[n:]
		if err != nil {
			return err
		}
	}
	return nil
}
func (p *nativePTY) text() string { p.mu.Lock(); defer p.mu.Unlock(); return p.vt.String() }
func (p *nativePTY) clientCode() (int, bool) {
	b, err := os.ReadFile(p.clientExit)
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return code, err == nil
}
func (p *nativePTY) close() {
	p.once.Do(func() {
		if _, done := p.clientCode(); !done {
			_ = p.write([]byte("\x1bq"))
		}
		_ = os.WriteFile(p.release, nil, 0600)
		select {
		case <-p.exited:
		default:
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-p.exited:
			case <-time.After(5 * time.Second):
				_ = p.cmd.Process.Kill()
				<-p.exited
			}
		}
		_ = p.vt.InputPipe().(io.Closer).Close()
		<-p.repliesDone
		_ = p.slave.Close()
		_ = p.master.Close()
		<-p.outputDone
		p.mu.Lock()
		_ = p.vt.Close()
		p.mu.Unlock()
	})
}
func nativeAwait(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("native terminal condition deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNativeClientRealEditorCrashReconnectAndTerminalRestore(t *testing.T) {
	nvim, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("Neovim unavailable")
	}
	base := t.TempDir()
	binary := filepath.Join(base, "st-next")
	if out, err := exec.Command("go", "build", "-race", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	dir := filepath.Join(base, "host")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- terminal.ServeReady(ctx, dir, func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("native host not ready")
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	status, err := terminal.Call(ctx, dir, terminal.Request{Op: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	launch := terminal.Launch{RequestID: "editor-start", TerminalID: "native-editor", Generation: 1, Definition: terminal.Definition{WorkspaceID: "native-workspace", Executable: nvim, Args: []string{"--clean", "--cmd", "set noswapfile"}, Directory: base, Environment: map[string]string{"HOME": os.Getenv("HOME"), "PATH": os.Getenv("PATH")}, Columns: 80, Rows: 24}}
	response, err := terminal.Call(ctx, dir, terminal.Request{Op: "ensure", Epoch: status.Status.Epoch, Launch: &launch})
	if err != nil {
		t.Fatal(err)
	}
	pid := response.Info.PID
	target := terminal.Target{TerminalID: launch.TerminalID, Generation: 1}
	outer := startNativePTY(t, binary, dir, target, 120, 30)
	nativeAwait(t, func() bool {
		text := outer.text()
		return strings.Contains(text, "NVIM") || strings.Contains(text, "Nvim")
	})
	if err = outer.write([]byte("iCLIENT_UNSAVED 界 é 👩‍💻")); err != nil {
		t.Fatal(err)
	}
	nativeAwait(t, func() bool { return strings.Contains(outer.text(), "CLIENT_UNSAVED 界 é 👩‍💻") })
	// Kill the UI's real terminal session, not merely an in-process context.
	// The independent host and real unsaved editor must remain available.
	if err = outer.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-outer.exited
	outer.close()
	response, err = terminal.Call(ctx, dir, terminal.Request{Op: "inspect", Epoch: status.Status.Epoch, Target: &target})
	if err != nil || response.Info.PID != pid || response.Info.State != "running" {
		t.Fatal("UI death stopped/replaced editor")
	}
	// A client needs a fresh writer lease after the kernel closes the old socket.
	time.Sleep(50 * time.Millisecond)
	outer = startNativePTY(t, binary, dir, target, 90, 28)
	nativeAwait(t, func() bool { return strings.Contains(outer.text(), "CLIENT_UNSAVED 界 é 👩‍💻") })
	if err = outer.write([]byte("\x1b[200~-PASTE-\x1bq\x1b[201~")); err != nil {
		t.Fatal(err)
	}
	nativeAwait(t, func() bool { return strings.Contains(outer.text(), "-PASTE-") })
	if _, done := outer.clientCode(); done {
		t.Fatal("pasted shortcut detached UI")
	}
	if err = outer.write([]byte("\x1bq")); err != nil {
		t.Fatal(err)
	}
	nativeAwait(t, func() bool { _, done := outer.clientCode(); return done })
	outer.mu.Lock()
	alt := outer.vt.IsAltScreen()
	outer.mu.Unlock()
	code, _ := outer.clientCode()
	if code != 0 || alt {
		t.Fatalf("detach did not restore outer screen: exit=%d alt=%v", code, alt)
	}
	current, err := term.GetState(int(outer.slave.Fd()))
	if err != nil || !reflect.DeepEqual(current, outer.original) {
		t.Fatalf("detach did not restore outer terminal modes: current=%#v original=%#v error=%v", current, outer.original, err)
	}
	response, err = terminal.Call(ctx, dir, terminal.Request{Op: "inspect", Epoch: status.Status.Epoch, Target: &target})
	if err != nil || response.Info.PID != pid || response.Info.State != "running" {
		t.Fatal("detach replaced/stopped editor")
	}
	// The actual outer decoder must pass Ctrl-a to Bash's line editor and
	// Ctrl-c to its foreground job, rather than reserving a mux prefix/signal.
	launch = terminal.Launch{RequestID: "shell-start", TerminalID: "native-shell", Generation: 1, Definition: terminal.Definition{WorkspaceID: "native-workspace", Executable: "/bin/bash", Args: []string{"--noprofile", "--norc", "-i"}, Directory: base, Environment: map[string]string{"HOME": os.Getenv("HOME"), "PATH": os.Getenv("PATH"), "PS1": "native> "}, Columns: 80, Rows: 24}}
	if _, err = terminal.Call(ctx, dir, terminal.Request{Op: "ensure", Epoch: status.Status.Epoch, Launch: &launch}); err != nil {
		t.Fatal(err)
	}
	shell := startNativePTY(t, binary, dir, terminal.Target{TerminalID: launch.TerminalID, Generation: 1}, 120, 30)
	nativeAwait(t, func() bool { return strings.Contains(shell.text(), "native>") })
	if err = shell.write([]byte("printf '__SHOULD_NOT_RUN__\\n'\x01#\r")); err != nil {
		t.Fatal(err)
	}
	nativeAwait(t, func() bool { return strings.Contains(shell.text(), "#printf") })
	if strings.Contains(shell.text(), "\n__SHOULD_NOT_RUN__\n") {
		t.Fatal("Ctrl-a did not reach the shell's line editor")
	}
	if err = shell.write([]byte("sleep 30\r")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err = shell.write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err = shell.write([]byte("printf '__STATUS_%s__\\n' \"$?\"\r")); err != nil {
		t.Fatal(err)
	}
	nativeAwait(t, func() bool { return strings.Contains(shell.text(), "__STATUS_130__") })
	shell.close()
	// Already-exited programs keep their last screen and explicit exit status.
	launch = terminal.Launch{RequestID: "exit-start", TerminalID: "native-exited", Generation: 1, Definition: terminal.Definition{WorkspaceID: "native-workspace", Executable: "/bin/sh", Args: []string{"-c", "printf 'EXITED_SCREEN\\n'; exit 17"}, Directory: base, Environment: map[string]string{"PATH": "/usr/bin:/bin"}, Columns: 80, Rows: 24}}
	if _, err = terminal.Call(ctx, dir, terminal.Request{Op: "ensure", Epoch: status.Status.Epoch, Launch: &launch}); err != nil {
		t.Fatal(err)
	}
	target = terminal.Target{TerminalID: launch.TerminalID, Generation: 1}
	nativeAwait(t, func() bool {
		r, e := terminal.Call(ctx, dir, terminal.Request{Op: "inspect", Epoch: status.Status.Epoch, Target: &target})
		return e == nil && r.Info.State == "exited"
	})
	exited := startNativePTY(t, binary, dir, target, 100, 30)
	nativeAwait(t, func() bool {
		text := exited.text()
		return strings.Contains(text, "EXITED_SCREEN") && strings.Contains(text, "exited (17)")
	})
	exited.close()
}
