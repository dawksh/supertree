package terminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type process struct {
	sessionMu                       sync.Mutex // pins the unreaped leader until its session is empty.
	reaped                          bool
	mu                              sync.Mutex
	vt                              *vt.Emulator
	cmd                             *exec.Cmd
	pty                             *os.File
	outputDone, repliesDone, exited chan struct{}
	pipeOnce, closeOnce             sync.Once
	bytes                           atomic.Uint64
	sequence                        uint64
	geometry                        uint64
	exitCode                        *int
	signal                          int
	inputError                      string
	subscribers                     map[chan struct{}]bool
	disposed                        bool
	writeBudget                     atomic.Int64
}

func startProcess(d Definition) (*process, error) {
	cmd := exec.Command(d.Executable, d.Args...)
	cmd.Dir = d.Directory
	// The launch definition is the complete environment, not a patch over the
	// host's possibly stale login/configuration. Virtual-terminal values win.
	keys := make([]string, 0, len(d.Environment))
	for k := range d.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cmd.Env = []string{}
	for _, k := range keys {
		switch k {
		case "TMUX", "TMUX_PANE", "TERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "TERM_SESSION_ID", "COLORTERM", "VTE_VERSION", "WT_SESSION", "WINDOWID":
			continue
		}
		if strings.HasPrefix(k, "WEZTERM_") || strings.HasPrefix(k, "KITTY_") || strings.HasPrefix(k, "ITERM_") {
			continue
		}
		cmd.Env = append(cmd.Env, k+"="+d.Environment[k])
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "TERM_PROGRAM=supertree-native", "COLORTERM=truecolor")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(d.Columns), Rows: uint16(d.Rows)})
	if err != nil {
		return nil, failure("spawn_failed", "terminal executable could not be started; inspect the private launch definition")
	}
	// NewFile must see a nonblocking FD at construction on Darwin, and the
	// duplicate must be CLOEXEC so later launches cannot retain this PTY.
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		err = unix.SetNonblock(fd, true)
	}
	if err != nil {
		if fd >= 0 {
			unix.Close(fd)
		}
		f.Close()
		cmd.Process.Kill()
		cmd.Wait()
		return nil, failure("spawn_failed", "could not initialize terminal descriptor")
	}
	f.Close()
	f = os.NewFile(uintptr(fd), "native-pty")
	if err = f.SetWriteDeadline(time.Time{}); err != nil {
		f.Close()
		cmd.Process.Kill()
		cmd.Wait()
		return nil, failure("spawn_failed", "terminal descriptor does not support deadlines")
	}
	p := &process{vt: vt.NewEmulator(d.Columns, d.Rows), cmd: cmd, pty: f, outputDone: make(chan struct{}), repliesDone: make(chan struct{}), exited: make(chan struct{}), sequence: 1, geometry: 1, subscribers: map[chan struct{}]bool{}}
	p.vt.SetScrollbackSize(ScrollbackLines)
	// Do not retain arbitrary link/title/cwd strings in cells/history. Their
	// parser buffer is bounded upstream; none is a supported outer side effect.
	for _, cmd := range []int{0, 1, 2, 7, 8, 52} {
		p.vt.RegisterOscHandler(cmd, func([]byte) bool { return true })
	}
	go p.readReplies()
	go p.readOutput()
	go p.reapSession()
	return p, nil
}

func (p *process) recordExit(status syscall.WaitStatus) {
	code, signal := status.ExitStatus(), 0
	if status.Signaled() {
		code = -1
		signal = int(status.Signal())
	}
	p.mu.Lock()
	if p.exitCode == nil {
		p.exitCode = &code
		p.signal = signal
		p.sequence++
		p.notifyLocked()
	}
	p.mu.Unlock()
}

func (p *process) reapSession() {
	for {
		p.sessionMu.Lock()
		status, exited, err := leaderStatus(p.cmd.Process.Pid)
		if err == nil && exited {
			p.recordExit(status)
			members, err := sessionMembers(p.cmd.Process.Pid)
			if err == nil && len(members) == 0 {
				// Until this point the original child remains unreaped, so its
				// PID/SID cannot be recycled underneath background-job inventory.
				_ = p.cmd.Wait()
				p.reaped = true
				close(p.exited)
				p.sessionMu.Unlock()
				return
			}
		}
		p.sessionMu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *process) members() ([]member, error) {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	if p.reaped {
		return nil, nil
	}
	return sessionMembers(p.cmd.Process.Pid)
}

func (p *process) breakInput(message string) {
	// Close the pipe before taking the emulator lock: an output/query handler
	// might hold it while blocked in PipeWriter.Write.
	p.pipeOnce.Do(func() { _ = p.vt.InputPipe().(io.Closer).Close() })
	p.mu.Lock()
	if p.inputError == "" {
		p.inputError = message
		p.sequence++
		p.notifyLocked()
	}
	p.mu.Unlock()
}

func (p *process) readReplies() {
	defer close(p.repliesDone)
	buf := make([]byte, 4096)
	for {
		n, err := p.vt.Read(buf)
		if n > 0 {
			deadline := time.Now().Add(250 * time.Millisecond)
			if budget := p.writeBudget.Load(); budget != 0 {
				if limit := time.Unix(0, budget); limit.Before(deadline) {
					deadline = limit
				}
			}
			_ = p.pty.SetWriteDeadline(deadline)
			data := buf[:n]
			for len(data) > 0 {
				w, e := p.pty.Write(data)
				data = data[w:]
				if e != nil {
					p.breakInput("PTY input failed; delivery is uncertain and must not be replayed")
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *process) readOutput() {
	defer close(p.outputDone)
	buf := make([]byte, 16<<10)
	for {
		n, err := p.pty.Read(buf)
		if n > 0 {
			p.mu.Lock()
			_, _ = p.vt.Write(buf[:n])
			p.sequence++
			p.notifyLocked()
			p.mu.Unlock()
			p.bytes.Add(uint64(n))
		}
		if err != nil {
			return
		}
	}
}

// signalSession inventories only the session created by pty.Start. Every PID is
// revalidated by birth identity, SID and PGID immediately before signaling. A
// persisted receipt's PID is never passed here. Daemonized/new-session children
// are outside this terminal's job-control session and are not signaled.
func (p *process) signalSession(sig syscall.Signal) (int, error) {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	if p.reaped {
		return 0, nil
	}
	members, err := sessionMembers(p.cmd.Process.Pid)
	if err != nil {
		return 0, err
	}
	for _, member := range members {
		current, err := processIdentity(member.pid)
		if err == nil && current == member {
			_ = unix.Kill(member.pid, sig)
		}
	}
	return len(members), nil
}

func (p *process) stop(force bool) error {
	// Continue stopped jobs so they can receive HUP/TERM, including shell job
	// groups in the background rather than only the original shell's group.
	for _, sig := range []syscall.Signal{syscall.SIGCONT, syscall.SIGHUP, syscall.SIGTERM} {
		if _, err := p.signalSession(sig); err != nil {
			return failure("stop_failed", "could not inventory the owned terminal session")
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		members, err := p.members()
		if err != nil {
			return failure("stop_failed", "could not verify the owned terminal session")
		}
		if len(members) == 0 {
			break
		}
		if time.Now().After(deadline) {
			if !force {
				return failure("stop_pending", "terminal jobs remain; inspect before requesting force stop")
			}
			if _, err := p.signalSession(syscall.SIGKILL); err != nil {
				return err
			}
			// Bounded repeated inventory covers children forked during TERM.
			if time.Now().After(deadline.Add(2 * time.Second)) {
				return failure("stop_failed", "owned terminal session did not finish stopping")
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-p.exited:
	case <-time.After(3 * time.Second):
		return failure("stop_failed", "terminal exit status is not yet available")
	}
	// Reaping and PTY EOF are independent. Drain already-written final output
	// before closing the master; an exited child can still have bytes queued.
	select {
	case <-p.outputDone:
	case <-time.After(time.Second):
		p.breakInput("terminal output remained open at stop; final drain was incomplete")
	}
	p.closeIO()
	return nil
}

func (p *process) closeIO() {
	p.closeOnce.Do(func() {
		p.pipeOnce.Do(func() { _ = p.vt.InputPipe().(io.Closer).Close() })
		_ = p.pty.Close()
		<-p.repliesDone
		<-p.outputDone
	})
}
func (p *process) dispose() {
	p.closeIO()
	p.mu.Lock()
	p.disposed = true
	p.notifyLocked()
	_ = p.vt.Close()
	p.mu.Unlock()
}

func (p *process) info(info Info) Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.infoLocked(info)
}
func (p *process) infoLocked(info Info) Info {
	if p.exitCode != nil {
		code := *p.exitCode
		info.State = "exited"
		info.ExitCode = &code
		info.Signal = p.signal
	}
	if p.inputError != "" {
		info.Error = p.inputError
	}
	return info
}

// writeBytes is kept private: Stage 4 exposes no unrestricted input RPC.
func (p *process) writeBytes(data []byte) error {
	if len(data) > 64<<10 {
		return failure("invalid_arguments", "input exceeds bound")
	}
	_ = p.pty.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
	for len(data) > 0 {
		n, err := p.pty.Write(data)
		data = data[n:]
		if err != nil {
			return errors.New("terminal input delivery is uncertain")
		}
	}
	return nil
}

func envKeys(d Definition) []string {
	keys := []string{}
	for k := range d.Environment {
		if !strings.HasPrefix(k, "=") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
