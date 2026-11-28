package terminal

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func namespace(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return fmt.Sprintf("%x", sum[:16])
}
func socketPath(dir string) string {
	if sandbox := os.Getenv("SUPERTREE_TEST_SANDBOX"); sandbox != "" {
		return filepath.Join(sandbox, "native-sockets", namespace(dir)+".sock")
	}
	return filepath.Join(fmt.Sprintf("/tmp/supertree-terminal-%d", os.Getuid()), namespace(dir)+".sock")
}

func testScope(dir string) error {
	if sandbox := os.Getenv("SUPERTREE_TEST_SANDBOX"); sandbox != "" {
		root, err := filepath.EvalSymlinks(sandbox)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return failure("unsafe_state", "test terminal hosts must stay inside the isolated runner namespace")
		}
	}
	return nil
}

func privateDir(dir string, create bool) (string, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) == string(filepath.Separator) {
		return "", failure("invalid_arguments", "host state directory must be absolute and private")
	}
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) {
		return "", failure("unsafe_state", "host directory must be owned by you with mode 0700 and cannot be a symlink")
	}
	return filepath.EvalSymlinks(dir)
}

func openPrivate(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Base(path))
	i, err := f.Stat()
	if err == nil {
		s, ok := i.Sys().(*syscall.Stat_t)
		if !ok || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 || s.Uid != uint32(os.Getuid()) || s.Nlink != 1 {
			err = failure("unsafe_state", "host state file must be private, regular and unlinked elsewhere")
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

type endpoint struct {
	net.Listener
	lock *os.File
}

func listen(dir string) (*endpoint, error) {
	lock, err := openPrivate(filepath.Join(dir, "host.lock"), unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			lock.Close()
		}
	}()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, failure("already_running", "a terminal host owns this namespace")
	}
	socket := socketPath(dir)
	if _, err = privateDir(filepath.Dir(socket), true); err != nil {
		return nil, err
	}
	if info, e := os.Lstat(socket); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, failure("unsafe_state", "refusing to replace a non-socket host endpoint")
		}
		if c, e := net.DialTimeout("unix", socket, 200*time.Millisecond); e == nil {
			c.Close()
			return nil, failure("already_running", "terminal host endpoint is live")
		}
		if err = os.Remove(socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		l.Close()
		return nil, err
	}
	ok = true
	return &endpoint{l, lock}, nil
}
func (e *endpoint) Close() error { err := e.Listener.Close(); e.lock.Close(); return err }
