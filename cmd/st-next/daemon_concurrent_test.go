package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/workspace"
	"golang.org/x/sys/unix"
)

func TestConcurrentDaemonStartsConverge(t *testing.T) {
	base := t.TempDir()
	binary := filepath.Join(base, "st-next")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	state := filepath.Join(base, "state")
	env := append(os.Environ(), "ST_STATE="+state, "ST_WORKTREE_ROOT="+filepath.Join(base, "trees"), "ST_NEXT_SOCKET=", "ST_NEXT_TMUX_SOCKET="+filepath.Join(base, "tmux"))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, binary, "daemon", "stop")
		c.Dir = base
		c.Env = env
		_ = c.Run()
	})
	const n = 32
	type result struct {
		out []byte
		err error
	}
	results := make([]result, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, binary, "daemon", "start", "--json")
			c.Dir = base
			c.Env = env
			results[i].out, results[i].err = c.CombinedOutput()
		}(i)
	}
	close(start)
	wg.Wait()
	pid := 0
	for i, r := range results {
		if r.err != nil {
			t.Errorf("start %d: %v: %s", i, r.err, r.out)
			continue
		}
		var s rpc.Status
		if err := json.Unmarshal(r.out, &s); err != nil || !s.Running || s.PID == 0 {
			t.Errorf("start %d: invalid status %s: %v", i, r.out, err)
			continue
		}
		if pid == 0 {
			pid = s.PID
		}
		if s.PID != pid {
			t.Errorf("start %d returned PID %d, want %d", i, s.PID, pid)
		}
	}
}

// TestMain provides a controllable detached child for the parent-death regression.
func TestMain(m *testing.M) {
	if os.Getenv("ST_TEST_START_PARENT") == "1" {
		base := os.Getenv("ST_TEST_START_BASE")
		os.Unsetenv("ST_TEST_START_PARENT")
		os.Setenv("ST_TEST_START_CHILD", "1")
		_, err := startDaemon(context.Background(), workspace.Options{StateDir: filepath.Join(base, "state")}, filepath.Join(base, "socket"))
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if os.Getenv("ST_TEST_START_CHILD") == "1" {
		base := os.Getenv("ST_TEST_START_BASE")
		os.WriteFile(filepath.Join(base, "child-ready"), []byte("ready"), 0600)
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(filepath.Join(base, "child-exit")); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestStartupLockSurvivesInitiatorDeath(t *testing.T) {
	base := t.TempDir()
	parent := exec.Command(os.Args[0])
	parent.Env = append(os.Environ(), "ST_TEST_START_PARENT=1", "ST_TEST_START_BASE="+base)
	// Only the detached child should enter the child branch.
	// The parent changes the environment before it launches its child in TestMain.
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.WriteFile(filepath.Join(base, "child-exit"), nil, 0600)
		parent.Process.Kill()
		parent.Wait()
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(base, "child-ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detached child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	parent.Wait()
	f, err := os.OpenFile(filepath.Join(base, "state", "next", "startup.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		t.Fatal("startup lock released while detached child is still starting")
	}
	if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "child-exit"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			unix.Flock(int(f.Fd()), unix.LOCK_UN)
			break
		}
		if (err != unix.EWOULDBLOCK && err != unix.EAGAIN) || time.Now().After(deadline) {
			t.Fatalf("startup lock not released after child exit: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
