package terminal

import (
	"image/color"
	"strings"
	"unicode"
)

type Cell struct {
	Text           string      `json:"t,omitempty"`
	Width          int         `json:"w,omitempty"`
	FG             *color.RGBA `json:"f,omitempty"`
	BG             *color.RGBA `json:"b,omitempty"`
	UnderlineColor *color.RGBA `json:"uc,omitempty"`
	Attrs          uint8       `json:"a,omitempty"`
	Underline      uint8       `json:"u,omitempty"`
}
type Screen struct {
	HostEpoch         string `json:"host_epoch"`
	TerminalID        string `json:"terminal_id"`
	Generation        int64  `json:"generation"`
	Sequence          uint64 `json:"sequence"`
	Geometry          uint64 `json:"geometry"`
	Columns           int    `json:"columns"`
	Height            int    `json:"height"`
	Row               int    `json:"row"`
	Rows              int    `json:"rows"`
	Cells             []Cell `json:"cells"`
	CursorX           int    `json:"cursor_x"`
	CursorY           int    `json:"cursor_y"`
	CursorHidden      bool   `json:"cursor_hidden"`
	CursorShape       int    `json:"cursor_shape"`
	CursorBlink       bool   `json:"cursor_blink"`
	AltScreen         bool   `json:"alt_screen"`
	OutputBytes       uint64 `json:"output_bytes"`
	GraphemeOverflows uint64 `json:"grapheme_overflows"`
	InputError        string `json:"input_error,omitempty"`
}

func rgb(c color.Color) *color.RGBA {
	if c == nil {
		return nil
	}
	r, g, b, a := c.RGBA()
	return &color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

func (p *process) screen(info Info, row, rows int) (Screen, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if rows == 0 {
		rows = 16
	}
	if rows > 16 {
		return Screen{}, failure("invalid_arguments", "screen inspection accepts at most 16 rows")
	}
	return p.screenLocked(info, row, rows)
}
func (p *process) screenLocked(info Info, row, rows int) (Screen, error) {
	if row < 0 || row >= p.vt.Height() || rows < 1 || rows > MaxRows {
		return Screen{}, failure("invalid_arguments", "screen inspection accepts 1..16 rows at a valid offset")
	}
	rows = min(rows, p.vt.Height()-row)
	f := Screen{HostEpoch: info.HostEpoch, TerminalID: info.TerminalID, Generation: info.Generation, Sequence: p.sequence, Geometry: p.geometry, Columns: p.vt.Width(), Height: p.vt.Height(), Row: row, Rows: rows, CursorX: p.vt.CursorPosition().X, CursorY: p.vt.CursorPosition().Y, CursorHidden: p.vt.CursorHidden(), AltScreen: p.vt.IsAltScreen(), OutputBytes: p.bytes.Load(), GraphemeOverflows: p.vt.GraphemeOverflowCount(), InputError: p.inputError}
	shape, blink := p.vt.CursorStyle()
	f.CursorShape, f.CursorBlink = int(shape), blink
	f.Cells = make([]Cell, f.Columns*rows)
	for y := row; y < row+rows; y++ {
		for x := 0; x < f.Columns; x++ {
			if c := p.vt.CellAt(x, y); c != nil {
				text := strings.Map(func(r rune) rune {
					if unicode.IsControl(r) {
						return -1
					}
					return r
				}, c.Content)
				f.Cells[(y-row)*f.Columns+x] = Cell{Text: text, Width: c.Width, FG: rgb(c.Style.Fg), BG: rgb(c.Style.Bg), UnderlineColor: rgb(c.Style.UnderlineColor), Attrs: c.Style.Attrs, Underline: uint8(c.Style.Underline)}
			}
		}
	}
	return f, nil
}
