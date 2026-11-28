package stack

import (
	"os"
	"strings"
)

func ChildEnvironment() []string {
	var env []string
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		switch key {
		case "TMUX", "TMUX_PANE", "TERM", "TERM_PROGRAM", "COLORTERM":
			continue
		}
		env = append(env, e)
	}
	return append(env, "TERM=xterm-256color", "TERM_PROGRAM=st-terminal-stack-experiment", "COLORTERM=truecolor")
}
