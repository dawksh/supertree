// Package muxui is the native single-terminal client. It owns outer-terminal
// input/output; the independent host owns the child, modes and authoritative grid.
package muxui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/terminal"
	"golang.org/x/term"
)

func Attach(ctx context.Context, dir string, target terminal.Target, readOnly bool, input, output *os.File) error {
	if !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(output.Fd())) {
		return fmt.Errorf("native attach requires terminal input and output")
	}
	attachment, err := terminal.DialAttachment(ctx, dir, target, readOnly)
	if err != nil {
		return err
	}
	defer attachment.Close()
	state, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(input.Fd()), state)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, err := uv.NewCancelReader(input)
	if err != nil {
		return err
	}
	events := make(chan uv.Event, 16)
	inputErrors := make(chan error, 1)
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		inputErrors <- uv.NewTerminalReader(&pasteReader{r: reader}, os.Getenv("TERM")).StreamEvents(ctx, events)
	}()
	defer func() {
		cancel()
		reader.Cancel()
		for {
			select {
			case <-inputDone:
				_ = reader.Close()
				return
			case <-events:
			}
		}
	}()
	screen := uv.NewTerminalScreen(output, os.Environ())
	defer func() { _ = screen.Reset(); _ = screen.Flush() }()
	if err = screen.EnterAltScreen(); err != nil {
		return err
	}
	if err = screen.EnableBracketedPaste(); err != nil {
		return err
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	width, height := 0, 0
	var desiredGeometry uint64
	var latest *terminal.Snapshot
	resize := func() error {
		w, h, err := term.GetSize(int(output.Fd()))
		if err != nil {
			return err
		}
		width, height = w, h
		if err = screen.Resize(max(1, w), max(1, h)); err != nil {
			return err
		}
		if w >= 2 && h >= 2 && !readOnly && attachment.Attached.Info.State != "exited" && (latest == nil || latest.Info.State != "exited") {
			geometry, err := attachment.Resize(ctx, min(w, terminal.MaxColumns), min(h, terminal.MaxRows))
			if err != nil {
				var coded *command.Error
				if errors.As(err, &coded) && coded.Code == "terminal_exited" {
					return attachment.Resnapshot()
				}
				return err
			}
			desiredGeometry = geometry
		}
		return nil
	}
	paint := func() error {
		if latest == nil {
			return nil
		}
		if latest.Screen.Geometry < desiredGeometry {
			return nil
		}
		paintScreen(screen, latest.Screen, width, height)
		if latest.Info.State == "exited" && height > 0 {
			label := "exited | M-q detach"
			if latest.Info.Signal != 0 {
				label = fmt.Sprintf("exited (signal %d) | M-q detach", latest.Info.Signal)
			} else if latest.Info.ExitCode != nil {
				label = fmt.Sprintf("exited (%d) | M-q detach", *latest.Info.ExitCode)
			}
			for x := 0; x < width; x++ {
				cell := uv.EmptyCell
				if x < len(label) {
					cell = uv.Cell{Content: string(label[x]), Width: 1}
				}
				screen.SetCell(x, height-1, &cell)
			}
			_ = screen.HideCursor()
		}
		if err := screen.Render(); err != nil {
			return err
		}
		return screen.Flush()
	}
	if err = resize(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-attachment.Done():
			if ctx.Err() != nil {
				return nil
			}
			return attachment.Err()
		case err := <-inputErrors:
			if ctx.Err() != nil {
				return nil
			}
			return err
		case <-winch:
			if err = resize(); err != nil {
				return err
			}
			if err = paint(); err != nil {
				return err
			}
		case snapshot, ok := <-attachment.Snapshots():
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return attachment.Err()
			}
			latest = &snapshot
			if err = paint(); err != nil {
				return err
			}
		case event := <-events:
			switch event := event.(type) {
			case uv.KeyPressEvent:
				if detachKey(event) {
					return nil
				}
				if readOnly || latest != nil && latest.Info.State == "exited" {
					continue
				}
				key := event.Key()
				if err = attachment.SendInput(ctx, &key, "", false); err != nil {
					return err
				}
			case uv.PasteEvent:
				if readOnly || latest != nil && latest.Info.State == "exited" {
					continue
				}
				if err = attachment.SendInput(ctx, nil, event.Content, true); err != nil {
					return err
				}
			}
		}
	}
}

func detachKey(event uv.KeyPressEvent) bool {
	return event.Mod&uv.ModAlt != 0 && event.Mod&^(uv.ModAlt|uv.ModShift) == 0 && (event.Code == 'q' || event.Code == 'Q')
}

func paintScreen(screen uv.Screen, frame terminal.Screen, width, height int) {
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			screen.SetCell(x, y, &uv.EmptyCell)
		}
	}
	for y := 0; y < min(height, frame.Height); y++ {
		for x := 0; x < min(width, frame.Columns); x++ {
			cell := frame.Cells[y*frame.Columns+x]
			if cell.Width == 0 || cell.Width > width-x {
				continue
			}
			content := strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}
				return r
			}, cell.Text)
			// A wire cell is one grapheme, never an arbitrary string that can
			// print across the next cell/pane even after controls are removed.
			if !(len(content) == 1 && content[0] >= 0x20 && content[0] < 0x7f && cell.Width == 1) {
				cluster, width := ansi.FirstGraphemeCluster(content, ansi.GraphemeWidth)
				if len(cluster) != len(content) || width != cell.Width {
					continue
				}
			}
			out := uv.Cell{Content: content, Width: cell.Width, Style: uv.Style{Attrs: cell.Attrs, Underline: uv.Underline(cell.Underline)}}
			if cell.FG != nil {
				out.Style.Fg = *cell.FG
			}
			if cell.BG != nil {
				out.Style.Bg = *cell.BG
			}
			if cell.UnderlineColor != nil {
				out.Style.UnderlineColor = *cell.UnderlineColor
			}
			screen.SetCell(x, y, &out)
		}
	}
	if terminalScreen, ok := screen.(*uv.TerminalScreen); ok {
		if frame.CursorHidden || frame.CursorX < 0 || frame.CursorY < 0 || frame.CursorX >= width || frame.CursorY >= height {
			_ = terminalScreen.HideCursor()
		} else {
			_ = terminalScreen.SetCursorPosition(frame.CursorX, frame.CursorY)
			_ = terminalScreen.SetCursorStyle(uv.CursorShape(frame.CursorShape), frame.CursorBlink)
			_ = terminalScreen.ShowCursor()
		}
	}
}
