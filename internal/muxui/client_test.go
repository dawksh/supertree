package muxui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/dawksh/supertree/internal/terminal"
)

type fragmented struct {
	r    io.Reader
	size int
}

func (r fragmented) Read(b []byte) (int, error) { return r.r.Read(b[:min(len(b), r.size)]) }

func TestPasteGuardPreservesFragmentsAndDrainsOversize(t *testing.T) {
	for _, size := range []int{1, 5, 4096} {
		payload := "界 é 👩‍💻\n\x01\x1bq"
		encoded := "\x1b[200~" + payload + "\x1b[201~"
		data, err := io.ReadAll(&pasteReader{r: fragmented{strings.NewReader(encoded), size}})
		if err != nil || string(data) != encoded {
			t.Fatal("paste guard changed valid input")
		}
		encoded = "\x1b[200~" + strings.Repeat("x", 2<<20) + "\x1b[201~"
		source := strings.NewReader(encoded)
		data, err = io.ReadAll(&pasteReader{r: fragmented{source, size}})
		// The decoder sees at most the start marker plus bounded payload/end
		// lookahead; the oversized payload never receives its completion marker.
		if !errors.Is(err, errPasteLimit) || len(data) > (64<<10)+12 || source.Len() != 0 {
			t.Fatalf("oversize paste was retained/left queued: captured=%d remaining=%d error=%v", len(data), source.Len(), err)
		}
	}
	encoded := "\x1b[200~" + strings.Repeat("x", 64<<10) + "\x1b[201~"
	if data, err := io.ReadAll(&pasteReader{r: strings.NewReader(encoded)}); err != nil || string(data) != encoded {
		t.Fatal("exact-limit paste rejected")
	}
}

func TestPastedDetachTextNeverBecomesWorkspaceKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan uv.Event, 16)
	content := "literal \x1bq\n\x01"
	err := uv.NewTerminalReader(&pasteReader{r: strings.NewReader("\x1b[200~" + content + "\x1b[201~")}, "xterm-256color").StreamEvents(ctx, events)
	if err != nil {
		t.Fatal(err)
	}
	pastes := 0
	for len(events) > 0 {
		switch e := (<-events).(type) {
		case uv.PasteEvent:
			pastes++
			if e.Content != content {
				t.Fatalf("paste content changed: %q", e.Content)
			}
		case uv.KeyPressEvent:
			t.Fatal("paste became a workspace key")
		}
	}
	if pastes != 1 {
		t.Fatal("expected one paste event")
	}
	if detachKey(uv.KeyPressEvent{Code: 'a', Mod: uv.ModCtrl}) || detachKey(uv.KeyPressEvent{Code: 'q', Mod: uv.ModCtrl}) || !detachKey(uv.KeyPressEvent{Code: 'q', Mod: uv.ModAlt}) {
		t.Fatal("native detach stole child control keys")
	}
}

func TestPaintClipsWideCellsAndBlocksControlEffects(t *testing.T) {
	screen := uv.NewScreen(4, 2)
	frame := terminal.Screen{Columns: 5, Height: 2, Cells: make([]terminal.Cell, 10)}
	frame.Cells[0] = terminal.Cell{Text: "A", Width: 1}
	frame.Cells[3] = terminal.Cell{Text: "界", Width: 2}
	frame.Cells[5] = terminal.Cell{Text: "\x1b]52;c;secret\a", Width: 1}
	paintScreen(screen, frame, 4, 2)
	if got := screen.CellAt(3, 0); got.Content != " " {
		t.Fatal("wide cell crossed visible edge")
	}
	if got := screen.CellAt(0, 1); strings.ContainsAny(got.Content, "\x1b\a") {
		t.Fatal("control bytes reached renderer")
	}
	var output bytes.Buffer
	terminalScreen := uv.NewTerminalScreen(&output, uv.Environ{"TERM=xterm-256color"})
	_ = terminalScreen.Resize(4, 2)
	paintScreen(terminalScreen, frame, 4, 2)
	_ = terminalScreen.Render()
	_ = terminalScreen.Flush()
	if strings.Contains(output.String(), "\x1b]52;") {
		t.Fatal("clipboard control escaped cell rendering")
	}
}
