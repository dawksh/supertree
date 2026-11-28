package uitheme

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestTerminalFallbacksAndSafeTextKeepTheSameMeaning(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("ST_ASCII", "1")
	theme := New()
	for state, want := range map[string]string{"waiting": "! input", "running": "* running", "idle": "- ready", "failed": "x failed", "done": "+ done"} {
		got := theme.Status(state, -1)
		if got != want || strings.Contains(got, "\x1b") {
			t.Fatalf("%s fallback: %q", state, got)
		}
	}
	if strings.Contains(theme.Row("error", "failed", "bad\x1b[31m source\x1b[0m\nnew-row"), "\x1b") || strings.Contains(theme.Row("error", "failed", "bad\nnew-row"), "\n") {
		t.Fatal("a message can inject terminal escapes or rows")
	}
	for _, s := range []string{"feature/very-long-name-with-an-important-tail", "日本語/とても長いブランチ名"} {
		middle := theme.Middle(s, 18)
		if ansi.StringWidth(middle) > 18 || !strings.Contains(middle, "~") {
			t.Fatalf("label overflow: %q", middle)
		}
	}
	if strings.Contains(theme.Status("idle", 0), "+") {
		t.Fatal("turn completion is being presented as task success")
	}
}

func TestGoAndShellShareTheirColourRoles(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("ST_THEME", "dark")
	theme := New()
	shell, err := os.ReadFile(filepath.Join("..", "..", "bin", "st-lib", "05-ui.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []struct {
		name  string
		style lipgloss.Style
	}{{"UI_A", theme.Accent}, {"UI_IN", theme.Input}, {"UI_RUN", theme.Running}, {"UI_OK", theme.Success}, {"UI_ERR", theme.Error}, {"UI_REPO", theme.Repo}, {"UI_DIM", theme.Muted}, {"UI_WARN", theme.Warning}} {
		match := regexp.MustCompile(`ui_tone\s+` + role.name + `\s+([a-f0-9]+)`).FindStringSubmatch(string(shell))
		if len(match) != 2 || fmt.Sprint(role.style.GetForeground()) != "#"+match[1] {
			t.Fatalf("Go and shell disagree on %s", role.name)
		}
		if role.style.GetBold() != (role.name == "UI_IN") {
			t.Fatalf("only the input state should demand bold emphasis: %s", role.name)
		}
	}
	border := regexp.MustCompile(`ui_tone\s+UI_BORDER\s+([a-f0-9]+)`).FindStringSubmatch(string(shell))
	if len(border) != 2 || fmt.Sprint(theme.border) != "#"+border[1] {
		t.Fatal("Go and shell disagree on the border colour")
	}
}
