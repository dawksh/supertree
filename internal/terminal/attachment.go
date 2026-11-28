package terminal

import (
	"context"
	"io"
	"net"
	"slices"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

type Snapshot struct {
	Screen Screen
	Info   Info
}

// Attachment has one reader, one serialized mutation stream, and one latest
// complete screen. Close releases its writer lease, never the PTY/program.
type Attachment struct {
	Attached               Attached
	conn                   net.Conn
	namespace, epoch       string
	ctx                    context.Context
	cancel                 context.CancelFunc
	writeMu, callMu, errMu sync.Mutex
	next                   uint64
	err                    error
	frames                 chan Snapshot
	acks                   chan Ack
	done, heartbeatDone    chan struct{}
}

func DialAttachment(ctx context.Context, dir string, target Target, readOnly bool) (*Attachment, error) {
	conn, status, err := dialHost(ctx, dir)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	if !slices.Contains(status.Capabilities, "screen-stream-v1") {
		return nil, failure("protocol_mismatch", "host does not support attachment; keep its programs and use a compatible client")
	}
	request := Request{Version: ProtocolVersion, Namespace: status.Namespace, Epoch: status.Epoch, Op: "attach", Target: &target, ReadOnly: readOnly}
	if err = writeFrame(conn, request); err != nil {
		return nil, err
	}
	var welcome Response
	if err = readFrame(conn, &welcome, MaxMessage); err != nil {
		return nil, err
	}
	if welcome.Error != nil {
		return nil, welcome.Error
	}
	if welcome.Event != "attached" || welcome.Attached == nil || welcome.Attached.Info.TerminalID != target.TerminalID || welcome.Attached.Info.Generation != target.Generation || welcome.Attached.Info.HostEpoch != status.Epoch || welcome.Attached.ReadOnly != readOnly || (!readOnly && welcome.Attached.Lease == "") {
		return nil, failure("protocol_mismatch", "invalid attachment identity/lease")
	}
	lifetime, cancel := context.WithCancel(ctx)
	s := &Attachment{Attached: *welcome.Attached, conn: conn, namespace: status.Namespace, epoch: status.Epoch, ctx: lifetime, cancel: cancel, frames: make(chan Snapshot, 1), acks: make(chan Ack, 1), done: make(chan struct{}), heartbeatDone: make(chan struct{})}
	_ = conn.SetDeadline(time.Time{})
	go s.read()
	go s.heartbeat()
	ok = true
	return s, nil
}
func (a *Attachment) Snapshots() <-chan Snapshot { return a.frames }
func (a *Attachment) Done() <-chan struct{}      { return a.done }
func (a *Attachment) Err() error {
	a.errMu.Lock()
	defer a.errMu.Unlock()
	if a.err == nil {
		return io.EOF
	}
	return a.err
}
func (a *Attachment) fail(err error) {
	a.errMu.Lock()
	if a.err == nil {
		a.err = err
	}
	a.errMu.Unlock()
	a.cancel()
	a.conn.Close()
}
func (a *Attachment) Close() error {
	a.cancel()
	a.conn.Close()
	<-a.done
	<-a.heartbeatDone
	return nil
}

func (a *Attachment) write(req Request) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if err := a.ctx.Err(); err != nil {
		return err
	}
	req.Version, req.Namespace, req.Epoch = ProtocolVersion, a.namespace, a.epoch
	_ = a.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	return writeFrame(a.conn, req)
}
func (a *Attachment) heartbeat() {
	defer close(a.heartbeatDone)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if err := a.write(Request{Op: "ping"}); err != nil {
				a.fail(failure("connection_lost", "attachment connection failed; unacknowledged input must not be replayed"))
				return
			}
		}
	}
}

func (a *Attachment) SendInput(ctx context.Context, key *uv.Key, text string, paste bool) error {
	if a.Attached.ReadOnly {
		return failure("read_only", "attachment does not own terminal input")
	}
	input := &Input{Lease: a.Attached.Lease, Number: 1, Key: key, Text: text, Paste: paste}
	if err := validateInput(input); err != nil {
		return err
	}
	_, err := a.mutate(ctx, Request{Op: "input", Input: input})
	return err
}
func (a *Attachment) Resize(ctx context.Context, columns, rows int) (uint64, error) {
	if a.Attached.ReadOnly {
		return 0, failure("read_only", "attachment does not own terminal geometry")
	}
	if !sizeOK(columns, rows) {
		return 0, failure("invalid_arguments", "terminal dimensions are out of bounds")
	}
	ack, err := a.mutate(ctx, Request{Op: "resize", Resize: &Resize{Lease: a.Attached.Lease, Columns: columns, Rows: rows}})
	return ack.Geometry, err
}
func (a *Attachment) Resnapshot() error { return a.write(Request{Op: "resnapshot"}) }
func (a *Attachment) mutate(ctx context.Context, req Request) (Ack, error) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Ack{}, err
	}
	if a.next == ^uint64(0) {
		return Ack{}, failure("resource_limit", "attachment input counter exhausted")
	}
	a.next++
	number := a.next
	if req.Input != nil {
		req.Input.Number = number
	} else {
		req.Resize.Number = number
	}
	operation, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(operation, func() {
		a.fail(failure("delivery_uncertain", "input acknowledgement was lost; inspect without replaying input"))
	})
	defer stop()
	if err := a.write(req); err != nil {
		a.fail(failure("delivery_uncertain", "terminal input delivery is uncertain; do not replay it"))
		return Ack{}, a.Err()
	}
	select {
	case ack := <-a.acks:
		if ack.Number != number {
			a.fail(failure("protocol_mismatch", "input acknowledgement number differs"))
			return Ack{}, a.Err()
		}
		if ack.Error != nil {
			return ack, ack.Error
		}
		return ack, nil
	case <-a.done:
		return Ack{}, failure("delivery_uncertain", "attachment closed before input acknowledgement; do not replay it")
	case <-operation.Done():
		return Ack{}, failure("delivery_uncertain", "input acknowledgement was lost; inspect without replaying input")
	}
}

func (a *Attachment) read() {
	defer close(a.done)
	defer close(a.frames)
	stop := context.AfterFunc(a.ctx, func() { a.conn.Close() })
	defer stop()
	var pending *Snapshot
	row := 0
	var last uint64
	recoveries := 0
	identity := func(f *Screen) bool {
		return f != nil && f.HostEpoch == a.epoch && f.TerminalID == a.Attached.Info.TerminalID && f.Generation == a.Attached.Info.Generation && sizeOK(f.Columns, f.Height) && f.Sequence > 0 && f.Geometry > 0
	}
	resnapshot := func() bool {
		pending = nil
		row = 0
		recoveries++
		if recoveries > 3 {
			a.fail(failure("protocol_mismatch", "screen snapshots repeatedly arrived incomplete"))
			return false
		}
		if err := a.Resnapshot(); err != nil {
			a.fail(err)
			return false
		}
		return true
	}
	for {
		_ = a.conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		var packet Response
		if err := readFrame(a.conn, &packet, MaxMessage); err != nil {
			if a.ctx.Err() != nil {
				a.fail(a.ctx.Err())
			} else {
				a.fail(failure("connection_lost", "attachment disconnected; inspect the same terminal without replaying input"))
			}
			return
		}
		if packet.Error != nil {
			a.fail(packet.Error)
			return
		}
		switch packet.Event {
		case "pong":
		case "ack":
			if packet.Ack == nil || packet.Ack.Number == 0 {
				a.fail(failure("protocol_mismatch", "invalid input acknowledgement"))
				return
			}
			select {
			case a.acks <- *packet.Ack:
			default:
				a.fail(failure("protocol_mismatch", "unexpected extra input acknowledgement"))
				return
			}
		case "snapshot_begin":
			f := packet.Screen
			if !identity(f) || packet.Info == nil || packet.Info.TerminalID != f.TerminalID || packet.Info.Generation != f.Generation || packet.Info.HostEpoch != f.HostEpoch || f.Row != 0 || f.Rows != f.Height || len(f.Cells) != 0 {
				a.fail(failure("protocol_mismatch", "snapshot identity or dimensions differ"))
				return
			}
			copy := *f
			copy.Cells = make([]Cell, f.Columns*f.Height)
			pending = &Snapshot{Screen: copy, Info: *packet.Info}
			row = 0
		case "snapshot_chunk":
			f := packet.Screen
			if !identity(f) {
				a.fail(failure("protocol_mismatch", "screen chunk identity differs"))
				return
			}
			if pending == nil {
				continue
			}
			base := pending.Screen
			if f.Sequence != base.Sequence || f.Geometry != base.Geometry || f.Columns != base.Columns || f.Height != base.Height || f.Row != row || f.Rows < 1 || f.Rows > 16 || row+f.Rows > f.Height || len(f.Cells) != f.Columns*f.Rows {
				if !resnapshot() {
					return
				}
				continue
			}
			for _, cell := range f.Cells {
				if len(cell.Text) > 256 || cell.Width < 0 || cell.Width > 2 {
					a.fail(failure("protocol_mismatch", "screen cell exceeds supported bounds"))
					return
				}
			}
			copy(pending.Screen.Cells[row*f.Columns:], f.Cells)
			row += f.Rows
		case "snapshot_end":
			f := packet.Screen
			if !identity(f) {
				a.fail(failure("protocol_mismatch", "screen end identity differs"))
				return
			}
			if pending == nil {
				continue
			}
			base := pending.Screen
			if row != base.Height || f.Sequence != base.Sequence || f.Geometry != base.Geometry || f.Columns != base.Columns || f.Height != base.Height {
				if !resnapshot() {
					return
				}
				continue
			}
			if base.Sequence >= last {
				select {
				case a.frames <- *pending:
				default:
					select {
					case <-a.frames:
					default:
					}
					select {
					case a.frames <- *pending:
					default:
					}
				}
				last = base.Sequence
			}
			pending = nil
			row = 0
			recoveries = 0
		default:
			a.fail(failure("protocol_mismatch", "unknown attachment stream event"))
			return
		}
	}
}
