package main

import (
	"context"
	"flag"
	"io"
	"os"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/muxui"
	"github.com/dawksh/supertree/internal/terminal"
)

func nativeCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "attach" {
		return &command.Error{Code: "invalid_arguments", Message: "usage: st native attach --state <host-dir> --terminal <id> --generation <n> [--read-only]; Option-q detaches"}
	}
	flags := flag.NewFlagSet("native attach", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("state", "", "explicit native host namespace")
	id := flags.String("terminal", "", "existing native terminal ID")
	generation := flags.Int64("generation", 0, "explicit terminal generation")
	readOnly := flags.Bool("read-only", false, "view without owning input or geometry")
	if err := flags.Parse(args[1:]); err != nil {
		return &command.Error{Code: "invalid_arguments", Message: err.Error()}
	}
	if flags.NArg() != 0 || *dir == "" || *id == "" || *generation < 1 {
		return &command.Error{Code: "invalid_arguments", Message: "native attach requires --state, --terminal and --generation"}
	}
	return muxui.Attach(ctx, *dir, terminal.Target{TerminalID: *id, Generation: *generation}, *readOnly, os.Stdin, os.Stdout)
}
