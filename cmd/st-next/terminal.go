package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/terminal"
	"golang.org/x/sys/unix"
)

func decodeTerminalJSON(b []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(value) != nil || dec.Decode(new(any)) != io.EOF {
		return &command.Error{Code: "invalid_arguments", Message: "expected one terminal request object with known fields"}
	}
	return nil
}

func terminalHostRole(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return &command.Error{Code: "invalid_arguments", Message: "terminal host requires one private namespace"}
	}
	unix.CloseOnExec(3)
	lock := os.NewFile(3, "terminal-startup.lock")
	defer lock.Close()
	return terminal.ServeReady(ctx, args[0], func() { lock.Close() })
}

func terminalCommand(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return &command.Error{Code: "invalid_arguments", Message: "usage: st next terminal <host start|host serve|host status|host stop|ensure|inventory|inspect|screen|stop|restart> --state <private-dir>"}
	}
	host := args[0] == "host"
	action := args[0]
	rest := args[1:]
	if host {
		if len(rest) == 0 {
			return &command.Error{Code: "invalid_arguments", Message: "terminal host action required"}
		}
		action, rest = rest[0], rest[1:]
	}
	flags := flag.NewFlagSet("terminal", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("state", "", "explicit private native host namespace")
	force := flags.Bool("force", false, "explicitly terminate owned terminal jobs")
	flags.Bool("json", true, "structured output")
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if *dir == "" || flags.NArg() != 0 {
		return &command.Error{Code: "invalid_arguments", Message: "terminal operations require --state and no positional arguments"}
	}
	if host {
		switch action {
		case "serve":
			return terminal.ServeReady(ctx, *dir, nil)
		case "start":
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			status, err := terminal.StartHost(ctx, *dir, exe)
			if err != nil {
				return err
			}
			return json.NewEncoder(stdout).Encode(status)
		case "status":
			response, err := terminal.Call(ctx, *dir, terminal.Request{Op: "hello"})
			if err != nil {
				return err
			}
			return json.NewEncoder(stdout).Encode(response.Status)
		case "stop":
			status, err := terminal.Call(ctx, *dir, terminal.Request{Op: "hello"})
			if err != nil {
				return err
			}
			_, err = terminal.Call(ctx, *dir, terminal.Request{Op: "shutdown", Epoch: status.Status.Epoch, Force: *force})
			if err != nil {
				return err
			}
			deadline, cancel := context.WithTimeout(ctx, 35*time.Second)
			defer cancel()
			for {
				running, e := terminal.Running(*dir)
				if e != nil {
					return e
				}
				if !running {
					return json.NewEncoder(stdout).Encode(map[string]bool{"stopped": true})
				}
				select {
				case <-deadline.Done():
					return deadline.Err()
				case <-time.After(25 * time.Millisecond):
				}
			}
		default:
			return &command.Error{Code: "unknown_command", Message: "unknown terminal host action"}
		}
	}
	if action != "ensure" && action != "inventory" && action != "inspect" && action != "screen" && action != "stop" && action != "restart" {
		return &command.Error{Code: "unknown_command", Message: "unknown terminal action"}
	}
	req := terminal.Request{Op: action}
	if action != "inventory" {
		// Launch argv/environment are JSON on stdin, never a shell string or a
		// command-line option exposing secrets in the process list.
		b, err := io.ReadAll(io.LimitReader(stdin, terminal.MaxRequest+1))
		if err != nil {
			return err
		}
		if len(b) > terminal.MaxRequest {
			return &command.Error{Code: "invalid_arguments", Message: "terminal request exceeds bound"}
		}
		var value struct {
			Epoch  string           `json:"epoch"`
			Launch *terminal.Launch `json:"launch,omitempty"`
			Target *terminal.Target `json:"target,omitempty"`
			Force  bool             `json:"force,omitempty"`
			Row    int              `json:"row,omitempty"`
			Rows   int              `json:"rows,omitempty"`
		}
		if err = decodeTerminalJSON(b, &value); err != nil {
			return err
		}
		req.Epoch, req.Launch, req.Target, req.Force, req.Row, req.Rows = value.Epoch, value.Launch, value.Target, value.Force || *force, value.Row, value.Rows
	}
	response, err := terminal.Call(ctx, *dir, req)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(response)
}
