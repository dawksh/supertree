package terminal

import (
	"context"
	"net"
	"time"
)

// serveAttachment uses one frame writer and one request reader. No socket write
// holds the host/emulator locks; slow screens coalesce to a fresh full snapshot.
func (h *host) serveAttachment(parent context.Context, conn net.Conn, req Request) {
	h.mu.Lock()
	if h.stopping || req.Epoch != h.journal.Epoch {
		h.mu.Unlock()
		_ = writeFrame(conn, responseError(failure("stale_epoch", "host changed or is stopping")))
		return
	}
	r, p, err := h.target(req.Target)
	if err == nil && p == nil {
		err = failure("terminal_lost", "this host cannot attach a historical/lost PTY")
	}
	lease := ""
	if err == nil && !req.ReadOnly {
		if h.writers[r.Info.TerminalID] != "" {
			err = failure("writer_busy", "terminal has a writer; detach it before requesting another")
		} else {
			lease = token()
			h.writers[r.Info.TerminalID] = lease
		}
	}
	if err != nil {
		h.mu.Unlock()
		_ = writeFrame(conn, responseError(err))
		return
	}
	initial, info, notices, unsubscribe, err := p.subscribe(r.Info)
	if err != nil {
		if lease != "" {
			delete(h.writers, r.Info.TerminalID)
		}
		h.mu.Unlock()
		_ = writeFrame(conn, responseError(err))
		return
	}
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	_ = conn.SetDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	control := make(chan Response, 16)
	readerDone := make(chan struct{})
	go h.readAttachment(ctx, conn, req, r.Info, p, lease, control, readerDone)
	defer func() {
		cancel()
		conn.Close()
		<-readerDone
		unsubscribe()
		h.mu.Lock()
		if lease != "" && h.writers[r.Info.TerminalID] == lease {
			delete(h.writers, r.Info.TerminalID)
		}
		h.mu.Unlock()
	}()
	send := func(response Response) error {
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		return writeFrame(conn, response)
	}
	if send(Response{Event: "attached", Attached: &Attached{Info: info, Lease: lease, ReadOnly: req.ReadOnly}}) != nil {
		return
	}
	if sendSnapshot(send, initial, info) != nil {
		return
	}
	lastPaint := time.Now()
	lastSequence := initial.Sequence
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var paint <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-control:
			if !ok {
				return
			}
			if message.Event == "resnapshot" {
				frame, current, err := p.capture(r.Info)
				if err != nil {
					_ = send(responseError(err))
					return
				}
				if sendSnapshot(send, frame, current) != nil {
					return
				}
				lastPaint = time.Now()
				lastSequence = frame.Sequence
			} else if send(message) != nil {
				return
			}
		case <-notices:
			// A paint timer never delays acknowledgements. Idle attachments have
			// no paint tick; notices only coalesce output that already occurred.
			if paint == nil {
				timer.Reset(max(0, time.Until(lastPaint.Add(time.Second/60))))
				paint = timer.C
			}
		case <-paint:
			paint = nil
			frame, current, err := p.capture(r.Info)
			if err != nil {
				_ = send(responseError(err))
				return
			}
			if frame.Sequence != lastSequence {
				if sendSnapshot(send, frame, current) != nil {
					return
				}
				lastSequence = frame.Sequence
				lastPaint = time.Now()
			}
		}
	}
}

func sendSnapshot(send func(Response) error, frame Screen, info Info) error {
	meta := frame
	meta.Cells = nil
	meta.Row = 0
	meta.Rows = frame.Height
	if err := send(Response{Event: "snapshot_begin", Screen: &meta, Info: &info}); err != nil {
		return err
	}
	for row := 0; row < frame.Height; row += 16 {
		chunk := frame
		chunk.Row = row
		chunk.Rows = min(16, frame.Height-row)
		chunk.Cells = frame.Cells[row*frame.Columns : (row+chunk.Rows)*frame.Columns]
		if err := send(Response{Event: "snapshot_chunk", Screen: &chunk}); err != nil {
			return err
		}
	}
	return send(Response{Event: "snapshot_end", Screen: &meta})
}

func (h *host) checkWriter(info Info, p *process, lease string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping || h.journal.Epoch != info.HostEpoch {
		return failure("stale_epoch", "host is stopping or changed epoch")
	}
	if lease == "" || h.writers[info.TerminalID] != lease {
		return failure("stale_lease", "attachment does not own terminal input/geometry")
	}
	_, current, err := h.target(&Target{TerminalID: info.TerminalID, Generation: info.Generation})
	if err != nil {
		return err
	}
	if current != p {
		return failure("stale_generation", "terminal was replaced")
	}
	return nil
}

func (h *host) readAttachment(ctx context.Context, conn net.Conn, attached Request, info Info, p *process, lease string, control chan<- Response, done chan<- struct{}) {
	defer close(done)
	defer close(control)
	var last uint64
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		var req Request
		if readFrame(conn, &req, MaxRequest) != nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if req.Version != ProtocolVersion || req.Namespace != attached.Namespace || req.Epoch != info.HostEpoch || req.Launch != nil || req.Target != nil {
			return
		}
		var response Response
		switch req.Op {
		case "ping":
			response.Event = "pong"
		case "resnapshot":
			response.Event = "resnapshot"
		case "input", "resize":
			ack := Ack{}
			var err error
			if req.Op == "input" {
				err = validateInput(req.Input)
				if req.Input != nil {
					ack.Number = req.Input.Number
					if req.Input.Lease != lease {
						err = failure("stale_lease", "input lease differs from this attachment")
					}
				}
				if req.Resize != nil {
					err = failure("invalid_arguments", "input and resize are separate operations")
				}
			} else {
				if req.Resize == nil || !sizeOK(req.Resize.Columns, req.Resize.Rows) {
					err = failure("invalid_arguments", "invalid terminal resize")
				}
				if req.Resize != nil {
					ack.Number = req.Resize.Number
					if req.Resize.Lease != lease {
						err = failure("stale_lease", "resize lease differs from this attachment")
					}
				}
				if req.Input != nil {
					err = failure("invalid_arguments", "input and resize are separate operations")
				}
			}
			if err == nil {
				err = h.checkWriter(info, p, lease)
			}
			if err == nil && (ack.Number == 0 || last == ^uint64(0) || ack.Number != last+1) {
				err = failure("stale_input", "input numbers must increase exactly once within their lease")
			}
			if err == nil {
				last = ack.Number
				if req.Op == "input" {
					err = p.input(req.Input)
				} else {
					ack.Geometry, err = p.resize(req.Resize.Columns, req.Resize.Rows)
				}
			}
			if err != nil {
				ack.Error = responseError(err).Error
			}
			response = Response{Event: "ack", Ack: &ack}
		default:
			return
		}
		select {
		case control <- response:
		case <-ctx.Done():
			return
		default:
			return
		}
	}
}
