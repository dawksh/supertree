package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/dawksh/supertree/internal/tui"
	"golang.org/x/term"
	"io"
	"os"
)

func tuiCommand(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("tui", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("workspace", "", "initial workspace ID")
	if err := flags.Parse(args); err != nil {
		return &command.Error{Code: "invalid_arguments", Message: err.Error()}
	}
	if flags.NArg() != 0 {
		return &command.Error{Code: "invalid_arguments", Message: "usage: st next tui [--workspace <id>]"}
	}
	file, ok := stdout.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) || !term.IsTerminal(int(os.Stdin.Fd())) {
		return &command.Error{Code: "terminal_required", Message: "tui needs an interactive terminal; use inspect, commands, or invoke for structured access"}
	}
	options, err := environment()
	if err != nil {
		return err
	}
	socket := daemonSocket(options)
	if _, err := startDaemon(ctx, options, socket); err != nil {
		return err
	}
	meta := clientMeta(options)
	meta.Actor = "human:tui"
	invoke := func(ctx context.Context, name string, args any) (json.RawMessage, error) {
		params, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		requestMeta := meta
		origin := command.Caller(ctx)
		if origin.Actor != "system" {
			requestMeta.Actor = origin.Actor
		}
		requestMeta.RequestID = origin.RequestID
		return rpc.Call(ctx, socket, name, params, requestMeta)
	}
	data, err := invoke(ctx, "command.list", struct{}{})
	if err != nil {
		return err
	}
	var commands core.CommandList
	if err := json.Unmarshal(data, &commands); err != nil {
		return err
	}
	ready := false
	for _, d := range commands.Commands {
		if d.Name == "view.open" {
			ready = true
		}
	}
	if !ready {
		return &command.Error{Code: "upgrade_required", Message: "restart the core to load the TUI commands: st next daemon stop; st next daemon start"}
	}
	return tui.Run(ctx, tui.Options{Invoke: invoke, Attach: attachTerminal, WorkspaceID: *id}, socket, meta, os.Stdin, stdout)
}
