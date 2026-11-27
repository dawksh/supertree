package stack

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

func requireUnicodeTrial(t *testing.T) {
	t.Helper()
	if os.Getenv("ST_STACK_UNICODE_TRIAL") != "1" {
		t.Skip("explicit upstream repair trial only; published candidate still has the recorded Unicode gap")
	}
}

type terminalImage struct {
	Cells       []uv.Cell
	Cursor      uv.Position
	Hidden, Alt bool
}

func imageOf(e *vt.Emulator) terminalImage {
	img := terminalImage{Cursor: e.CursorPosition(), Hidden: e.CursorHidden(), Alt: e.IsAltScreen()}
	for y := 0; y < e.Height(); y++ {
		for x := 0; x < e.Width(); x++ {
			img.Cells = append(img.Cells, *e.CellAt(x, y))
		}
	}
	return img
}

func TestUnicodeTrialEveryByteBoundary(t *testing.T) {
	requireUnicodeTrial(t)
	for _, data := range []string{
		"e\u0301X", "👩\u200d💻X", "🇮🇳X", "♥\ufe0fX", "🙂\ufe0eX", "a\u0301\u0308b",
		"1234567e\u0301X", "123456👩\u200d💻X", "1234567♥\ufe0fX",
		"1\r\n2\r\n3\r\n4\r\ne\u0301X", "\x1b[31me\u0301\x1b[0mX",
		"e\u0301\x1b[KX", "e\u0301\x1b[2;1HX", "\x1b[?1049he\u0301X\x1b[?1049lY",
		"e\u0301\x1b[3b", "\x1b*0\x8eq\u0301X",
	} {
		t.Run(data, func(t *testing.T) {
			e := vt.NewEmulator(8, 4)
			defer e.Close()
			_, _ = e.WriteString(data)
			want := imageOf(e)
			reset := func() { _, _ = e.WriteString("\x1bc"); e.ClearScrollback() }
			for split := 1; split < len(data); split++ {
				reset()
				_, _ = e.WriteString(data[:split])
				_, _ = e.WriteString(data[split:])
				if got := imageOf(e); !reflect.DeepEqual(got, want) {
					t.Fatalf("split %d changed cells/cursor vs whole write\ngot: %+v\nwant: %+v", split, got, want)
				}
			}
			reset()
			for i := range len(data) {
				_, _ = e.WriteString(data[i : i+1])
			}
			if !reflect.DeepEqual(imageOf(e), want) {
				t.Fatal("byte-at-a-time delivery changed screen/cursor")
			}
		})
	}
}

func TestUnicodeTrialImmediateDisplayAndHardBoundaries(t *testing.T) {
	requireUnicodeTrial(t)
	e := vt.NewEmulator(12, 4)
	defer e.Close()
	_, _ = e.WriteString("e")
	if e.CellAt(0, 0).Content != "e" {
		t.Fatal("repair deferred display of ordinary input")
	}
	_, _ = e.WriteString("\u0301")
	if e.CellAt(0, 0).Content != "e\u0301" {
		t.Fatal("continuation did not amend preceding cell")
	}
	e.Resize(14, 4)
	_, _ = e.WriteString("\u0308")
	if e.CellAt(0, 0).Content != "e\u0301" {
		t.Fatal("continuation undid resize")
	}
	_, _ = e.WriteString("\x1b[Hq")
	e.SetCell(0, 0, &uv.Cell{Content: "Z", Width: 1})
	_, _ = e.WriteString("\u0301")
	if e.CellAt(0, 0).Content != "Z" {
		t.Fatal("continuation undid external cell mutation")
	}
}

func TestUnicodeTrialLongFragmentedCluster(t *testing.T) {
	requireUnicodeTrial(t)
	e := vt.NewEmulator(80, 24)
	defer e.Close()
	_, _ = e.WriteString("a")
	start := time.Now()
	for i := 0; i < 2000; i++ {
		_, _ = e.WriteString("\u0301")
	}
	if want := "a" + strings.Repeat("\u0301", 2000); e.CellAt(0, 0).Content != want {
		t.Fatal("fragmented long cluster was lost")
	}
	t.Logf("2000 fragmented combining marks: %s; retain as a cost measurement, not a linear-time claim", time.Since(start))
}

func TestUnicodeTrialRealPTYFragments(t *testing.T) {
	requireUnicodeTrial(t)
	tm := startTest(t, childCommand("unicode"), 80, 24)
	var total uint64
	for _, chunk := range []string{"e", "\u0301", "👩", "\u200d", "💻", "X"} {
		total += uint64(len(chunk))
		waitFrame(t, tm, func(f Frame) bool { return f.OutputBytes >= total })
		inputText(t, tm, ">")
	}
	waitFrame(t, tm, func(f Frame) bool { return f.Exited })
	f := tm.Snapshot()
	if f.Cells[0].Text != "e\u0301" || f.Cells[1].Text != "👩\u200d💻" || f.Cells[3].Text != "X" {
		t.Fatalf("real PTY chunk delivery lost graphemes: %q", f.Text())
	}
}
