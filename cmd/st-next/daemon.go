package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/workspace"
	"golang.org/x/sys/unix"
)

func daemonSocket(options workspace.Options) string {
	if socket := os.Getenv("ST_NEXT_SOCKET"); socket != "" {
		return socket
	}
	return rpc.SocketPath(options.StateDir)
}

func clientMeta(options workspace.Options) rpc.Meta {
	actor := os.Getenv("ST_NEXT_ACTOR")
	if actor == "" {
		actor = "human"
	}
	return rpc.Meta{Directory: options.Directory, Actor: actor, RequestID: os.Getenv("ST_NEXT_REQUEST_ID"),
		WorktreeRoot: options.WorktreeRoot, TmuxSocket: options.TmuxSocket}
}

func runtimeCommand(ctx context.Context, args []string, stdout io.Writer, asJSON bool) error {
	options, err := environment()
	if err != nil {
		return err
	}
	socket := daemonSocket(options)
	if !filepath.IsAbs(socket) {
		return &command.Error{Code: "invalid_arguments", Message: "ST_NEXT_SOCKET must be absolute"}
	}
	if args[0] == "events" {
		flags := flag.NewFlagSet("events", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		flags.Bool("json", true, "emit newline-delimited JSON")
		var cursor core.Cursor
		flags.StringVar(&cursor.Epoch, "epoch", "", "previous daemon epoch")
		flags.Int64Var(&cursor.After, "after", 0, "last received sequence")
		flags.StringVar(&cursor.WorkspaceID, "workspace", "", "watch one workspace")
		if err := flags.Parse(args[1:]); err != nil {
			return &command.Error{Code: "invalid_arguments", Message: err.Error()}
		}
		if flags.NArg() != 0 || (cursor.After != 0 && cursor.Epoch == "") {
			return &command.Error{Code: "invalid_arguments", Message: "events accepts no positional arguments; --after requires --epoch"}
		}
		params, _ := json.Marshal(cursor)
		err := rpc.Stream(ctx, socket, params, clientMeta(options), func(data json.RawMessage) error {
			return json.NewEncoder(stdout).Encode(data)
		})
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	if len(args) < 2 {
		return &command.Error{Code: "invalid_arguments", Message: "usage: st next daemon <start|serve|status|stop> [--json]"}
	}
	flags := flag.NewFlagSet("daemon "+args[1], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&asJSON, "json", asJSON, "print structured output")
	if err := flags.Parse(args[2:]); err != nil {
		return &command.Error{Code: "invalid_arguments", Message: err.Error()}
	}
	if flags.NArg() != 0 {
		return &command.Error{Code: "invalid_arguments", Message: "unexpected positional arguments"}
	}
	var result rpc.Status
	switch args[1] {
	case "serve":
		return rpc.Serve(ctx, options, socket, os.Stderr)
	case "start":
		result, err = startDaemon(ctx, options, socket)
	case "status":
		result, err = daemonStatus(ctx, options, socket)
	case "stop":
		result, err = daemonStatus(ctx, options, socket)
		if err == nil && result.Running {
			_, err = rpc.Call(ctx, socket, "daemon.stop", json.RawMessage(`{}`), clientMeta(options))
			if err == nil {
				deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				for {
					running, checkErr := rpc.Running(options.StateDir)
					if checkErr != nil {
						err = checkErr
						break
					}
					if !running {
						result.Running, result.PID = false, 0
						break
					}
					if err = pause(deadline); err != nil {
						break
					}
				}
			}
		}
	default:
		return &command.Error{Code: "unknown_command", Message: "unknown daemon action: " + args[1]}
	}
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(result)
	}
	if result.Running {
		_, err = fmt.Fprintf(stdout, "Daemon running (pid %d)\nSocket: %s\nEpoch: %s; sequence: %d\n", result.PID, result.Socket, result.Epoch, result.Sequence)
	} else {
		_, err = fmt.Fprintln(stdout, "Daemon stopped; tmux sessions are unaffected.")
	}
	return err
}

func daemonStatus(ctx context.Context, options workspace.Options, socket string) (rpc.Status, error) {
	data, err := rpc.Call(ctx, socket, "daemon.status", json.RawMessage(`{}`), clientMeta(options))
	if err != nil {
		running, checkErr := rpc.Running(options.StateDir)
		if checkErr != nil {
			return rpc.Status{}, checkErr
		}
		if !running && rpc.IsUnavailable(err) {
			return rpc.Status{SchemaVersion: command.SchemaVersion, Running: false, Socket: socket, StateDir: options.StateDir,
				WorktreeRoot: options.WorktreeRoot, TmuxSocket: options.TmuxSocket, Capabilities: []string{}}, nil
		}
		return rpc.Status{}, err
	}
	var status rpc.Status
	err = json.Unmarshal(data, &status)
	return status, err
}

func startDaemon(ctx context.Context, options workspace.Options, socket string) (rpc.Status, error) {
	status, err := daemonStatus(ctx, options, socket)
	if err == nil && status.Running {
		if status.WorktreeRoot != options.WorktreeRoot || status.TmuxSocket != options.TmuxSocket {
			return status, &command.Error{Code: "configuration_mismatch", Message: "daemon uses different workspace or tmux settings; stop it before starting with this configuration"}
		}
		return status, nil
	}
	// Starting twice concurrently is safe: the child must acquire the state lock.
	if err != nil && !rpc.IsUnavailable(err) {
		return status, err
	}
	dir := filepath.Join(options.StateDir, "next")
	if err := rpc.PrivateDir(dir); err != nil {
		return status, err
	}
	logPath := filepath.Join(dir, "daemon.log")
	fd, err := unix.Open(logPath, unix.O_CREAT|unix.O_APPEND|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return status, err
	}
	log := os.NewFile(uintptr(fd), logPath)
	defer log.Close()
	executable, err := os.Executable()
	if err != nil {
		return status, err
	}
	child := exec.Command(executable, "daemon", "serve")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdout, child.Stderr = log, log
	if err := child.Start(); err != nil {
		return status, err
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	deadline, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		probe, done := context.WithTimeout(deadline, 200*time.Millisecond)
		data, err := rpc.Call(probe, socket, "daemon.status", json.RawMessage(`{}`), clientMeta(options))
		done()
		if err == nil {
			err = json.Unmarshal(data, &status)
			return status, err
		}
		select {
		case childErr := <-exited:
			return status, fmt.Errorf("daemon exited during startup (%v); see %s", childErr, logPath)
		default:
		}
		if err := pause(deadline); err != nil {
			return status, fmt.Errorf("waiting for daemon startup: %w; see %s", err, logPath)
		}
	}
}

func pause(ctx context.Context) error {
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
