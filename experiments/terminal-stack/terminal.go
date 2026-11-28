// Package stack is a disposable Stage 2 experiment, not Supertree's runtime.
package stack

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const ScrollbackLines = 256

type Terminal struct {
	mu                                         sync.Mutex // vt is deliberately used by one owner at a time.
	vt                                         *vt.Emulator
	cmd                                        *exec.Cmd
	pty                                        *os.File
	input                                      chan []byte
	closing                                    chan struct{}
	outputDone, repliesDone, inputDone, exited chan struct{}
	once                                       sync.Once
	bytes                                      atomic.Uint64
	dropped                                    atomic.Uint64
	exitCode                                   int
	inputError                                 string
}

func validSize(width, height int) bool {
	return width >= 2 && width <= 240 && height >= 2 && height <= 100
}

// Start owns a real controlling PTY. The caller must explicitly Close it.
func Start(cmd *exec.Cmd, width, height int) (*Terminal, error) {
	if !validSize(width, height) {
		return nil, fmt.Errorf("experiment size must be 2..240 columns and 2..100 rows")
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)})
	if err != nil {
		return nil, err
	}
	// creack/pty's master starts outside Go's poller on Darwin. Duplicate it
	// before NewFile so Go sees O_NONBLOCK at construction, enabling deadlines
	// and Close interrupting Read. Setting O_NONBLOCK on the old File alone is
	// insufficient; never give two os.File values ownership of the same fd.
	// CLOEXEC is essential: a later child must not retain another PTY master.
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		err = unix.SetNonblock(fd, true)
	}
	if err != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		f.Close()
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}
	_ = f.Close()
	f = os.NewFile(uintptr(fd), "terminal-stack-master")
	if err := f.SetWriteDeadline(time.Time{}); err != nil {
		f.Close()
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("PTY deadlines: %w", err)
	}
	t := &Terminal{vt: vt.NewEmulator(width, height), cmd: cmd, pty: f,
		input: make(chan []byte, 128), closing: make(chan struct{}),
		outputDone: make(chan struct{}), repliesDone: make(chan struct{}),
		inputDone: make(chan struct{}), exited: make(chan struct{}), exitCode: -1}
	t.vt.SetScrollbackSize(ScrollbackLines)
	// Replies are drained even with zero clients. Nothing here depends on paint.
	go t.readReplies()
	go t.writeInput()
	go t.readOutput()
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = cmd.ProcessState.ExitCode()
		}
		t.mu.Lock()
		t.exitCode = code
		t.mu.Unlock()
		close(t.exited)
	}()
	return t, nil
}

func (t *Terminal) readOutput() {
	defer close(t.outputDone)
	buf := make([]byte, 16*1024)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.mu.Lock()
			_, _ = t.vt.Write(buf[:n])
			t.mu.Unlock()
			t.bytes.Add(uint64(n))
		}
		if err != nil {
			return
		}
	}
}

func (t *Terminal) enqueue(data []byte) bool {
	select {
	case <-t.closing:
		return false
	default:
	}
	select {
	case t.input <- append([]byte(nil), data...):
		return true
	default:
		t.dropped.Add(1)
		return false
	}
}

func (t *Terminal) readReplies() {
	defer close(t.repliesDone)
	buf := make([]byte, 4096)
	for {
		n, err := t.vt.Read(buf)
		if n > 0 {
			t.enqueue(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (t *Terminal) writeInput() {
	defer close(t.inputDone)
	for {
		select {
		case <-t.closing:
			return
		case data := <-t.input:
			_ = t.pty.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
			for len(data) > 0 {
				n, err := t.pty.Write(data)
				data = data[n:]
				if err != nil {
					t.mu.Lock()
					t.inputError = err.Error()
					t.mu.Unlock()
					break
				}
			}
		}
	}
}

// Input is mode-aware: outer escape bytes are decoded by Ultraviolet and keys
// are encoded using the child's modes. Enhanced encodings remain unsupported.
func (t *Terminal) Input(key *uv.Key, text string, paste bool) error {
	if len(text) > 64*1024 {
		return errors.New("input exceeds experiment limit")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.closing:
		return errors.New("terminal closed")
	default:
	}
	switch {
	case key != nil:
		t.vt.SendKey(uv.KeyPressEvent(*key))
	case paste:
		t.vt.Paste(text)
	default:
		t.vt.SendText(text)
	}
	return nil
}

func (t *Terminal) Resize(width, height int) error {
	if !validSize(width, height) {
		return errors.New("invalid experiment terminal size")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	raw, err := t.pty.SyscallConn()
	if err != nil {
		return err
	}
	var resizeErr error
	err = raw.Control(func(fd uintptr) {
		resizeErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(width), Row: uint16(height)})
	})
	if err != nil {
		return err
	}
	if resizeErr != nil {
		return resizeErr
	}
	t.vt.Resize(width, height)
	return nil
}

func (t *Terminal) Close() {
	t.once.Do(func() {
		close(t.closing)
		// Close the pipe itself first: vt.Read reads a non-atomic closed flag,
		// so vt.Close must wait until that reader is finished. This also frees
		// a writer blocked producing a query reply while holding the vt lock.
		_ = t.vt.InputPipe().(io.Closer).Close()
		<-t.repliesDone
		select {
		case <-t.exited:
		default:
			// This is a bounded experiment, not a production process-tree stop.
			if pg, err := unix.IoctlGetInt(int(t.pty.Fd()), unix.TIOCGPGRP); err == nil && pg > 0 {
				_ = syscall.Kill(-pg, syscall.SIGKILL)
			}
			_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
			_ = t.cmd.Process.Kill()
		}
		_ = t.pty.Close()
		<-t.outputDone
		<-t.inputDone
		<-t.exited
		t.mu.Lock()
		_ = t.vt.Close()
		t.mu.Unlock()
	})
}
