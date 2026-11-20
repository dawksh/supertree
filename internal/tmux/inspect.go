// Package tmux inspects terminal sessions without changing the tmux server.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Session struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	SupertreeLabel string `json:"supertree_label"`
}

// Inspect returns running, stopped, unavailable, or error. A missing executable
// and a server which is not running are different from an empty workspace list.
func Inspect(ctx context.Context, socket string) ([]Session, string, error) {
	sessions := []Session{}
	args := []string{"-N"}
	if socket != "" {
		args = append(args, "-S", socket)
	}
	// Path is last so tabs in directory names remain part of that field.
	args = append(args, "list-sessions", "-F", "#{session_id}\t#{session_name}\t#{@supertree_label}\t#{session_path}")
	cmd := exec.CommandContext(ctx, "tmux", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return sessions, "unavailable", nil
		}
		message := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			return sessions, "error", ctx.Err()
		}
		if strings.Contains(message, "no server running") || strings.Contains(message, "no sessions") ||
			(strings.Contains(message, "error connecting to") && strings.Contains(message, "No such file or directory")) {
			return sessions, "stopped", nil
		}
		return sessions, "error", fmt.Errorf("tmux inspection failed: %s (%w)", message, err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) != 4 || !strings.HasPrefix(fields[0], "$") {
			return sessions, "error", fmt.Errorf("unexpected tmux session record")
		}
		sessions = append(sessions, Session{ID: fields[0], Name: fields[1], SupertreeLabel: fields[2], Path: fields[3]})
	}
	return sessions, "running", nil
}
