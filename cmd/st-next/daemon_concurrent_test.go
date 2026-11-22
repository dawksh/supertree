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
