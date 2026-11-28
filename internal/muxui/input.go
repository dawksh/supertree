package muxui

import (
	"errors"
	"io"
)

var errPasteLimit = errors.New("paste exceeds 64 KiB; it was discarded before terminal input")

// Ultraviolet assembles a paste before emitting its event. Bound that assembly
// before its decoder, including delimiters split across reads. Oversized pastes
// drain through their end marker so their tail cannot become shell commands
// when the client restores the outer terminal.
type pasteReader struct {
	r                 io.Reader
	start, end, count int
	paste, discard    bool
}

func (r *pasteReader) Read(b []byte) (int, error) {
	const start = "\x1b[200~"
	const end = "\x1b[201~"
	for {
		n, err := r.r.Read(b)
		discardChunk := r.discard
		for _, c := range b[:n] {
			if !r.paste {
				if c == start[r.start] {
					r.start++
				} else if c == start[0] {
					r.start = 1
				} else {
					r.start = 0
				}
				if r.start == len(start) {
					r.start = 0
					r.paste = true
					r.count = 0
					r.end = 0
				}
				continue
			}
			r.count++
			if c == end[r.end] {
				r.end++
			} else if c == end[0] {
				r.end = 1
			} else {
				r.end = 0
			}
			if r.end == len(end) {
				oversized := r.count-len(end) > 64<<10
				r.paste = false
				r.end = 0
				if r.discard || oversized {
					return 0, errPasteLimit
				}
			} else if r.count > (64<<10)+len(end) {
				r.discard = true
				discardChunk = true
			}
		}
		if err != nil {
			if r.discard {
				return 0, errPasteLimit
			}
			return n, err
		}
		if !discardChunk {
			return n, nil
		}
	}
}
