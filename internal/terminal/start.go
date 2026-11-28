package terminal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Running tests the process-held namespace lock, never a PID receipt.
func Running(dir string) (bool, error) {
	dir, err := privateDir(dir, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	f, err := openPrivate(filepath.Join(dir, "host.lock"), unix.O_RDWR)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return true, nil
	}
	return false, err
}

// StartHost is explicit. A startup lock is inherited by the host so killing the
// initiating CLI cannot cause a second contender to launch a competing host.
func StartHost(ctx context.Context, dir, executable string) (Status, error) {
	dir, err := privateDir(dir, true)
	if err != nil {
		return Status{}, err
	}
	if err = testScope(dir); err != nil {
		return Status{}, err
	}
	lock, err := openPrivate(filepath.Join(dir, "startup.lock"), unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		return Status{}, err
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return Status{}, err
		}
		if err = pause(ctx); err != nil {
			return Status{}, err
		}
	}
	if response, err := Call(ctx, dir, Request{Op: "hello"}); err == nil {
		return *response.Status, nil
	}
	if live, err := Running(dir); err != nil {
		return Status{}, err
	} else if live {
		return Status{}, failure("host_busy", "a host owns the namespace but is not answering; inspect it without replacing programs")
	}
	log, err := openPrivate(filepath.Join(dir, "host.log"), unix.O_CREAT|unix.O_APPEND|unix.O_WRONLY)
	if err != nil {
		return Status{}, err
	}
	defer log.Close()
	child := exec.Command(executable, "_terminal-host", dir)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.ExtraFiles = []*os.File{lock}
	child.Stdout, child.Stderr = log, log
	// Hosts receive no ambient child launch secrets; definitions carry the
	// exact private environment over the socket instead of command arguments.
	child.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if sandbox := os.Getenv("SUPERTREE_TEST_SANDBOX"); sandbox != "" {
		child.Env = append(child.Env, "SUPERTREE_TEST_SANDBOX="+sandbox, "TMPDIR="+os.Getenv("TMPDIR"))
	}
	if err = child.Start(); err != nil {
		return Status{}, failure("host_start_failed", "could not start terminal host")
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	for {
		probe, done := context.WithTimeout(ctx, 200*time.Millisecond)
		response, e := Call(probe, dir, Request{Op: "hello"})
		done()
		if e == nil {
			return *response.Status, nil
		}
		select {
		case <-exited:
			return Status{}, failure("host_start_failed", "terminal host exited during startup; inspect its private host.log")
		default:
		}
		if err = pause(ctx); err != nil {
			return Status{}, failure("host_start_uncertain", "terminal host startup timed out; inspect before retrying")
		}
	}
}
func pause(ctx context.Context) error {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
