package rpc

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/workspace"
	"golang.org/x/sys/unix"
)

func SocketPath(stateDir string) string {
	abs := workspace.CanonicalPath(stateDir)
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(fmt.Sprintf("/tmp/supertree-next-%d", os.Getuid()), fmt.Sprintf("%x.sock", sum[:16]))
}

// PrivateDir creates only owner-accessible daemon directories. Existing foreign,
// symlinked or permissive directories are rejected rather than silently reused.
func PrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("daemon directory must be owned by you with mode 0700: %s", path)
	}
	return nil
}

type Endpoint struct {
	net.Listener
	lock *os.File
}

// Listen holds a per-state lock, even with an explicitly overridden socket.
// A crashed daemon releases the lock; only then may its stale socket be removed.
func Listen(stateDir, socket string) (*Endpoint, error) {
	dir := filepath.Join(stateDir, "next")
	if err := PrivateDir(dir); err != nil {
		return nil, err
	}
	if err := PrivateDir(filepath.Dir(socket)); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(dir, "daemon.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "daemon.lock")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, &command.Error{Code: "already_running", Message: "a daemon already owns this state directory"}
	}
	ok := false
	defer func() {
		if !ok {
			lock.Close()
		}
	}()
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace a non-socket: %s", socket)
		}
		if conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
			conn.Close()
			return nil, &command.Error{Code: "already_running", Message: "socket is already serving a client"}
		}
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	ok = true
	return &Endpoint{listener, lock}, nil
}

func (e *Endpoint) Close() error {
	err := e.Listener.Close()
	e.lock.Close()
	return err
}

// Running checks the process-held state lock, never a PID file. It also covers
// startup and shutdown, when the socket may not yet be accepting connections.
func Running(stateDir string) (bool, error) {
	fd, err := unix.Open(filepath.Join(stateDir, "next", "daemon.lock"), unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}
