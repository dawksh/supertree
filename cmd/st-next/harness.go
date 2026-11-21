package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/harness"
	"github.com/dawksh/supertree/internal/rpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func mcpCommand(ctx context.Context, args []string, stdout io.Writer) error {
	f := flag.NewFlagSet("mcp", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	name := f.String("harness", "external", "agent attribution")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || strings.ContainsAny(*name, " \t\n\r:") || *name == "" {
		return &command.Error{Code: "invalid_arguments", Message: "usage: st next mcp [--harness <name>]"}
	}
	options, err := environment()
	if err != nil {
		return err
	}
	if _, err := startDaemon(ctx, options, daemonSocket(options)); err != nil {
		return err
	}
	meta := clientMeta(options)
	meta.Actor, meta.RequestID = "agent:"+*name, ""
	call := func(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
		return rpc.Call(ctx, daemonSocket(options), name, args, meta)
	}
	// Resolve through the same cwd-based selector as all human CLI commands.
	// currentWorkspace uses the same core/socket and performs no mutations.
	workspaceID, err := currentWorkspace(ctx, options)
	if err != nil {
		return err
	}
	data, err := call(ctx, "command.list", nil)
	if err != nil {
		return err
	}
	var list core.CommandList
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	s := harness.Server(version, workspaceID, list.Commands, call)
	return s.Run(ctx, &mcp.IOTransport{Reader: os.Stdin, Writer: writerCloser{stdout}})
}

type writerCloser struct{ io.Writer }

func (writerCloser) Close() error { return nil }

// Per-launch overrides preserve the user's existing harness config and MCP
// servers. No project/global settings files or agent conversations are edited.
func harnessCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "codex" && args[0] != "claude") {
		return &command.Error{Code: "invalid_arguments", Message: "usage: st next harness <codex|claude> -- [harness arguments]"}
	}
	provider, argv := args[0], args[1:]
	if len(argv) != 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	options, err := environment()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	env := map[string]string{"ST_STATE": options.StateDir, "ST_WORKTREE_ROOT": options.WorktreeRoot,
		"ST_NEXT_SOCKET": daemonSocket(options), "ST_NEXT_TMUX_SOCKET": options.TmuxSocket}
	serverArgs := []string{"mcp", "--harness", provider}
	if provider == "codex" {
		encodedArgs, _ := json.Marshal(serverArgs)
		override := fmt.Sprintf("mcp_servers.supertree={command=%s,args=%s,env=%s,cwd=%s}", tomlString(executable), encodedArgs, tomlEnvironment(env), tomlString(options.Directory))
		argv = append([]string{"-c", override}, argv...)
	} else {
		config, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"supertree": map[string]any{
			"command": executable, "args": serverArgs, "env": env,
		}}})
		argv = append([]string{"--mcp-config", string(config)}, argv...)
	}
	child := exec.CommandContext(ctx, provider, argv...)
	child.Dir = options.Directory
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, stdout, stderr
	return child.Run()
}

func tomlString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
func tomlEnvironment(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, tomlString(key)+"="+tomlString(env[key]))
	}
	return "{" + strings.Join(entries, ",") + "}"
}
