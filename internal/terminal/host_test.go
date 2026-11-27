package terminal

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dawksh/supertree/internal/command"
)

func privateTestDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "host")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testHost(t *testing.T) (string, Status) {
	t.Helper()
	dir := privateTestDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() { done <- ServeReady(ctx, dir, func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("host not ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(20 * time.Second):
			t.Error("host cleanup stalled")
		}
	})
	r := mustCall(t, dir, Request{Op: "hello"})
	return dir, *r.Status
}
func mustCall(t *testing.T, dir string, req Request) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := Call(ctx, dir, req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func testLaunch(dir, id, script string) Launch {
	return Launch{RequestID: "req_" + id, TerminalID: id, Generation: 1, Definition: Definition{WorkspaceID: "ws_test", Executable: "/bin/sh", Args: []string{"-c", script}, Directory: dir, Environment: map[string]string{"PATH": "/usr/bin:/bin"}, Columns: 80, Rows: 24}}
}
func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func screenText(f Screen) string {
	var b strings.Builder
	for i, c := range f.Cells {
		b.WriteString(c.Text)
		if (i+1)%f.Columns == 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *command.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error %v, want code %s", err, code)
	}
}

func TestHostConcurrentLaunchLostAcknowledgementAndIndependentLifetime(t *testing.T) {
	dir, status := testHost(t)
	marker := filepath.Join(dir, "launch-count")
	l := testLaunch(dir, "term_one", `printf x >> launch-count; printf 'READY\n'; while :; do printf 'tick\n'; sleep .05; done`)
	canonical, _ := privateDir(dir, false)
	c, err := net.Dial("unix", socketPath(canonical))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = writeFrame(c, Request{Version: 1, Namespace: status.Namespace, Op: "hello"}); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err = readFrame(c, &response, MaxMessage); err != nil {
		t.Fatal(err)
	}
	if err = writeFrame(c, Request{Version: 1, Namespace: status.Namespace, Op: "ensure", Epoch: status.Epoch, Launch: &l}); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { b, _ := os.ReadFile(marker); return string(b) == "x" })
	// The initiating client dies without consuming its launch acknowledgement.
	c.Close()
	const count = 16
	var wg sync.WaitGroup
	results := make(chan Response, count)
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := Call(context.Background(), dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &l})
			if e != nil {
				errs <- e
			} else {
				results <- r
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	pid := 0
	for r := range results {
		if r.Info == nil || r.Info.PID == 0 {
			t.Fatal("missing launch inventory")
		}
		if pid != 0 && pid != r.Info.PID {
			t.Fatal("duplicate program after lost acknowledgement")
		}
		pid = r.Info.PID
	}
	b, _ := os.ReadFile(marker)
	if string(b) != "x" {
		t.Fatal("launch ran more than once")
	}
	target := Target{l.TerminalID, 1}
	first := mustCall(t, dir, Request{Op: "screen", Epoch: status.Epoch, Target: &target}).Screen
	await(t, func() bool {
		return mustCall(t, dir, Request{Op: "screen", Epoch: status.Epoch, Target: &target}).Screen.OutputBytes > first.OutputBytes
	})
	if got := mustCall(t, dir, Request{Op: "hello"}).Status; got.PID != status.PID || got.Terminals[0].PID != pid {
		t.Fatal("reconnect replaced host or terminal")
	}
	l.Definition.Args = []string{"-c", "exit 0"}
	_, err = Call(context.Background(), dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &l})
	assertCode(t, err, "conflict")
	_, err = Call(context.Background(), dir, Request{Op: "stop", Epoch: "old", Target: &target})
	assertCode(t, err, "stale_epoch")
	wrong := Target{l.TerminalID, 2}
	_, err = Call(context.Background(), dir, Request{Op: "stop", Epoch: status.Epoch, Target: &wrong})
	assertCode(t, err, "stale_generation")
	_, err = Call(context.Background(), dir, Request{Op: "shutdown", Epoch: status.Epoch})
	assertCode(t, err, "conflict")
}

func TestHostExitScreenFailureAndExplicitRestart(t *testing.T) {
	dir, status := testHost(t)
	l := testLaunch(dir, "term_exit", `printf '界 é 👩‍💻\n'; exit 7`)
	info := mustCall(t, dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &l}).Info
	target := Target{l.TerminalID, 1}
	await(t, func() bool {
		info = mustCall(t, dir, Request{Op: "inspect", Epoch: status.Epoch, Target: &target}).Info
		return info.State == "exited"
	})
	if info.ExitCode == nil || *info.ExitCode != 7 {
		t.Fatalf("exit outcome: %#v", info)
	}
	await(t, func() bool {
		return strings.Contains(screenText(*mustCall(t, dir, Request{Op: "screen", Epoch: status.Epoch, Target: &target}).Screen), "界 é 👩‍💻")
	})
	l.Generation = 2
	l.RequestID = "restart_1"
	result := mustCall(t, dir, Request{Op: "restart", Epoch: status.Epoch, Launch: &l}).Info
	if result.Generation != 2 {
		t.Fatal("restart did not create a new generation")
	}
	_, err := Call(context.Background(), dir, Request{Op: "inspect", Epoch: status.Epoch, Target: &target})
	assertCode(t, err, "stale_generation")
	failed := testLaunch(dir, "term_bad", "ignored")
	failed.Definition.Executable = filepath.Join(dir, "missing-secret-do-not-print")
	result = mustCall(t, dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &failed}).Info
	if result.State != "failed-to-start" || result.PID != 0 || strings.Contains(result.Error, "secret") {
		t.Fatal("unsafe failed spawn outcome")
	}
	s, err := os.Stat(filepath.Join(dir, "receipts.json"))
	if err != nil || s.Mode().Perm() != 0600 {
		t.Fatal("receipt permissions")
	}
}

func TestHostNoAdoptionOrRelaunchAfterEpochLoss(t *testing.T) {
	dir := privateTestDir(t)
	l := testLaunch(dir, "lost_terminal", "printf unintended > should-not-exist")
	unrelated := exec.Command("/bin/sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { unrelated.Process.Kill(); unrelated.Wait() }()
	r := receipt{Launch: l, Fingerprint: fingerprint(l), Info: Info{TerminalID: l.TerminalID, Generation: 1, RequestID: l.RequestID, WorkspaceID: l.Definition.WorkspaceID, HostEpoch: "old_epoch", State: "starting", PID: unrelated.Process.Pid}}
	if err := atomicJournal(dir, journal{Version: 1, Epoch: "old_epoch", Receipts: map[string]receipt{l.RequestID: r}}); err != nil {
		t.Fatal(err)
	}
	h, err := loadHost(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	response, _ := h.handle(context.Background(), Request{Op: "ensure", Epoch: h.journal.Epoch, Launch: &l})
	if response.Error != nil || response.Info.State != "lost" || response.Info.PID != 0 {
		t.Fatalf("historical intent was adopted: %#v", response)
	}
	target := Target{l.TerminalID, 1}
	response, _ = h.handle(context.Background(), Request{Op: "stop", Epoch: h.journal.Epoch, Target: &target, Force: true})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	if err = unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("historical PID was signaled")
	}
	if _, err = os.Stat(filepath.Join(dir, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("historical intent executed")
	}
	l.Generation = 2
	l.RequestID = "explicit_restart"
	response, _ = h.handle(context.Background(), Request{Op: "restart", Epoch: h.journal.Epoch, Launch: &l})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	await(t, func() bool { _, e := os.Stat(filepath.Join(dir, "should-not-exist")); return e == nil })
}

func TestHostJournalFailuresDoNotDuplicateOrOrphan(t *testing.T) {
	dir := privateTestDir(t)
	h, err := loadHost(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	l := testLaunch(dir, "journal_term", `printf x >> once; sleep 30`)
	h.save = func(string, journal) error { return os.ErrPermission }
	_, err = h.ensure(context.Background(), l, false)
	assertCode(t, err, "receipt_uncertain")
	if _, err = os.Stat(filepath.Join(dir, "once")); !os.IsNotExist(err) {
		t.Fatal("spawned before durable intent")
	}
	calls := 0
	h.save = func(dir string, j journal) error {
		calls++
		if calls == 2 {
			return os.ErrPermission
		}
		return atomicJournal(dir, j)
	}
	info, err := h.ensure(context.Background(), l, false)
	assertCode(t, err, "receipt_uncertain")
	if info.PID == 0 {
		t.Fatal("uncertain outcome lost live program")
	}
	h.save = atomicJournal
	retry, err := h.ensure(context.Background(), l, false)
	if err != nil || retry.PID != info.PID {
		t.Fatal("receipt failure caused duplicate launch")
	}
	await(t, func() bool { b, _ := os.ReadFile(filepath.Join(dir, "once")); return string(b) == "x" })
}

func TestHostPrivateEndpointAndMalformedFrames(t *testing.T) {
	dir, status := testHost(t)
	canonical, _ := privateDir(dir, false)
	if _, err := listen(canonical); err == nil {
		t.Fatal("second host acquired namespace")
	}
	for _, req := range []Request{{Version: 99, Namespace: status.Namespace, Op: "hello"}, {Version: 1, Namespace: "wrong", Op: "hello"}, {Version: 1, Namespace: status.Namespace, Op: "shutdown", Epoch: status.Epoch, Force: true}} {
		c, err := net.Dial("unix", socketPath(canonical))
		if err != nil {
			t.Fatal(err)
		}
		_ = writeFrame(c, req)
		var r Response
		err = readFrame(c, &r, MaxMessage)
		c.Close()
		if err != nil || r.Error == nil || r.Error.Code != "protocol_mismatch" {
			t.Fatal("invalid handshake accepted")
		}
	}
	c, err := net.Dial("unix", socketPath(canonical))
	if err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxRequest+1)
	_, _ = c.Write(header[:])
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err = c.Read(b[:]); err == nil {
		t.Fatal("oversized frame accepted")
	}
	c.Close()
	if mustCall(t, dir, Request{Op: "hello"}).Status.Epoch != status.Epoch {
		t.Fatal("malformed input replaced host")
	}
	unsafe := filepath.Join(t.TempDir(), "unsafe")
	if err = os.Mkdir(unsafe, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = privateDir(unsafe, false); err == nil {
		t.Fatal("permissive namespace accepted")
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err = os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err = privateDir(link, false); err == nil {
		t.Fatal("symlink namespace accepted")
	}
	path := filepath.Join(t.TempDir(), "receipt-link")
	if err = os.Symlink(filepath.Join(dir, "receipts.json"), path); err != nil {
		t.Fatal(err)
	}
	if _, err = openPrivate(path, os.O_RDONLY); err == nil {
		t.Fatal("symlink state file accepted")
	}
}

func TestHostBoundsAndSecretPresentation(t *testing.T) {
	dir, status := testHost(t)
	for i := 0; i < MaxTerminals; i++ {
		l := testLaunch(dir, fmt.Sprintf("terminal_%d", i), "exit 0")
		l.Definition.Environment["PRIVATE_TOKEN"] = "fixture-secret-value"
		result := mustCall(t, dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &l})
		b, _ := json.Marshal(result)
		if strings.Contains(string(b), "fixture-secret-value") {
			t.Fatal("secret leaked in public outcome")
		}
	}
	l := testLaunch(dir, "overflow", "printf unintended > limit-marker")
	_, err := Call(context.Background(), dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &l})
	assertCode(t, err, "resource_limit")
	if _, err = os.Stat(filepath.Join(dir, "limit-marker")); !os.IsNotExist(err) {
		t.Fatal("limit admitted another program")
	}
	l.Definition.Columns = MaxColumns + 1
	_, err = Call(context.Background(), dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &l})
	assertCode(t, err, "invalid_arguments")
}

func TestRestartPreservesPriorOutcomeWhileExitWatcherWaits(t *testing.T) {
	h, err := loadHost(privateTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	h.mu.Lock()
	defer h.mu.Unlock()
	original := testLaunch(h.dir, "fast-exit", "exit 7")
	if _, err = h.ensure(context.Background(), original, false); err != nil {
		t.Fatal(err)
	}
	p := h.processes[original.TerminalID]
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture did not exit")
	}
	// Its asynchronous receipt watcher cannot acquire h.mu until this test
	// completes, matching a restart arriving before that watcher gets scheduled.
	next := original
	next.Generation = 2
	next.RequestID = "next-request"
	if _, err = h.ensure(context.Background(), next, true); err != nil {
		t.Fatal(err)
	}
	info, err := h.ensure(context.Background(), original, false)
	if err != nil || info.State != "exited" || info.ExitCode == nil || *info.ExitCode != 7 {
		t.Fatal("superseded request returned a stale running outcome")
	}
}

func TestFailedRestartIntentPreservesExitedScreen(t *testing.T) {
	h, err := loadHost(privateTestDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	h.mu.Lock()
	defer h.mu.Unlock()
	original := testLaunch(h.dir, "retained-screen", `printf 'RETAINED_SCREEN\n'; exit 0`)
	if _, err = h.ensure(context.Background(), original, false); err != nil {
		t.Fatal(err)
	}
	p := h.processes[original.TerminalID]
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture did not exit")
	}
	next := original
	next.Generation = 2
	next.RequestID = "cannot-persist"
	h.save = func(dir string, j journal) error {
		if _, ok := j.Receipts[next.RequestID]; ok {
			return os.ErrPermission
		}
		return atomicJournal(dir, j)
	}
	_, err = h.ensure(context.Background(), next, true)
	h.save = atomicJournal
	assertCode(t, err, "receipt_uncertain")
	if h.processes[original.TerminalID] != p || h.current[original.TerminalID] != original.RequestID {
		t.Fatal("failed intent discarded previous terminal")
	}
	screen, err := p.screen(h.journal.Receipts[original.RequestID].Info, 0, 16)
	if err != nil || !strings.Contains(screenText(screen), "RETAINED_SCREEN") {
		t.Fatal("failed restart discarded exited screen")
	}
}
