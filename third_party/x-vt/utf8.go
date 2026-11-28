package vt

import (
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// printableASCII reports whether r is a character that can never join the
// grapheme cluster beside it. No ASCII character is a combining mark, a ZWJ, a
// spacing mark or a regional indicator, and CR and LF are not printable, so a
// cluster boundary between two of these is certain without asking the
// segmenter.
func printableASCII(r rune) bool {
	return r >= ansi.SP && r < ansi.DEL
}

// MaxGraphemeBytes bounds one displayed cluster and the segmentation lookback.
// Beyond this application resource limit, insert U+FFFD as a new visible cluster
// and consume the overflowing rune. Later runes start from that replacement.
// This explicit forced boundary is independent of Write/read chunk boundaries.
const MaxGraphemeBytes = 256

// GraphemeOverflowCount counts visible forced segmentation boundaries.
// Like other Emulator accessors it requires external synchronization.
func (e *Emulator) GraphemeOverflowCount() uint64 { return e.graphemeOverflows }

// graphemeTail is the last visible cluster's actual position before cursor
// advancement. Keep the original single-shift/REP context for width amendments.
type graphemeTail struct {
	valid    bool
	content  string
	screen   *Screen
	cursor   Cursor
	end      uv.Position
	phantom  bool
	single   int
	lastChar rune
}

// handlePrint commits every rune immediately. A continuation amends the previous
// cluster, including within a single Write, so fragmentation never controls
// segmentation. The only unbounded input is the caller's slice: retained cluster
// bytes and each resegmentation are bounded by MaxGraphemeBytes + utf8.UTFMax.
func (e *Emulator) handlePrint(r rune) {
	tail := e.tail
	e.tail = graphemeTail{}
	ascii := printableASCII(r)
	// Two plain ASCII glyphs have a known boundary. A non-ASCII predecessor
	// can be Prepend, so an ASCII successor alone is not a valid fast path.
	plainPair := ascii && len(tail.content) == 1 && printableASCII(rune(tail.content[0]))
	if tail.valid && !plainPair && tail.screen == e.scr && tail.end == e.scr.cur.Position && tail.phantom == e.atPhantom {
		joined := tail.content + string(r)
		cluster, width := ansi.FirstGraphemeCluster(joined, ansi.GraphemeWidth)
		if len(cluster) == len(joined) {
			if len(joined) > MaxGraphemeBytes {
				e.graphemeOverflows++
				e.handleGrapheme("\ufffd", 1)
				return
			}
			// Wrapping/scrolling already happened at this actual cell. Do not
			// do it a second time when the cluster's width changes.
			e.scr.cur = tail.cursor
			e.atPhantom = false
			e.gsingle, e.lastChar = tail.single, tail.lastChar
			e.handleGrapheme(joined, width)
			return
		}
	}
	content, width := string(r), 1
	if !ascii {
		_, width = ansi.FirstGraphemeCluster(content, ansi.GraphemeWidth)
	}
	e.handleGrapheme(content, width)
}

// flushGrapheme flushes the current grapheme buffer, if any, and handles the
// grapheme as a single unit.
func (e *Emulator) flushGrapheme() {
	if len(e.grapheme) == 0 {
		return
	}

	// XXX: We always use [ansi.GraphemeWidth] here to report accurate widths
	// and it's up to the caller to decide how to handle Unicode vs non-Unicode
	// modes.
	method := ansi.GraphemeWidth
	graphemes := e.grapheme
	for len(graphemes) > 0 {
		cluster, width := ansi.FirstGraphemeCluster(graphemes, method)
		if len(cluster) == 0 {
			break
		}
		e.handleGrapheme(string(cluster), width)
		graphemes = graphemes[len(cluster):]
	}
	e.grapheme = e.grapheme[:0] // Reset the grapheme buffer.
}

// handleGrapheme handles UTF-8 graphemes.
func (e *Emulator) handleGrapheme(content string, width int) {
	awm := e.isModeSet(ansi.ModeAutoWrap)
	cell := uv.Cell{
		Content: content,
		Width:   width,
		Style:   e.scr.cursorPen(),
		Link:    e.scr.cursorLink(),
	}

	x, y := e.scr.CursorPosition()
	if e.atPhantom && awm {
		// moves cursor down similar to [Terminal.linefeed] except it doesn't
		// respects [ansi.LNM] mode.
		// This will reset the phantom state i.e. pending wrap state.
		e.index()
		_, y = e.scr.CursorPosition()
		x = 0
	}

	start := e.scr.cur
	start.Position = uv.Pos(x, y)
	tail := graphemeTail{valid: true, content: content, screen: e.scr, cursor: start, single: e.gsingle, lastChar: e.lastChar}

	// A single shift applies to the next character, whatever that character
	// turns out to be. Take it now, so it cannot leak onto the one after this
	// when this is a cluster no charset has a mapping for.
	single := e.gsingle
	e.gsingle = 0

	// Handle character set mappings
	if len(content) == 1 { //nolint:nestif
		var charset CharSet
		c := content[0]
		if single > 1 && single < 4 {
			charset = e.charsets[single]
		} else if c < 128 {
			charset = e.charsets[e.gl]
		} else {
			charset = e.charsets[e.gr]
		}

		if charset != nil {
			if r, ok := charset[c]; ok {
				cell.Content = r
				cell.Width = 1
			}
		}
	}

	if cell.Width == 1 && len(content) == 1 {
		e.lastChar, _ = utf8.DecodeRuneInString(content)
	}

	e.scr.SetCell(x, y, &cell)

	// Handle phantom state at the end of the line
	e.atPhantom = awm && x >= e.scr.Width()-1
	if !e.atPhantom {
		x += cell.Width
	}

	// NOTE: We don't reset the phantom state here, we handle it up above.
	e.scr.setCursor(x, y, false)
	tail.end, tail.phantom = e.scr.cur.Position, e.atPhantom
	e.tail = tail
}
