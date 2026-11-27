package stack

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

func emulator(t *testing.T, w, h int) (*vt.Emulator, func() string) {
	t.Helper()
	e := vt.NewEmulator(w, h)
	e.SetScrollbackSize(ScrollbackLines)
	var input bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&input, e); close(done) }()
	closed := false
	finish := func() string {
		if !closed {
			_ = e.InputPipe().(io.Closer).Close()
			<-done
			_ = e.Close()
			closed = true
		}
		return input.String()
	}
	t.Cleanup(func() { finish() })
	return e, finish
}
func emulatorFrame(e *vt.Emulator) Frame {
	f := Frame{Width: e.Width(), Height: e.Height(), Cells: make([]Cell, e.Width()*e.Height())}
	for y := 0; y < f.Height; y++ {
		for x := 0; x < f.Width; x++ {
			if c := e.CellAt(x, y); c != nil {
				f.Cells[y*f.Width+x] = Cell{Text: c.Content, Width: c.Width, FG: rgb(c.Style.Fg), BG: rgb(c.Style.Bg), Attrs: c.Style.Attrs}
			}
		}
	}
	return f
}

func TestIndependentRectanglesContainChildEscapes(t *testing.T) {
	var frames []Frame
	for _, text := range []string{"LEFT", "MIDDLE", "RIGHT"} {
		e, _ := emulator(t, 19, 6)
		_, _ = e.WriteString("\x1b]0;ESCAPED-TITLE\x07\x1b]52;c;YWJj\x07\x1bPtmux;\x1b\x1b[2J\x1b\\\x1b[2J\x1b[H" + text)
		frames = append(frames, emulatorFrame(e))
	}
	var output bytes.Buffer
	screen := uv.NewTerminalScreen(&output, uv.Environ{"TERM=xterm-256color"})
	_ = screen.Resize(60, 7)
	_ = screen.EnterAltScreen()
	for y := 0; y < 7; y++ {
		for x := 0; x < 60; x++ {
			screen.SetCell(x, y, &uv.EmptyCell)
		}
	}
	Paint(screen, frames, Rectangles(60, 7, 3, 0, false))
	if err := screen.Render(); err != nil {
		t.Fatal(err)
	}
	if err := screen.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "ESCAPED-TITLE") || strings.Contains(output.String(), "]52;") || strings.Contains(output.String(), "tmux;") {
		t.Fatal("child effects escaped compositor")
	}
	outer, _ := emulator(t, 60, 7)
	_, _ = outer.Write(output.Bytes())
	for i, want := range []string{"LEFT", "MIDDLE", "RIGHT"} {
		var got strings.Builder
		for x := i * 20; x < i*20+len(want); x++ {
			got.WriteString(outer.CellAt(x, 1).Content)
		}
		if got.String() != want {
			t.Fatalf("pane %d: got %q, want %q", i, got.String(), want)
		}
	}
}

func TestVTUnicodeAlternateScreenStylesAndScrollRegion(t *testing.T) {
	e, _ := emulator(t, 16, 6)
	_, _ = e.WriteString("main\x1b[?1049halt\x1b[?1049l")
	if !strings.HasPrefix(e.String(), "main") || e.IsAltScreen() {
		t.Fatal("alternate screen failed to restore main screen")
	}
	_, _ = e.WriteString("\x1b[2J\x1b[H界e\u0301🙂\x1b[31mR\x1b[0m")
	if c := e.CellAt(0, 0); c.Content != "界" || c.Width != 2 {
		t.Fatalf("wide cell: %+v", c)
	}
	if c := e.CellAt(2, 0); c.Content != "e\u0301" || c.Width != 1 {
		t.Fatalf("combining cell: %+v", c)
	}
	if c := e.CellAt(3, 0); c.Content != "🙂" || c.Width != 2 {
		t.Fatalf("emoji cell: %+v", c)
	}
	if c := e.CellAt(5, 0); c.Content != "R" || c.Style.Fg == nil {
		t.Fatalf("SGR color: %+v", c)
	}
	_, _ = e.WriteString("\x1b[6;1Hbottom\x1b[2;5r\x1b[5;1Hone\r\ntwo\r\nthree")
	if e.CellAt(0, 5).Content != "b" {
		t.Fatal("scroll region affected excluded bottom row")
	}
	_, _ = e.WriteString("\x1b[r\x1b[6;1H")
	for i := 0; i < 600; i++ {
		_, _ = e.WriteString("bounded\r\n")
	}
	if e.ScrollbackLen() > ScrollbackLines {
		t.Fatal("scrollback exceeded bound")
	}
}

func TestMouseCoordinatesAndLegacyKeys(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		key        uv.KeyPressEvent
		want       string
	}{
		{"normal arrow", "", uv.KeyPressEvent{Code: uv.KeyUp}, "\x1b[A"},
		{"application arrow", "\x1b[?1h", uv.KeyPressEvent{Code: uv.KeyUp}, "\x1bOA"},
		{"meta", "", uv.KeyPressEvent{Code: 'x', Mod: uv.ModAlt}, "\x1bx"},
		{"interrupt", "", uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl}, "\x03"},
		{"suspend", "", uv.KeyPressEvent{Code: 'z', Mod: uv.ModCtrl}, "\x1a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, finish := emulator(t, 20, 10)
			_, _ = e.WriteString(tc.mode)
			e.SendKey(tc.key)
			if got := finish(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	e, finish := emulator(t, 20, 10)
	_, _ = e.WriteString("\x1b[?1000h\x1b[?1006h")
	e.SendMouse(uv.MouseClickEvent{X: 3, Y: 2, Button: uv.MouseLeft})
	if got := finish(); got != "\x1b[<0;4;3M" {
		t.Fatalf("pane-relative SGR mouse: %q", got)
	}
}

func TestOuterPasteIsOneEventIncludingShortcutText(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan uv.Event, 16)
	err := uv.NewTerminalReader(strings.NewReader("\x1b[200~one\n\x01d\x1b[201~"), "xterm-256color").StreamEvents(ctx, events)
	if err != nil {
		t.Fatal(err)
	}
	var pastes []string
	for len(events) > 0 {
		switch e := (<-events).(type) {
		case uv.PasteEvent:
			pastes = append(pastes, e.Content)
		case uv.KeyPressEvent:
			t.Fatalf("paste turned into a shortcut: %+v", e)
		}
	}
	if !reflect.DeepEqual(pastes, []string{"one\n\x01d"}) {
		t.Fatal(pastes)
	}
}

func TestEnhancedKeyboardIsNotAdvertised(t *testing.T) {
	e, finish := emulator(t, 20, 10)
	_, _ = e.WriteString("\x1b[?u")
	if got := finish(); got != "" {
		t.Fatalf("unsupported Kitty negotiation advertised: %q", got)
	}
	// This explicitly records the pinned candidate's limitation, rather than
	// treating outer Kitty decoding as child encoding support.
}

func TestClippedWideCharacterDoesNotCrossPane(t *testing.T) {
	e, _ := emulator(t, 4, 2)
	_, _ = e.WriteString("ab界")
	s := uv.NewScreen(5, 4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 5; x++ {
			s.SetCell(x, y, &uv.EmptyCell)
		}
	}
	Paint(s, []Frame{emulatorFrame(e)}, []Rect{{Width: 3, Height: 2}})
	if c := s.CellAt(3, 1); c.Content != " " {
		t.Fatalf("wide character escaped rectangle: %+v", c)
	}
}

func TestProtocolRejectsOversizedFrames(t *testing.T) {
	if err := readMessage(bytes.NewReader([]byte{255, 255, 255, 255}), &Request{}); err == nil {
		t.Fatal("oversized frame accepted")
	}
}
