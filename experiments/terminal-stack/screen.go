package stack

import (
	"image/color"
	"strings"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
)

// Wire cells carry data, never child-provided ANSI or outer-terminal effects.
type Cell struct {
	Text           string      `json:"t,omitempty"`
	Width          int         `json:"w,omitempty"`
	FG             *color.RGBA `json:"f,omitempty"`
	BG             *color.RGBA `json:"b,omitempty"`
	UnderlineColor *color.RGBA `json:"uc,omitempty"`
	Attrs          uint8       `json:"a,omitempty"`
	Underline      uint8       `json:"u,omitempty"`
}

type Frame struct {
	PID                       int
	Width, Height             int
	Cells                     []Cell
	CursorX, CursorY          int
	CursorHidden              bool
	CursorShape               int
	CursorBlink               bool
	AltScreen                 bool
	Exited                    bool
	ExitCode                  int
	Scrollback                int
	OutputBytes, DroppedInput uint64
	InputError                string
}

func rgb(c color.Color) *color.RGBA {
	if c == nil {
		return nil
	}
	r, g, b, a := c.RGBA()
	return &color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func (t *Terminal) Snapshot() Frame {
	t.mu.Lock()
	defer t.mu.Unlock()
	x, y := t.vt.CursorPosition().X, t.vt.CursorPosition().Y
	f := Frame{PID: t.cmd.Process.Pid, Width: t.vt.Width(), Height: t.vt.Height(),
		CursorX: x, CursorY: y, CursorHidden: t.vt.CursorHidden(), AltScreen: t.vt.IsAltScreen(),
		ExitCode: t.exitCode, Scrollback: t.vt.ScrollbackLen(), OutputBytes: t.bytes.Load(),
		DroppedInput: t.dropped.Load(), InputError: t.inputError}
	shape, blink := t.vt.CursorStyle()
	f.CursorShape, f.CursorBlink = int(shape), blink
	select {
	case <-t.exited:
		f.Exited = true
	default:
	}
	f.Cells = make([]Cell, f.Width*f.Height)
	for y := 0; y < f.Height; y++ {
		for x := 0; x < f.Width; x++ {
			if c := t.vt.CellAt(x, y); c != nil {
				f.Cells[y*f.Width+x] = Cell{Text: c.Content, Width: c.Width,
					FG: rgb(c.Style.Fg), BG: rgb(c.Style.Bg), UnderlineColor: rgb(c.Style.UnderlineColor),
					Attrs: c.Style.Attrs, Underline: uint8(c.Style.Underline)}
			}
		}
	}
	return f
}

func (f Frame) Text() string {
	var b strings.Builder
	for y := 0; y < f.Height; y++ {
		for x := 0; x < f.Width; x++ {
			c := f.Cells[y*f.Width+x]
			if c.Text == "" {
				if c.Width != 0 {
					b.WriteByte(' ')
				}
				continue
			}
			b.WriteString(c.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

type Rect struct{ X, Y, Width, Height int }

// Rectangles are intentionally simple. Narrow clients show only the focused
// terminal; this proof does not implement the product's persistent split model.
func Rectangles(width, height, count, focus int, solo bool) []Rect {
	r := make([]Rect, count)
	if width < 2 || height < 3 || count == 0 {
		return r
	}
	if solo || width < count*20 {
		r[focus] = Rect{Width: min(width, 240), Height: min(height-1, 100)}
		return r
	}
	for i := range r {
		left, right := i*width/count, (i+1)*width/count
		r[i] = Rect{X: left, Width: min(right-left-1, 240), Height: min(height-1, 100)}
	}
	return r
}

func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// Paint places cells, clips wide glyphs at pane boundaries and drops links.
// Child clear-screen/title/clipboard/mode bytes never reach the outer terminal.
func Paint(screen uv.Screen, frames []Frame, rects []Rect) {
	for i, f := range frames {
		r := rects[i]
		for y := 0; y < min(r.Height, f.Height); y++ {
			for x := 0; x < min(r.Width, f.Width); x++ {
				c := f.Cells[y*f.Width+x]
				if c.Width == 0 {
					continue
				}
				if c.Width > r.Width-x {
					continue
				}
				out := uv.Cell{Content: cleanText(c.Text), Width: c.Width, Style: uv.Style{Attrs: c.Attrs, Underline: uv.Underline(c.Underline)}}
				if c.FG != nil {
					out.Style.Fg = *c.FG
				}
				if c.BG != nil {
					out.Style.Bg = *c.BG
				}
				if c.UnderlineColor != nil {
					out.Style.UnderlineColor = *c.UnderlineColor
				}
				screen.SetCell(r.X+x, r.Y+y+1, &out)
			}
		}
	}
}
