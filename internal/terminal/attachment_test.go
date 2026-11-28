package terminal

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

func attachFixture(t *testing.T, script string) (string, Status, Launch, Info) {
	t.Helper()
	dir, status := testHost(t)
	l := testLaunch(dir, "attached", script)
	info := mustCall(t, dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &l}).Info
	return dir, status, l, *info
}
func nextSnapshot(t *testing.T, a *Attachment, predicate func(Snapshot) bool) Snapshot {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-a.Snapshots():
			if !ok {
				t.Fatal(a.Err())
			}
			if predicate(f) {
				return f
			}
		case <-a.Done():
			t.Fatal(a.Err())
		case <-timer.C:
			t.Fatal("snapshot deadline")
		}
	}
}
func attachWriter(t *testing.T, dir string, target Target) *Attachment {
	t.Helper()
	var a *Attachment
	await(t, func() bool {
		var err error
		a, err = DialAttachment(context.Background(), dir, target, false)
		if err == nil {
			return true
		}
		if coded := responseError(err).Error; coded.Code != "writer_busy" {
			t.Fatal(err)
		}
		return false
	})
	t.Cleanup(func() { a.Close() })
	return a
}

func TestAttachmentInputResizeReadOnlyAndReconnect(t *testing.T) {
	dir, status, l, info := attachFixture(t, `stty raw -echo; printf 'READY\r\n'; key=$(dd bs=1 count=1 2>/dev/null | od -An -tx1); printf 'KEY:%s\r\n' "$key"; stty sane -echo; printf 'LINES_READY\n'; while IFS= read -r line; do printf 'GOT:%s\n' "$line"; done`)
	target := Target{l.TerminalID, 1}
	a := attachWriter(t, dir, target)
	nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "READY") })
	key := uv.Key{Code: 'a', Mod: uv.ModCtrl}
	if err := a.SendInput(context.Background(), &key, "", false); err != nil {
		t.Fatal(err)
	}
	nextSnapshot(t, a, func(f Snapshot) bool {
		text := screenText(f.Screen)
		return strings.Contains(strings.Join(strings.Fields(text), " "), "KEY: 01") && strings.Contains(text, "LINES_READY")
	})
	if err := a.SendInput(context.Background(), nil, "界 é 👩‍💻\r", true); err != nil {
		t.Fatal(err)
	}
	nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "GOT:界 é 👩‍💻") })
	geometry, err := a.Resize(context.Background(), 90, 30)
	if err != nil {
		t.Fatal(err)
	}
	resized := nextSnapshot(t, a, func(f Snapshot) bool {
		return f.Screen.Geometry >= geometry && f.Screen.Columns == 90 && f.Screen.Height == 30
	})
	if len(resized.Screen.Cells) != 2700 {
		t.Fatal("partial snapshot published")
	}
	if extra, err := DialAttachment(context.Background(), dir, target, false); err == nil {
		extra.Close()
		t.Fatal("second writer silently took over")
	} else {
		assertCode(t, err, "writer_busy")
	}
	readOnly, err := DialAttachment(context.Background(), dir, target, true)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	assertCode(t, readOnly.SendInput(context.Background(), nil, "forbidden\r", true), "read_only")
	_, err = readOnly.Resize(context.Background(), 40, 10)
	assertCode(t, err, "read_only")
	nextSnapshot(t, readOnly, func(f Snapshot) bool { return f.Screen.Columns == 90 && f.Screen.Height == 30 })
	a.Close()
	a = attachWriter(t, dir, target)
	nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "GOT:界 é 👩‍💻") })
	if current := mustCall(t, dir, Request{Op: "inspect", Epoch: status.Epoch, Target: &target}).Info; current.PID != info.PID || current.State != "running" {
		t.Fatal("reconnect replaced terminal")
	}
	if err = a.SendInput(context.Background(), nil, "after-reconnect\r", true); err != nil {
		t.Fatal(err)
	}
	nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "GOT:after-reconnect") })
}

func TestVirtualTerminalCannotAddressOuterTerminalTransport(t *testing.T) {
	dir, status := testHost(t)
	l := testLaunch(dir, "environment", `for key in WEZTERM_UNIX_SOCKET KITTY_LISTEN_ON ITERM_SESSION_ID TMUX TERM_SESSION_ID; do eval 'value=${'"$key"'-}'; [ -z "$value" ] || printf 'OUTER_TRANSPORT_LEAK\n'; done; printf 'TERM:%s CUSTOM:%s\n' "$TERM" "$CUSTOM_VALUE"; sleep 10`)
	for _, key := range []string{"WEZTERM_UNIX_SOCKET", "KITTY_LISTEN_ON", "ITERM_SESSION_ID", "TMUX", "TERM_SESSION_ID"} {
		l.Definition.Environment[key] = "outer-fixture-transport"
	}
	l.Definition.Environment["CUSTOM_VALUE"] = "preserved"
	mustCall(t, dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &l})
	a := attachWriter(t, dir, Target{l.TerminalID, 1})
	f := nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "CUSTOM:preserved") })
	text := screenText(f.Screen)
	if strings.Contains(text, "OUTER_TRANSPORT_LEAK") || !strings.Contains(text, "TERM:xterm-256color") {
		t.Fatal("virtual child inherited outer terminal transport or lost its virtual terminal identity")
	}
}

func TestAttachmentRejectsDuplicateAndForeignInput(t *testing.T) {
	dir, status, l, _ := attachFixture(t, `stty -echo; printf 'READY\n'; while IFS= read -r line; do printf x >> received; printf 'ACK\n'; done`)
	conn, hello, err := dialHost(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := Request{Version: 1, Namespace: hello.Namespace, Epoch: status.Epoch, Op: "attach", Target: &Target{l.TerminalID, 1}}
	if err = writeFrame(conn, request); err != nil {
		t.Fatal(err)
	}
	var welcome Response
	if err = readFrame(conn, &welcome, MaxMessage); err != nil || welcome.Attached == nil {
		t.Fatal("attach handshake")
	}
	lease := welcome.Attached.Lease
	for {
		var packet Response
		if err = readFrame(conn, &packet, MaxMessage); err != nil {
			t.Fatal(err)
		}
		if packet.Event == "snapshot_end" {
			break
		}
	}
	// Wait until the child has disabled echo before sending canonical input.
	await(t, func() bool {
		return strings.Contains(screenText(*mustCall(t, dir, Request{Op: "screen", Epoch: status.Epoch, Target: &Target{l.TerminalID, 1}}).Screen), "READY")
	})
	send := func(number uint64, token, text string) Ack {
		t.Helper()
		req := Request{Version: 1, Namespace: hello.Namespace, Epoch: status.Epoch, Op: "input", Input: &Input{Number: number, Lease: token, Text: text}}
		if err = writeFrame(conn, req); err != nil {
			t.Fatal(err)
		}
		for {
			var packet Response
			if err = readFrame(conn, &packet, MaxMessage); err != nil {
				t.Fatal(err)
			}
			if packet.Ack != nil {
				return *packet.Ack
			}
		}
	}
	if ack := send(1, lease, "one\r"); ack.Error != nil {
		t.Fatal(ack.Error)
	}
	if ack := send(1, lease, "duplicate\r"); ack.Error == nil || ack.Error.Code != "stale_input" {
		t.Fatal("duplicate input was not rejected")
	}
	if ack := send(2, "foreign-lease", "foreign\r"); ack.Error == nil || ack.Error.Code != "stale_lease" {
		t.Fatal("foreign input lease accepted")
	}
	if ack := send(2, lease, "two\r"); ack.Error != nil {
		t.Fatal(ack.Error)
	}
	await(t, func() bool { b, _ := os.ReadFile(filepath.Join(dir, "received")); return string(b) == "xx" })
	_, err = Call(context.Background(), dir, Request{Op: "input", Epoch: status.Epoch, Input: &Input{Number: 3, Lease: lease, Text: "not-an-attachment\r"}})
	assertCode(t, err, "unknown_command")
}

func TestAttachmentIdleResnapshotAndBoundedLatestScreen(t *testing.T) {
	dir, _, l, _ := attachFixture(t, `printf 'IDLE_SCREEN\n'; sleep 30`)
	a := attachWriter(t, dir, Target{l.TerminalID, 1})
	first := nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "IDLE_SCREEN") })
	select {
	case <-a.Snapshots():
		t.Fatal("idle stream repainted without output")
	case <-time.After(100 * time.Millisecond):
	}
	if err := a.Resnapshot(); err != nil {
		t.Fatal(err)
	}
	refresh := nextSnapshot(t, a, func(f Snapshot) bool { return f.Screen.Sequence == first.Screen.Sequence })
	if len(refresh.Screen.Cells) != refresh.Screen.Columns*refresh.Screen.Height {
		t.Fatal("resnapshot is incomplete")
	}
	if cap(a.frames) != 1 {
		t.Fatal("screen queue is not bounded to the latest complete snapshot")
	}
}

func TestAttachmentReportsUncertainDeliveryWithoutRestartingChild(t *testing.T) {
	dir, status, l, info := attachFixture(t, `stty raw -echo; printf 'NOT_READING\r\n'; exec sleep 30`)
	a := attachWriter(t, dir, Target{l.TerminalID, 1})
	nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "NOT_READING") })
	err := a.SendInput(context.Background(), nil, strings.Repeat("x", 64<<10), true)
	assertCode(t, err, "delivery_uncertain")
	if current := mustCall(t, dir, Request{Op: "inspect", Epoch: status.Epoch, Target: &Target{l.TerminalID, 1}}).Info; current.PID != info.PID || current.State != "running" {
		t.Fatal("input failure killed or replaced program")
	}
}

func TestAttachmentDropsIncompleteSnapshotAndRequestsFreshOne(t *testing.T) {
	dir := privateTestDir(t)
	canonical, err := privateDir(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = privateDir(filepath.Dir(socketPath(canonical)), true); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath(canonical))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		var req Request
		if err = readFrame(conn, &req, MaxRequest); err != nil {
			serverDone <- err
			return
		}
		status := Status{Version: 1, Namespace: namespace(canonical), Epoch: "snapshot-fixture", Capabilities: []string{"screen-stream-v1"}}
		if err = writeFrame(conn, Response{Status: &status}); err != nil {
			serverDone <- err
			return
		}
		if err = readFrame(conn, &req, MaxRequest); err != nil {
			serverDone <- err
			return
		}
		info := Info{TerminalID: "fixture", Generation: 1, HostEpoch: status.Epoch, State: "running"}
		if err = writeFrame(conn, Response{Event: "attached", Attached: &Attached{Info: info, ReadOnly: true}}); err != nil {
			serverDone <- err
			return
		}
		frame := Screen{HostEpoch: status.Epoch, TerminalID: info.TerminalID, Generation: 1, Sequence: 1, Geometry: 1, Columns: 4, Height: 4, Rows: 4}
		_ = writeFrame(conn, Response{Event: "snapshot_begin", Screen: &frame, Info: &info})
		// End before all rows arrived; the client must never publish this frame.
		_ = writeFrame(conn, Response{Event: "snapshot_end", Screen: &frame})
		if err = readFrame(conn, &req, MaxRequest); err != nil {
			serverDone <- err
			return
		}
		if req.Op != "resnapshot" {
			serverDone <- failure("test", "client did not request resnapshot")
			return
		}
		frame.Sequence = 2
		frame.Cells = make([]Cell, 16)
		frame.Cells[0] = Cell{Text: "N", Width: 1}
		frame.Cells[1] = Cell{Text: "E", Width: 1}
		frame.Cells[2] = Cell{Text: "W", Width: 1}
		if err = sendSnapshot(func(r Response) error { return writeFrame(conn, r) }, frame, info); err != nil {
			serverDone <- err
			return
		}
		var ignored Request
		_ = readFrame(conn, &ignored, MaxRequest)
		serverDone <- nil
	}()
	a, err := DialAttachment(context.Background(), dir, Target{"fixture", 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	f := nextSnapshot(t, a, func(Snapshot) bool { return true })
	if f.Screen.Sequence != 2 || !strings.Contains(screenText(f.Screen), "NEW") {
		t.Fatal("partial/outdated snapshot was published")
	}
	a.Close()
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestSlowAttachmentCannotBlockAnotherTerminal(t *testing.T) {
	dir, status, noisy, _ := attachFixture(t, `while :; do printf 'NOISY_OUTPUT_123456789\n'; done`)
	quiet := testLaunch(dir, "quiet", `stty -echo; printf 'QUIET_READY\n'; while IFS= read -r line; do printf 'QUIET:%s\n' "$line"; done`)
	mustCall(t, dir, Request{Op: "ensure", Epoch: status.Epoch, Launch: &quiet})
	conn, hello, err := dialHost(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = writeFrame(conn, Request{Version: 1, Namespace: hello.Namespace, Epoch: status.Epoch, Op: "attach", Target: &Target{noisy.TerminalID, 1}}); err != nil {
		t.Fatal(err)
	}
	var welcome Response
	if err = readFrame(conn, &welcome, MaxMessage); err != nil || welcome.Attached == nil {
		t.Fatal("slow subscriber did not attach")
	}
	// Stop reading all screen data. Only this connection can time out.
	before := mustCall(t, dir, Request{Op: "screen", Epoch: status.Epoch, Target: &Target{noisy.TerminalID, 1}}).Screen.OutputBytes
	a := attachWriter(t, dir, Target{quiet.TerminalID, 1})
	nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "QUIET_READY") })
	if err = a.SendInput(context.Background(), nil, "responsive\r", false); err != nil {
		t.Fatal(err)
	}
	nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "QUIET:responsive") })
	await(t, func() bool {
		return mustCall(t, dir, Request{Op: "screen", Epoch: status.Epoch, Target: &Target{noisy.TerminalID, 1}}).Screen.OutputBytes > before
	})
	// The writer deadline drops the abandoned lease. The programs still run.
	time.Sleep(3300 * time.Millisecond)
	reconnected := attachWriter(t, dir, Target{noisy.TerminalID, 1})
	reconnected.Close()
}

func TestAttachmentUsesChildKeyboardAndPasteModes(t *testing.T) {
	dir, _, l, _ := attachFixture(t, `stty raw -echo; printf '\033[?1h\033[?2004hMODE_READY\r\n'; bytes=$(dd bs=1 count=18 2>/dev/null | od -An -tx1); printf '\r\nBYTES:%s\r\n' "$bytes"; exec sleep 30`)
	a := attachWriter(t, dir, Target{l.TerminalID, 1})
	nextSnapshot(t, a, func(f Snapshot) bool { return strings.Contains(screenText(f.Screen), "MODE_READY") })
	up := uv.Key{Code: uv.KeyUp}
	if err := a.SendInput(context.Background(), &up, "", false); err != nil {
		t.Fatal(err)
	}
	if err := a.SendInput(context.Background(), nil, "p", true); err != nil {
		t.Fatal(err)
	}
	newline := uv.Key{Code: 'j', Mod: uv.ModCtrl}
	enter := uv.Key{Code: uv.KeyEnter}
	if err := a.SendInput(context.Background(), &newline, "", false); err != nil {
		t.Fatal(err)
	}
	if err := a.SendInput(context.Background(), &enter, "", false); err != nil {
		t.Fatal(err)
	}
	nextSnapshot(t, a, func(f Snapshot) bool {
		text := strings.Join(strings.Fields(screenText(f.Screen)), " ")
		return strings.Contains(text, "1b 4f 41 1b 5b 32 30 30 7e 70 1b 5b 32 30 31 7e") && strings.Contains(text, "0a 0d")
	})
}
