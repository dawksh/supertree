package tmux

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Client struct{ Socket string }
type Pane struct {
	ID, WindowID, SessionID, ProcessID, Directory, WindowName string
	Dead                                                      bool
	ExitCode                                                  int
}

func (c Client) Args(args ...string) []string {
	if c.Socket != "" {
		return append([]string{"-S", c.Socket}, args...)
	}
	return args
}

func (c Client) Run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", c.Args(args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %s (%w)", args[0], strings.TrimSpace(stderr.String()), err)
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}

func (c Client) Panes(ctx context.Context) ([]Pane, error) {
	data, err := c.Run(ctx, "-N", "list-panes", "-a", "-F", "#{pane_id}\t#{window_id}\t#{session_id}\t#{@st_next_process}\t#{pane_dead}\t#{pane_dead_status}\t#{window_name}\t#{pane_current_path}")
	if err != nil {
		_, status, _ := Inspect(ctx, c.Socket)
		if status == "stopped" || status == "unavailable" {
			return []Pane{}, nil
		}
		return nil, err
	}
	result := []Pane{}
	for _, line := range strings.Split(data, "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 8)
		if len(fields) != 8 || !strings.HasPrefix(fields[0], "%") {
			return nil, fmt.Errorf("unexpected tmux pane record")
		}
		exit := -1
		if fields[5] != "" {
			exit, _ = strconv.Atoi(fields[5])
		}
		result = append(result, Pane{ID: fields[0], WindowID: fields[1], SessionID: fields[2], ProcessID: fields[3], Dead: fields[4] == "1", ExitCode: exit, WindowName: fields[6], Directory: fields[7]})
	}
	return result, nil
}

func (c Client) Tagged(ctx context.Context, paneID, processID string) (Pane, error) {
	panes, err := c.Panes(ctx)
	if err != nil {
		return Pane{}, err
	}
	for _, pane := range panes {
		if pane.ID == paneID && pane.ProcessID == processID {
			return pane, nil
		}
	}
	return Pane{}, fmt.Errorf("process terminal is missing or belongs to another process")
}

// ShellCommand quotes every argument once for the terminal's shell. Passing
// arbitrary arguments directly to tmux can interpret semicolons as commands.
func ShellCommand(argv []string, environment map[string]string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	words := []string{"exec", "/usr/bin/env", "--"}
	// Map iteration order does not affect execution; launch definitions retain
	// their original argument array rather than this display representation.
	for key, value := range environment {
		words = append(words, quote(key+"="+value))
	}
	for _, arg := range argv {
		words = append(words, quote(arg))
	}
	return strings.Join(words, " ")
}

// Watch attaches a control client without selecting windows or panes. The
// caller still reconciles periodically and reconnects after server/session loss.
func (c Client) Watch(ctx context.Context, session string, changed func()) error {
	cmd := exec.CommandContext(ctx, "tmux", c.Args("-N", "-C", "attach-session", "-f", "no-output,ignore-size", "-t", session)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Consume protocol lines, never pane output. Names and paths from tmux are
	// data; no control-mode output is sent back as a command.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "%") && !strings.HasPrefix(line, "%begin") && !strings.HasPrefix(line, "%end") {
			changed()
		}
	}
	return cmd.Wait()
}
