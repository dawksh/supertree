// Package uitheme applies Supertree's terminal design grammar. The palette
// mirrors 05-ui.sh; fonts and terminal backgrounds remain the user's choice.
package uitheme

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type Theme struct {
	Accent, Input, Running, Success, Muted, Error, Warning, Repo lipgloss.Style
	Pointer, Main, Ellipsis, Prompt                              string
	ASCII, Plain                                                 bool
	border                                                       lipgloss.Color
}

func New() Theme {
	t := Theme{Pointer: "▌", Main: "⌂", Ellipsis: "…", Prompt: "›", ASCII: os.Getenv("ST_ASCII") != "", Plain: os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"}
	dark := []string{"#5ccfe6", "#ffb454", "#73b8ff", "#9fdc7c", "#8b94a6", "#ff7a7a", "#ffb454", "#d2a6ff", "#3a4458"}
	if os.Getenv("ST_THEME") == "light" {
		dark = []string{"#0b6680", "#9a4a08", "#1d4ed8", "#126c34", "#566170", "#b91c1c", "#9a4a08", "#7e22ce", "#9aa4b2"}
	}
	if os.Getenv("ST_THEME") == "ansi" {
		dark = []string{"6", "3", "4", "2", "8", "1", "3", "5", "8"}
	}
	styles := []*lipgloss.Style{&t.Accent, &t.Input, &t.Running, &t.Success, &t.Muted, &t.Error, &t.Warning, &t.Repo}
	for i, s := range styles {
		*s = lipgloss.NewStyle()
		if !t.Plain {
			*s = s.Foreground(lipgloss.Color(dark[i]))
		}
	}
	t.Input = t.Input.Bold(!t.Plain)
	t.border = lipgloss.Color(dark[8])
	if t.ASCII {
		t.Pointer, t.Main, t.Ellipsis, t.Prompt = "|", "@", "~", ">"
	}
	return t
}

func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
func one(s string) string { return strings.NewReplacer("\n", " ", "\t", " ").Replace(Clean(s)) }
func (t Theme) glyph(unicode, ascii string) string {
	if t.ASCII {
		return ascii
	}
	return unicode
}
func (t Theme) Frame(active bool) lipgloss.Style {
	border := lipgloss.RoundedBorder()
	if t.ASCII {
		border = lipgloss.Border{Top: "-", Bottom: "-", Left: "|", Right: "|", TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+"}
	}
	s := lipgloss.NewStyle().Border(border)
	if !t.Plain {
		if active {
			s = s.BorderForeground(t.Accent.GetForeground())
		} else {
			s = s.BorderForeground(t.border)
		}
	}
	return s
}

func (t Theme) Status(state string, exit int) string {
	style, glyph, label := t.Muted, t.glyph("○", "-"), state
	switch state {
	case "input", "waiting":
		style, glyph, label = t.Input, t.glyph("◆", "!"), "input"
	case "running", "starting", "connecting":
		style, glyph = t.Running, t.glyph("●", "*")
	case "done":
		style, glyph = t.Success, t.glyph("✓", "+")
	case "exited":
		if exit == 0 {
			style, glyph = t.Success, t.glyph("✓", "+")
		} else if exit > 0 {
			style, glyph, label = t.Error, t.glyph("✗", "x"), fmt.Sprintf("exit %d", exit)
		}
	case "failed", "error":
		style, glyph = t.Error, t.glyph("✗", "x")
	case "interrupted", "unknown":
		style, glyph = t.Warning, "!"
	case "idle":
		label = "ready"
	case "unavailable", "missing", "stopped":
		label = "closed"
	}
	return style.Render(glyph + " " + one(label))
}

func (t Theme) Row(kind, verb, detail string) string {
	style, glyph := t.Muted, t.glyph("›", ">")
	switch kind {
	case "ok":
		style, glyph = t.Success, t.glyph("✓", "+")
	case "error":
		style, glyph = t.Error, t.glyph("✗", "x")
	case "warn":
		style, glyph = t.Warning, "!"
	case "input":
		style, glyph = t.Input, "?"
	case "running":
		style, glyph = t.Running, t.glyph("●", "*")
	}
	return "  " + style.Render(glyph) + "  " + fmt.Sprintf("%-10s", one(verb)) + " " + t.Muted.Render(one(detail))
}

func (t Theme) Middle(s string, width int) string {
	s = one(s)
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width < 3 {
		return ansi.Truncate(s, max(1, width), t.Ellipsis)
	}
	runes := []rune(s)
	left, right := "", ""
	budget := width - ansi.StringWidth(t.Ellipsis)
	for len(runes) > 0 && ansi.StringWidth(left) < (budget+1)/2 {
		candidate := left + string(runes[0])
		if ansi.StringWidth(candidate) > (budget+1)/2 {
			break
		}
		left = candidate
		runes = runes[1:]
	}
	for len(runes) > 0 {
		candidate := string(runes[len(runes)-1]) + right
		if ansi.StringWidth(candidate) > budget-ansi.StringWidth(left) {
			break
		}
		right = candidate
		runes = runes[:len(runes)-1]
	}
	return left + t.Ellipsis + right
}

func (t Theme) Label(repo, branch string, width int) string {
	label := t.Middle(repo+"/"+branch, width)
	prefix, rest, ok := strings.Cut(label, "/")
	if !ok {
		return t.Repo.Render(label)
	}
	return t.Repo.Render(prefix) + "/" + rest
}
