package stack

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/term"
)

// Attach is a keyboard-only compositor proof. Direct Meta shortcuts select,
// hide, move and reveal existing terminals. Ctrl-a is a child key by default;
// legacyPrefix explicitly opts into the old experiment's fallback sequences.
func Attach(ctx context.Context, dir string, legacyPrefix bool) error {
	client, err := Dial(dir)
	if err != nil {
		return err
	}
	defer client.Close()
	initial, err := client.Call(Request{Op: "snapshot"})
	if err != nil {
		return err
	}
	if len(initial.Frames) == 0 {
		return fmt.Errorf("experiment has no terminals")
	}
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(os.Stdin.Fd()), state)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, err := uv.NewCancelReader(os.Stdin)
	if err != nil {
		return err
	}
	events := make(chan uv.Event, 16)
	inputErrors := make(chan error, 1)
	screen := uv.NewTerminalScreen(os.Stdout, os.Environ())
	defer func() { _ = screen.Reset(); _ = screen.Flush() }()
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		inputErrors <- uv.NewTerminalReader(reader, os.Getenv("TERM")).StreamEvents(ctx, events)
	}()
	defer func() {
		cancel()
		reader.Cancel()
		// The decoder can be flushing buffered events when cancellation arrives.
		// Drain until it and its internal read goroutine finish; Close racing
		// an epoll reader's Read is unsafe even if Cancel was already called.
		for {
			select {
			case <-inputDone:
				_ = reader.Close()
				return
			case <-events:
			}
		}
	}()
	_ = screen.EnterAltScreen()
	_ = screen.EnableBracketedPaste()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	tick := time.NewTicker(time.Second / 30)
	defer tick.Stop()
	views := newViewState(len(initial.Frames))
	prefix := false
	width, height := 0, 0
	var rects []Rect
	resize := func() error {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil {
			return err
		}
		width, height = w, h
		if w < 2 || h < 3 {
			return fmt.Errorf("experiment needs at least 2 columns and 3 rows")
		}
		if err := screen.Resize(w, h); err != nil {
			return err
		}
		rects = views.rectangles(w, h)
		for i, r := range rects {
			if r.Width > 0 {
				if _, err := client.Call(Request{Op: "resize", Index: i, Width: r.Width, Height: r.Height}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := resize(); err != nil {
		return err
	}
	paint := func() error {
		response, err := client.Call(Request{Op: "snapshot"})
		if err != nil {
			return err
		}
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				screen.SetCell(x, y, &uv.EmptyCell)
			}
		}
		Paint(screen, response.Frames, rects)
		order := []int{}
		for _, id := range views.visible() {
			order = append(order, id+1)
		}
		header := fmt.Sprintf("terminal proof | views %v focus %d | M-1..3 show/focus  M-0 hide  M-H/L move  M-z solo  M-q detach", order, views.focus+1)
		if len(order) == 0 {
			header = "terminal proof | all views hidden | M-1..3 restore | M-q detach"
		}
		if legacyPrefix && prefix {
			header = "legacy prefix pending | " + header
		}
		for x, r := range header {
			if x >= width {
				break
			}
			screen.SetCell(x, 0, &uv.Cell{Content: string(r), Width: 1})
		}
		if views.focus < 0 {
			_ = screen.HideCursor()
		} else {
			f, r := response.Frames[views.focus], rects[views.focus]
			if f.CursorHidden || f.CursorX >= r.Width || f.CursorY >= r.Height {
				_ = screen.HideCursor()
			} else {
				_ = screen.SetCursorPosition(r.X+f.CursorX, 1+f.CursorY)
				_ = screen.SetCursorStyle(uv.CursorShape(f.CursorShape), f.CursorBlink)
				_ = screen.ShowCursor()
			}
		}
		if err := screen.Render(); err != nil {
			return err
		}
		return screen.Flush()
	}
	if err := paint(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-inputErrors:
			return err
		case <-winch:
			if err := resize(); err != nil {
				return err
			}
		case <-tick.C:
			if err := paint(); err != nil {
				return err
			}
		case event := <-events:
			req := Request{Op: "input"}
			switch e := event.(type) {
			case uv.KeyPressEvent:
				if legacyPrefix && prefix {
					prefix = false
					if !e.MatchString("ctrl+a") {
						if e.Code == 'd' {
							return nil
						}
						_, detach := workspaceKey(e, views)
						if detach {
							return nil
						}
						if err := resize(); err != nil {
							return err
						}
						continue
					}
				} else if legacyPrefix && e.MatchString("ctrl+a") {
					prefix = true
					continue
				} else if e.Mod&uv.ModAlt != 0 && e.Mod & ^(uv.ModAlt|uv.ModShift) == 0 {
					if handled, detach := workspaceKey(e, views); handled {
						if detach {
							return nil
						}
						if err := resize(); err != nil {
							return err
						}
						continue
					}
				}
				key := e.Key()
				req.Key = &key
			case uv.PasteEvent:
				req.Text = e.Content
				req.Paste = true // Never route pasted prefix/shortcut text.
			default:
				continue // Mouse and unknown/enhanced events are not advertised.
			}
			if views.focus < 0 {
				continue
			}
			req.Index = views.focus
			if _, err := client.Call(req); err != nil {
				return err
			}
		}
	}
}

func workspaceKey(e uv.KeyPressEvent, views *viewState) (handled, detach bool) {
	if e.Mod & ^(uv.ModAlt|uv.ModShift) != 0 {
		return false, false
	}
	if e.Mod&uv.ModShift != 0 && e.Code != 'h' && e.Code != 'H' && e.Code != 'l' && e.Code != 'L' && e.Code != 'q' && e.Code != 'Q' {
		return false, false
	}
	switch {
	case e.Code == 'q' || e.Code == 'Q':
		return true, true
	case e.Code >= '1' && e.Code <= '9':
		views.selectTerminal(int(e.Code - '1'))
	case e.Code == '0':
		views.hide()
	case e.Code == 'H' || e.Code == 'h' && e.Mod&uv.ModShift != 0:
		views.move(-1)
	case e.Code == 'L' || e.Code == 'l' && e.Mod&uv.ModShift != 0:
		views.move(1)
	case e.Code == 'h' || e.Code == 'p':
		views.next(-1)
	case e.Code == 'l' || e.Code == 'n':
		views.next(1)
	case e.Code == 'z':
		views.solo = !views.solo
	default:
		return false, false
	}
	return true, false
}
