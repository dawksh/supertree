package terminal

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func processText(p *process) string {
	var b strings.Builder
	for row := 0; row < 24; row += 16 {
		s, err := p.screen(Info{}, row, 16)
		if err == nil {
			b.WriteString(screenText(s))
		}
	}
	return b.String()
}
func fgGroup(p *process) int {
	raw, err := p.pty.SyscallConn()
	if err != nil {
		return 0
	}
	var group int
	_ = raw.Control(func(fd uintptr) { group, _ = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP) })
	return group
}

func TestOwnedShellJobControlAndSessionStop(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	d := testLaunch(t.TempDir(), "shell", "").Definition
	d.Executable = "/bin/bash"
	d.Args = []string{"--noprofile", "--norc", "-i"}
	d.Environment["HOME"] = t.TempDir()
	d.Environment["PS1"] = ""
	p, err := startProcess(d)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.stop(true); p.dispose() }()
	await(t, func() bool { return fgGroup(p) == p.cmd.Process.Pid })
	if err = p.writeBytes([]byte("stty -echo\r")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err = p.writeBytes([]byte("sleep 30\r")); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return fgGroup(p) > 0 && fgGroup(p) != p.cmd.Process.Pid })
	if err = p.writeBytes([]byte{26}); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return fgGroup(p) == p.cmd.Process.Pid && strings.Contains(processText(p), "Stopped") })
	if err = p.writeBytes([]byte("fg\r")); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return fgGroup(p) > 0 && fgGroup(p) != p.cmd.Process.Pid })
	if err = p.writeBytes([]byte{3}); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return fgGroup(p) == p.cmd.Process.Pid })
	if err = p.writeBytes([]byte("printf 'EXIT_%s\\n' \"$?\"; sleep 30 &\r")); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return strings.Contains(processText(p), "EXIT_130") })
	await(t, func() bool { members, _ := sessionMembers(p.cmd.Process.Pid); return len(members) >= 2 })
	unrelated := exec.Command("/bin/sleep", "30")
	if err = unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { unrelated.Process.Kill(); unrelated.Wait() }()
	if err = p.stop(true); err != nil {
		t.Fatal(err)
	}
	members, err := sessionMembers(p.cmd.Process.Pid)
	if err != nil || len(members) != 0 {
		t.Fatal("background/stopped session jobs survived stop")
	}
	if err = unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("stop killed an unrelated program")
	}
}

func TestStopRequiresExplicitForceForIgnoringJobs(t *testing.T) {
	dir := privateTestDir(t)
	l := testLaunch(dir, "ignore", `trap '' HUP TERM; printf 'IGNORE_READY\n'; while :; do sleep 1; done`)
	p, err := startProcess(l.Definition)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.stop(true); p.dispose() }()
	await(t, func() bool { return strings.Contains(processText(p), "IGNORE_READY") })
	err = p.stop(false)
	assertCode(t, err, "stop_pending")
	if members, _ := sessionMembers(p.cmd.Process.Pid); len(members) == 0 {
		t.Fatal("graceful stop silently forced termination")
	}
	if err = p.stop(true); err != nil {
		t.Fatal(err)
	}
	info := p.info(Info{})
	if info.ExitCode == nil || info.Signal != int(syscall.SIGKILL) {
		t.Fatalf("missing forced exit status: %#v", info)
	}
}

func TestRealNeovimUnsavedContentsRemainInOwnedEmulator(t *testing.T) {
	nvim, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("Neovim unavailable")
	}
	dir := privateTestDir(t)
	d := testLaunch(dir, "editor", "").Definition
	d.Executable = nvim
	d.Args = []string{"--clean", "--cmd", "set noswapfile"}
	d.Environment["HOME"] = t.TempDir()
	p, err := startProcess(d)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.stop(true); p.dispose() }()
	await(t, func() bool {
		return strings.Contains(processText(p), "NVIM") || strings.Contains(processText(p), "Nvim")
	})
	if err = p.writeBytes([]byte("iunsaved 界 é 👩‍💻")); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return strings.Contains(processText(p), "unsaved 界 é 👩‍💻") })
	if _, err = os.Stat(dir + "/untitled"); !os.IsNotExist(err) {
		t.Fatal("fixture unexpectedly saved editor contents")
	}
}

func TestHiddenTerminalRepliesAndEnvironmentIsolation(t *testing.T) {
	dir := privateTestDir(t)
	l := testLaunch(dir, "query", `stty raw -echo; printf '\033[6n'; reply=$(dd bs=1 count=6 2>/dev/null); printf '\r\nQUERY_OK:%s\r\n' "$reply"; printf 'ENV:%s:%s:%s\r\n' "$PRIVATE_QUOTED" "$TERM" "${TMUX-unset}"; sleep 30`)
	l.Definition.Environment["PRIVATE_QUOTED"] = "a b ' $() # %"
	l.Definition.Environment["TMUX"] = "must-not-inherit"
	l.Definition.Environment["TERM"] = "outer-must-not-inherit"
	p, err := startProcess(l.Definition)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.stop(true); p.dispose() }()
	await(t, func() bool {
		return strings.Contains(processText(p), "QUERY_OK:") && strings.Contains(processText(p), "a b ' $() # %:xterm-256color:unset")
	})
	// No subscriber/client was attached while the child waited for its query.
	if info := p.info(Info{}); info.Error != "" {
		t.Fatal("hidden query reply failed")
	}
}

func TestHostContextCancellationStopsOnlyOwnedNamespace(t *testing.T) {
	dir := privateTestDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() { done <- ServeReady(ctx, dir, func() { close(ready) }) }()
	<-ready
	status := mustCall(t, dir, Request{Op: "hello"}).Status
	l := testLaunch(dir, "owned", "sleep 30")
	pid := mustCall(t, dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &l}).Info.PID
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown stalled")
	}
	if members, _ := sessionMembers(pid); len(members) > 0 {
		t.Fatal("namespace shutdown leaked jobs")
	}
	if running, err := Running(dir); err != nil || running {
		t.Fatal("shutdown kept lock")
	}
}

func TestExitedLeaderPinsSessionUntilBackgroundJobsEnd(t *testing.T) {
	d := testLaunch(privateTestDir(t), "background", `set -m; (trap '' HUP TERM; printf 'BACKGROUND_READY\n'; exec sleep 30) & sleep .1; exit 9`).Definition
	d.Executable = "/bin/bash"
	d.Args = []string{"--noprofile", "--norc", "-c", d.Args[1]}
	p, err := startProcess(d)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.stop(true); p.dispose() }()
	await(t, func() bool { return p.info(Info{}).State == "exited" })
	if code := p.info(Info{}).ExitCode; code == nil || *code != 9 {
		t.Fatal("shell exit status missing while background job survives")
	}
	if members, err := p.members(); err != nil || len(members) == 0 {
		t.Fatal("background session was lost when shell exited")
	}
	select {
	case <-p.exited:
		t.Fatal("leader reaped before background session ended")
	default:
	}
	if err = p.stop(true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	default:
		t.Fatal("leader was not reaped after session stop")
	}
	unrelated := exec.Command("/bin/sleep", "30")
	if err = unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { unrelated.Process.Kill(); unrelated.Wait() }()
	// Model diagnostic PID reuse on a retained, already-reaped terminal. Stop
	// must not inventory or signal the new process, even if the number matches.
	retained := &process{cmd: &exec.Cmd{Process: unrelated.Process}, reaped: true}
	if count, err := retained.signalSession(syscall.SIGKILL); err != nil || count != 0 {
		t.Fatal("reaped terminal tried to signal another session")
	}
	if err = unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("reused diagnostic PID was killed")
	}
}

func TestStopRetainsFinalScreenAndStatus(t *testing.T) {
	d := testLaunch(privateTestDir(t), "final", `i=0; while [ "$i" -lt 300 ]; do printf '%0200d\n' "$i"; i=$((i+1)); done; printf '\033[2J\033[HFINAL_SCREEN 界 é 👩‍💻\n'; exit 17`).Definition
	p, err := startProcess(d)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.stop(true); p.dispose() }()
	await(t, func() bool {
		_, exited, err := leaderStatus(p.cmd.Process.Pid)
		return err == nil && exited || p.info(Info{}).State == "exited"
	})
	if err = p.stop(false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(processText(p), "FINAL_SCREEN 界 é 👩‍💻") {
		t.Fatal("stop discarded queued final PTY output")
	}
	if info := p.info(Info{}); info.ExitCode == nil || *info.ExitCode != 17 || info.Error != "" {
		t.Fatalf("final outcome: %#v", info)
	}
}
