package terminal

import (
	"time"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/sys/unix"
)

// Notices coalesce paint work, never bytes entering the authoritative emulator.
func (p *process) notifyLocked() {
	for ch := range p.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (p *process) subscribe(info Info) (Screen, Info, chan struct{}, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disposed {
		return Screen{}, Info{}, nil, nil, failure("stale_generation", "terminal was replaced")
	}
	if len(p.subscribers) >= 32 {
		return Screen{}, Info{}, nil, nil, failure("resource_limit", "terminal subscriber limit reached")
	}
	ch := make(chan struct{}, 1)
	p.subscribers[ch] = true
	f, err := p.screenLocked(info, 0, p.vt.Height())
	if err != nil {
		delete(p.subscribers, ch)
		return Screen{}, Info{}, nil, nil, err
	}
	return f, p.infoLocked(info), ch, func() { p.mu.Lock(); delete(p.subscribers, ch); p.mu.Unlock() }, nil
}
func (p *process) capture(info Info) (Screen, Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disposed {
		return Screen{}, Info{}, failure("stale_generation", "terminal generation was replaced; reattach explicitly")
	}
	f, err := p.screenLocked(info, 0, p.vt.Height())
	return f, p.infoLocked(info), err
}

func validateInput(input *Input) error {
	if input == nil || input.Number == 0 || input.Lease == "" || len(input.Text) > 64<<10 || !utf8.ValidString(input.Text) {
		return failure("invalid_arguments", "invalid or oversized terminal input")
	}
	if input.Key != nil && (input.Paste || input.Text != "" || len(input.Key.Text) > 64<<10 || !utf8.ValidString(input.Key.Text)) {
		return failure("invalid_arguments", "input must contain one key or text/paste")
	}
	if input.Key == nil && input.Text == "" && !input.Paste {
		return failure("invalid_arguments", "terminal input is empty")
	}
	return nil
}
func (p *process) input(input *Input) error {
	if err := validateInput(input); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disposed {
		return failure("stale_generation", "terminal was replaced")
	}
	if p.exitCode != nil {
		return failure("terminal_exited", "terminal has exited; restart is an explicit action")
	}
	if p.inputError != "" {
		return failure("delivery_uncertain", "terminal input is unavailable; inspect before reconnecting")
	}
	p.writeBudget.Store(time.Now().Add(250 * time.Millisecond).UnixNano())
	defer p.writeBudget.Store(0)
	switch {
	case input.Key != nil:
		p.vt.SendKey(uv.KeyPressEvent(*input.Key))
	case input.Paste:
		p.vt.Paste(input.Text)
	default:
		p.vt.SendText(input.Text)
	}
	// io.Pipe's zero-byte write is a read rendezvous. The sole reader writes
	// each prior chunk to the PTY before reading again, so this barrier proves
	// kernel delivery without sending a sentinel byte to the child. On timeout
	// the reader closes the pipe; the error forbids automatic input replay.
	if _, err := p.vt.InputPipe().Write(nil); err != nil {
		return failure("delivery_uncertain", "PTY input delivery is uncertain; do not replay it")
	}
	return nil
}
func (p *process) resize(columns, rows int) (uint64, error) {
	if !sizeOK(columns, rows) {
		return 0, failure("invalid_arguments", "terminal dimensions are out of bounds")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disposed {
		return p.geometry, failure("stale_generation", "terminal was replaced")
	}
	if p.exitCode != nil {
		return p.geometry, failure("terminal_exited", "exited terminal retains its last geometry")
	}
	if p.vt.Width() == columns && p.vt.Height() == rows {
		return p.geometry, nil
	}
	raw, err := p.pty.SyscallConn()
	if err != nil {
		return p.geometry, failure("resize_failed", "terminal descriptor is unavailable")
	}
	var result error
	err = raw.Control(func(fd uintptr) {
		result = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(columns), Row: uint16(rows)})
	})
	if err != nil || result != nil {
		return p.geometry, failure("resize_failed", "PTY resize was not applied")
	}
	p.vt.Resize(columns, rows)
	p.geometry++
	p.sequence++
	p.notifyLocked()
	return p.geometry, nil
}
