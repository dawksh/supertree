package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/terminal"
	"golang.org/x/sys/unix"
)

func TestNativeTerminalCLIConcurrentStartAndDaemonIndependence(t *testing.T) {
	base := t.TempDir()
	binary := filepath.Join(base, "st-next")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	dir := filepath.Join(base, "native-host")
	command := func(input []byte, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, binary, args...)
		c.Stdin = bytes.NewReader(input)
		c.Env = append(os.Environ(), "ST_STATE="+filepath.Join(base, "control"), "ST_NEXT_SOCKET=", "ST_WORKTREE_ROOT="+filepath.Join(base, "trees"), "ST_NEXT_TMUX_SOCKET="+filepath.Join(base, "tmux"))
		return c.CombinedOutput()
	}
	t.Cleanup(func() {
		if out, err := command(nil, "terminal", "host", "stop", "--state", dir, "--force"); err != nil {
			t.Errorf("host cleanup: %v: %s", err, out)
		}
		_, _ = command(nil, "daemon", "stop")
	})
	const n = 12
	var wg sync.WaitGroup
	statuses := make(chan terminal.Status, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := command(nil, "terminal", "host", "start", "--state", dir)
			if err != nil {
				errs <- err
				return
			}
			var s terminal.Status
			if err = json.Unmarshal(out, &s); err != nil {
				errs <- err
				return
			}
			statuses <- s
		}()
	}
	wg.Wait()
	close(statuses)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var status terminal.Status
	for s := range statuses {
		if status.PID != 0 && (status.PID != s.PID || status.Epoch != s.Epoch) {
			t.Fatal("concurrent CLI starts created multiple hosts")
		}
		status = s
	}
	if status.PID == 0 {
		t.Fatal("no native host")
	}
	l := terminal.Launch{RequestID: "cli-launch", TerminalID: "cli-terminal", Generation: 1, Definition: terminal.Definition{WorkspaceID: "ws_cli", Executable: "/bin/sh", Args: []string{"-c", `printf x >> once; while :; do printf 'native-tick\n'; sleep .1; done`}, Directory: base, Environment: map[string]string{"PATH": "/usr/bin:/bin"}, Columns: 80, Rows: 24}}
	input, _ := json.Marshal(map[string]any{"epoch": status.Epoch, "launch": l})
	out, err := command(input, "terminal", "ensure", "--state", dir)
	if err != nil {
		t.Fatalf("launch: %v: %s", err, out)
	}
	var launched terminal.Response
	if err = json.Unmarshal(out, &launched); err != nil || launched.Info == nil {
		t.Fatal("missing CLI receipt")
	}
	pid := launched.Info.PID
	for _, action := range []string{"start", "stop", "start", "stop"} {
		if out, err = command(nil, "daemon", action, "--json"); err != nil {
			t.Fatalf("control %s: %v: %s", action, err, out)
		}
	}
	out, err = command(nil, "terminal", "inventory", "--state", dir)
	if err != nil {
		t.Fatal(err)
	}
	var inventory terminal.Response
	if err = json.Unmarshal(out, &inventory); err != nil || inventory.Status == nil {
		t.Fatal("inventory missing")
	}
	if inventory.Status.Epoch != status.Epoch || len(inventory.Status.Terminals) != 1 || inventory.Status.Terminals[0].PID != pid || inventory.Status.Terminals[0].State != "running" {
		t.Fatal("control daemon lifetime affected native program")
	}
	b, _ := os.ReadFile(filepath.Join(base, "once"))
	if string(b) != "x" {
		t.Fatal("CLI duplicated program")
	}
	// Ordinary core shutdown has no permission to stop this separate host.
	if out, err = command(nil, "terminal", "host", "stop", "--state", dir); err == nil {
		t.Fatal("host silently stopped running programs without explicit force")
	}
	bad := []byte(`{"epoch":"x","unexpected":"do not log a private definition"}`)
	if out, err = command(bad, "terminal", "ensure", "--state", dir); err == nil || bytes.Contains(out, []byte("do not log")) {
		t.Fatal("invalid input accepted or echoed")
	}
	// Deliberately leave an inner fixture running. Its runner must stop that
	// host/session before removing state, while our outer host remains alive.
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	script := `import json,os,subprocess,sys
binary=sys.argv[1]
state=os.environ['SUPERTREE_TEST_SANDBOX']+'/owned-native'
status=json.loads(subprocess.check_output([binary,'terminal','host','start','--state',state]))
definition=dict(workspace_id='runner-fixture',executable='/bin/sh',args=['-c','exec sleep 30'],directory=state,environment=dict(PATH='/usr/bin:/bin'),columns=80,rows=24)
request=dict(epoch=status['epoch'],launch=dict(request_id='runner-start',terminal_id='runner-terminal',generation=1,definition=definition))
subprocess.check_output([binary,'terminal','ensure','--state',state],input=json.dumps(request).encode())
print(json.dumps(status))
`
	inner := exec.Command("bash", "../../scripts/test-local.sh", "--", python, "-c", script, binary)
	out, err = inner.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated runner cleanup: %v: %s", err, out)
	}
	var innerStatus terminal.Status
	if err = json.Unmarshal(out, &innerStatus); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(innerStatus.Directory); !os.IsNotExist(err) {
		t.Fatal("inner runner did not remove stopped native state")
	}
	out, err = command(nil, "terminal", "inventory", "--state", dir)
	if err != nil {
		t.Fatal("inner cleanup stopped unrelated outer host")
	}
	t.Run("actual host crash retains uncertainty", func(t *testing.T) {
		crashDir := filepath.Join(base, "crash-host")
		foreground := exec.Command(binary, "terminal", "host", "serve", "--state", crashDir)
		foreground.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := foreground.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			foreground.Process.Kill()
			foreground.Wait()
			_, _ = command(nil, "terminal", "host", "stop", "--state", crashDir, "--force")
		})
		deadline := time.Now().Add(5 * time.Second)
		var hello terminal.Response
		for {
			var err error
			hello, err = terminal.Call(context.Background(), crashDir, terminal.Request{Op: "hello"})
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("foreground test host not ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		launch := terminal.Launch{RequestID: "crash-start", TerminalID: "crash-terminal", Generation: 1, Definition: terminal.Definition{WorkspaceID: "crash-workspace", Executable: "/bin/sh", Args: []string{"-c", "printf x >> crash-once; exec sleep 30"}, Directory: base, Environment: map[string]string{"PATH": "/usr/bin:/bin"}, Columns: 80, Rows: 24}}
		if _, err := terminal.Call(context.Background(), crashDir, terminal.Request{Op: "ensure", Epoch: hello.Status.Epoch, Launch: &launch}); err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(5 * time.Second)
		for {
			b, _ := os.ReadFile(filepath.Join(base, "crash-once"))
			if string(b) == "x" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("crash fixture did not execute")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := foreground.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		foreground.Wait()
		out, err := command(nil, "terminal", "host", "start", "--state", crashDir)
		if err != nil {
			t.Fatalf("new epoch: %v: %s", err, out)
		}
		var recovered terminal.Status
		if err = json.Unmarshal(out, &recovered); err != nil {
			t.Fatal(err)
		}
		if recovered.Epoch == hello.Status.Epoch || len(recovered.Terminals) != 1 || recovered.Terminals[0].State != "lost" || recovered.Terminals[0].PID != 0 {
			t.Fatal("new host adopted a lost process")
		}
		response, err := terminal.Call(context.Background(), crashDir, terminal.Request{Op: "ensure", Epoch: recovered.Epoch, Launch: &launch})
		if err != nil || response.Info == nil || response.Info.State != "lost" {
			t.Fatal("old request was not retained as lost")
		}
		if b, _ := os.ReadFile(filepath.Join(base, "crash-once")); string(b) != "x" {
			t.Fatal("host crash automatically relaunched a saved definition")
		}
		launch.Generation = 2
		launch.RequestID = "explicit-after-crash"
		if _, err = terminal.Call(context.Background(), crashDir, terminal.Request{Op: "restart", Epoch: recovered.Epoch, Launch: &launch}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestNativeTerminalCLIRequiresExplicitNamespace(t *testing.T) {
	for _, args := range [][]string{{"host", "start"}, {"inventory"}, {"unknown", "--state", "relative"}} {
		var out bytes.Buffer
		if err := terminalCommand(context.Background(), args, bytes.NewReader(nil), &out); err == nil || out.Len() != 0 {
			t.Fatal("invalid explicit host invocation accepted")
		}
	}
}

func TestNativeHostStartupLockSurvivesInitiatorDeath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "host")
	parent := exec.Command(os.Args[0])
	parent.Env = append(os.Environ(), "ST_TEST_TERMINAL_PARENT=1", "ST_TEST_TERMINAL_STATE="+dir)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "release-start"), nil, 0600)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for {
			response, err := terminal.Call(ctx, dir, terminal.Request{Op: "hello"})
			if err == nil {
				_, _ = terminal.Call(ctx, dir, terminal.Request{Op: "shutdown", Epoch: response.Status.Epoch, Force: true})
				break
			}
			if ctx.Err() != nil {
				t.Error("helper host did not become available for cleanup")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		parent.Process.Kill()
		parent.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "child-ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host helper did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	parent.Wait()
	lock, err := os.OpenFile(filepath.Join(dir, "startup.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		t.Fatal("initiator death dropped startup lock while child was starting")
	}
	result := make(chan error, 1)
	go func() { _, err := terminal.StartHost(context.Background(), dir, os.Args[0]); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("contender did not wait: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = os.WriteFile(filepath.Join(dir, "release-start"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("contender did not reconnect to existing host")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "started-count")); string(b) != "x" {
		t.Fatal("initiator death launched another host")
	}
}
