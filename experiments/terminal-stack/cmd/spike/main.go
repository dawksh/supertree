// spike is a disposable feasibility driver. It does not import the main module.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	stack "github.com/dawksh/supertree/experiments/terminal-stack"
)

type commands []string

func (c *commands) String() string     { return strings.Join(*c, "; ") }
func (c *commands) Set(s string) error { *c = append(*c, s); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: spike start|attach|inspect|stop|measure [-state private-directory] [-command 'exec program args']")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	dir := fs.String("state", "", "private experiment namespace (start creates one if omitted)")
	count := fs.Int("terminals", 16, "number of real PTYs in the measurement")
	duration := fs.Duration("duration", 2*time.Second, "measurement duration")
	legacyPrefix := fs.Bool("legacy-prefix", false, "opt into the old experiment's Ctrl-a fallback (attach only)")
	var cmds commands
	fs.Var(&cmds, "command", "explicit shell command; repeat for up to 3 terminals")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	switch os.Args[1] {
	case "start":
		if *dir == "" {
			var err error
			*dir, err = os.MkdirTemp("/tmp", "st-terminal-stack-")
			if err != nil {
				return err
			}
		}
		if err := stack.PrivateDirectory(*dir); err != nil {
			return err
		}
		log, err := os.OpenFile(filepath.Join(*dir, "host.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer log.Close()
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		args := []string{"serve", "-state", *dir}
		for _, c := range cmds {
			args = append(args, "-command", c)
		}
		child := exec.Command(executable, args...)
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout, child.Stderr = log, log
		if err := child.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		deadline := time.NewTimer(8 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case err := <-done:
				return fmt.Errorf("experiment host exited (%v); inspect %s", err, filepath.Join(*dir, "host.log"))
			case <-ctx.Done():
				_ = child.Process.Kill()
				<-done
				return ctx.Err()
			case <-deadline.C:
				_ = child.Process.Signal(syscall.SIGTERM)
				<-done
				return errors.New("experiment startup timed out")
			case <-tick.C:
				c, err := stack.Dial(*dir)
				if err != nil {
					continue
				}
				_, err = c.Call(stack.Request{Op: "snapshot"})
				c.Close()
				if err != nil {
					continue
				}
				fmt.Println(*dir)
				return nil
			}
		}
	case "serve":
		if err := stack.PrivateDirectory(*dir); err != nil {
			return err
		}
		lock, err := os.OpenFile(filepath.Join(*dir, "host.lock"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		lock.Close()
		defer os.Remove(filepath.Join(*dir, "host.lock"))
		if len(cmds) == 0 {
			cmds = commands{"exec /bin/bash --noprofile --norc -i", "exec nvim --clean", "exec /bin/bash --noprofile --norc -i"}
		}
		if len(cmds) > 3 {
			return errors.New("experiment supports at most three displayed terminals")
		}
		var terminals []*stack.Terminal
		defer func() {
			for _, t := range terminals {
				t.Close()
			}
		}()
		for _, c := range cmds {
			cmd := exec.Command("/bin/sh", "-c", c)
			cmd.Env = stack.ChildEnvironment()
			t, err := stack.Start(cmd, 40, 22)
			if err != nil {
				return err
			}
			terminals = append(terminals, t)
		}
		return stack.Serve(ctx, *dir, terminals)
	case "attach":
		return stack.Attach(ctx, *dir, *legacyPrefix)
	case "inspect", "stop":
		c, err := stack.Dial(*dir)
		if err != nil {
			return err
		}
		defer c.Close()
		op := "snapshot"
		if os.Args[1] == "stop" {
			op = "stop"
		}
		response, err := c.Call(stack.Request{Op: op})
		if err != nil {
			return err
		}
		if op == "snapshot" {
			return json.NewEncoder(os.Stdout).Encode(response)
		}
		return nil
	case "measure":
		return measure(*count, *duration)
	case "probe":
		return json.NewEncoder(os.Stdout).Encode(stack.ProbeCandidate())
	default:
		return errors.New("unknown experiment action")
	}
}

func measure(count int, duration time.Duration) error {
	if count < 2 || count > 64 || duration < time.Second || duration > 30*time.Second {
		return errors.New("measure requires 2..64 terminals and 1..30 seconds")
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var terminals []*stack.Terminal
	defer func() {
		for _, t := range terminals {
			t.Close()
		}
	}()
	for i := 0; i < count; i++ {
		c := exec.Command("/bin/sh", "-c", "exec sleep 120")
		if i == 0 {
			c = exec.Command("/bin/sh", "-c", "while :; do printf 'noisy-output-0123456789\\r\\n'; done")
		}
		c.Env = stack.ChildEnvironment()
		t, err := stack.Start(c, 80, 24)
		if err != nil {
			return err
		}
		terminals = append(terminals, t)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	var latencies []float64
	startBytes := terminals[0].Snapshot().OutputBytes
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		start := time.Now()
		_ = terminals[len(terminals)-1].Snapshot()
		latencies = append(latencies, float64(time.Since(start).Microseconds())/1000)
		time.Sleep(10 * time.Millisecond)
	}
	sort.Float64s(latencies)
	result := map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version(), "terminals": count, "geometry": "80x24", "scrollback_lines": stack.ScrollbackLines, "heap_delta_bytes": int64(after.HeapAlloc) - int64(before.HeapAlloc), "sys_delta_bytes": int64(after.Sys) - int64(before.Sys), "quiet_snapshot_samples": len(latencies), "quiet_snapshot_p50_ms": latencies[len(latencies)/2], "quiet_snapshot_p95_ms": latencies[min(len(latencies)-1, len(latencies)*95/100)], "noisy_output_bytes": terminals[0].Snapshot().OutputBytes - startBytes, "duration_seconds": duration.Seconds(), "scope": "Go heap includes emulators/PTY machinery; excludes child RSS and OS page cache; latency is snapshot acquisition, not input-to-paint"}
	return json.NewEncoder(os.Stdout).Encode(result)
}
