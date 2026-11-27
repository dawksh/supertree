package stack

import (
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
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
		"\u0600AX", "\u0600\u0600AX", "क्\u200dषX", "각X",
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
	for i := 0; i < 100; i++ {
		_, _ = e.WriteString("\u0301")
	}
	if want := "a" + strings.Repeat("\u0301", 100); e.CellAt(0, 0).Content != want {
		t.Fatal("fragmented long cluster was lost")
	}
	t.Logf("100 fragmented combining marks below the limit: %s", time.Since(start))
}

func TestUnicodeTrialResourceBoundIndependentOfChunking(t *testing.T) {
	requireUnicodeTrial(t)
	const limit = 256 // Documented display-cluster resource policy.
	for _, data := range []string{
		"a" + strings.Repeat("\u0301", 4000) + "AFTER-LIMIT",
		strings.Repeat("👩\u200d", 1000) + "💻AFTER-LIMIT",
		strings.Repeat("\u0600", 1000) + "AFTER-LIMIT",
	} {
		e := vt.NewEmulator(80, 24)
		_, _ = e.WriteString(data)
		want := imageOf(e)
		stats, ok := any(e).(interface{ GraphemeOverflowCount() uint64 })
		if !ok || stats.GraphemeOverflowCount() == 0 {
			t.Fatal("repair did not report forced resource boundaries")
		}
		count := stats.GraphemeOverflowCount()
		for _, cell := range want.Cells {
			if len(cell.Content) > limit {
				t.Fatalf("cell retained %d bytes beyond the limit", len(cell.Content))
			}
		}
		for _, chunk := range []int{1, 7, 255, 256, 1024} {
			reset := vt.NewEmulator(80, 24)
			for offset := 0; offset < len(data); offset += chunk {
				_, _ = reset.WriteString(data[offset:min(offset+chunk, len(data))])
			}
			if !reflect.DeepEqual(imageOf(reset), want) {
				t.Fatalf("resource policy depends on chunk size %d", chunk)
			}
			if any(reset).(interface{ GraphemeOverflowCount() uint64 }).GraphemeOverflowCount() != count {
				t.Fatal("overflow accounting depends on chunk size")
			}
			_ = reset.Close()
		}
		_ = e.Close()
	}
}

func TestUnicodeTrialHostileClusterCost(t *testing.T) {
	requireUnicodeTrial(t)
	for _, count := range []int{10_000, 100_000} {
		e := vt.NewEmulator(80, 24)
		_, _ = e.WriteString("a")
		start := time.Now()
		for range count {
			_, _ = e.WriteString("\u0301")
		}
		elapsed := time.Since(start)
		for _, cell := range imageOf(e).Cells {
			if len(cell.Content) > 256 {
				t.Fatal("unbounded cell content")
			}
		}
		_, _ = e.WriteString("\r\nAFTER-HOSTILE")
		var text strings.Builder
		for _, cell := range imageOf(e).Cells {
			text.WriteString(cell.Content)
		}
		if !strings.Contains(text.String(), "AFTER-HOSTILE") {
			t.Fatal("hostile cluster prevented subsequent normal output")
		}
		t.Logf("%d separately written marks: %s; per-rune segmentation input is capped at 260 bytes", count, elapsed)
		_ = e.Close()
	}
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

func TestUnicodeTrialHostilePTYReportsOverflow(t *testing.T) {
	requireUnicodeTrial(t)
	tm := startTest(t, childCommand("hostile-unicode"), 80, 24)
	f := waitFrame(t, tm, func(f Frame) bool { return f.Exited && strings.Contains(f.Text(), "AFTER-HOSTILE-PTY") })
	if f.GraphemeOverflows == 0 {
		t.Fatal("real PTY overflow was not exposed in inspection")
	}
	for _, cell := range f.Cells {
		if len(cell.Text) > 256 {
			t.Fatal("real PTY retained unbounded cluster data")
		}
	}
}

func TestUnicodeTrialMixedTextMatchesSegmentationOracle(t *testing.T) {
	requireUnicodeTrial(t)
	rng := rand.New(rand.NewSource(1))
	alphabet := []rune("ab界e\u0301\u0308\u0600👩\u200d💻🇮🇳♥\ufe0f\ufe0e각क्\u200dष")
	for sample := 0; sample < 300; sample++ {
		var text strings.Builder
		for range 40 {
			text.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		data := text.String()
		want := map[int]string{}
		x := 0
		for rest := data; len(rest) > 0; {
			cluster, width := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
			if len(cluster) == 0 {
				t.Fatal("oracle made no progress")
			}
			want[x] = cluster
			x += width
			rest = rest[len(cluster):]
		}
		for _, chunk := range []int{1, 7, len(data)} {
			e := vt.NewEmulator(240, 2)
			for offset := 0; offset < len(data); offset += chunk {
				_, _ = e.WriteString(data[offset:min(offset+chunk, len(data))])
			}
			if e.CursorPosition().X != x {
				t.Fatalf("sample %d chunk %d: cursor %d, want %d", sample, chunk, e.CursorPosition().X, x)
			}
			for column, content := range want {
				if e.CellAt(column, 0).Content != content {
					t.Fatalf("sample %d chunk %d column %d: got %q want %q", sample, chunk, column, e.CellAt(column, 0).Content, content)
				}
			}
			_ = e.Close()
		}
	}
}
