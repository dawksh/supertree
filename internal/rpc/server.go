package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/workspace"
)

type Status struct {
	SchemaVersion int      `json:"schema_version"`
	Running       bool     `json:"running"`
	PID           int      `json:"pid"`
	Socket        string   `json:"socket"`
	StateDir      string   `json:"state_dir"`
	WorktreeRoot  string   `json:"worktree_root"`
	TmuxSocket    string   `json:"tmux_socket"`
	Epoch         string   `json:"epoch"`
	Sequence      int64    `json:"sequence"`
	Capabilities  []string `json:"capabilities"`
}

type streamKey struct{}
type metaKey struct{}
type streamSlot struct {
	events <-chan core.Event
	cancel func()
}

type server struct {
	native  bool
	core    *core.Core
	r       *command.Registry
	options workspace.Options
	socket  string
	stop    context.CancelFunc
}

// Serve owns only the daemon socket, database and client connections. Shutdown
// never signals tmux or the terminal programs it hosts.
func Serve(ctx context.Context, options workspace.Options, socket string, log io.Writer) error {
	return ServeReady(ctx, options, socket, log, nil)
}

// ServeReady calls ready after initialization, before accepting queued clients.
func ServeReady(ctx context.Context, options workspace.Options, socket string, log io.Writer, ready func()) error {
	return serveReady(ctx, options, socket, log, ready, false)
}

// ServeNative exposes the engine-independent native metadata assembly. Callers
// must select a separate private state/socket namespace from the legacy daemon.
// No installed CLI entrypoint is enabled by this internal assembly function.
func ServeNative(ctx context.Context, options workspace.Options, socket string, log io.Writer) error {
	return serveReady(ctx, options, socket, log, nil, true)
}

func serveReady(ctx context.Context, options workspace.Options, socket string, log io.Writer, ready func(), native bool) error {
	options.ControlSocket = socket
	endpoint, err := Listen(options.StateDir, socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	open := core.Open
	if native {
		open = core.OpenNative
	}
	c, err := open(ctx, options)
	if err != nil {
		endpoint.Close()
		return err
	}
	s := &server{native: native, core: c, options: options, socket: socket, stop: cancel}
	s.r, err = c.Registry(func(ctx context.Context, args core.InspectArgs) (workspace.Snapshot, error) {
		meta, _ := ctx.Value(metaKey{}).(Meta)
		if err := c.Refresh(ctx, meta.Directory, core.Origin{Actor: meta.Actor, RequestID: meta.RequestID}); err != nil {
			return workspace.Snapshot{}, err
		}
		return c.Snapshot(args.WorkspaceID)
	})
	if err == nil {
		err = command.Register(s.r, command.Definition{Name: "daemon.status", Version: 1,
			Description: "Inspect daemon identity, protocol capabilities and current sequence.", Effects: []string{"read"}, Repeatable: true},
			func(context.Context, struct{}) (Status, error) { return s.status(), nil })
	}
	if err == nil {
		err = command.Register(s.r, command.Definition{Name: "daemon.stop", Version: 1,
			Description: "Stop this daemon while leaving tmux sessions and programs running.", Effects: []string{"daemon.stop"}},
			func(context.Context, struct{}) (struct {
				Stopping bool `json:"stopping"`
			}, error) {
				return struct {
					Stopping bool `json:"stopping"`
				}{true}, nil
			})
	}
	if err == nil {
		err = command.Register(s.r, command.Definition{Name: "event.subscribe", Version: 1,
			Description: "Subscribe on a dedicated connection; resume with epoch and after, or use the supplied snapshot after a gap.", Effects: []string{"read"}, Repeatable: true},
			func(ctx context.Context, cursor core.Cursor) (core.Subscription, error) {
				slot, ok := ctx.Value(streamKey{}).(*streamSlot)
				if !ok {
					return core.Subscription{}, &command.Error{Code: "invalid_arguments", Message: "subscriptions require a single request with an id on a dedicated connection"}
				}
				meta, _ := ctx.Value(metaKey{}).(Meta)
				if err := c.Refresh(ctx, meta.Directory, core.Origin{Actor: meta.Actor, RequestID: meta.RequestID}); err != nil {
					return core.Subscription{}, err
				}
				ack, events, close, err := c.Subscribe(cursor)
				if err == nil {
					slot.events, slot.cancel = events, close
				}
				return ack, err
			})
	}
	if err != nil {
		endpoint.Close()
		c.Close()
		return err
	}
	if ready != nil {
		ready()
	}
	var clients sync.WaitGroup
	clients.Add(1)
	go func() {
		defer clients.Done()
		c.Poll(ctx, time.Second, func(err error) { fmt.Fprintf(log, "refresh: %v\n", err) })
	}()
	closeListener := context.AfterFunc(ctx, func() { endpoint.Listener.Close() })
	defer func() {
		cancel()
		closeListener()
		endpoint.Listener.Close()
		clients.Wait()
		c.Close()
		endpoint.lock.Close()
	}()
	for {
		conn, err := endpoint.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		clients.Add(1)
		go func() {
			defer clients.Done()
			s.connection(ctx, conn)
		}()
	}
}

func (s *server) status() Status {
	snapshot, _ := s.core.Snapshot("")
	capabilities := []string{"jsonrpc-2.0", "durable-workspace-identity", "event-replay", "resnapshot"}
	tmuxSocket := s.options.TmuxSocket
	if s.native {
		tmuxSocket = ""
		capabilities = append(capabilities, "native-workspace-metadata", "native-buffer-layouts-v1")
	}
	return Status{command.SchemaVersion, true, os.Getpid(), s.socket, s.options.StateDir,
		s.options.WorktreeRoot, tmuxSocket, snapshot.Epoch, snapshot.Sequence, capabilities}
}

func (s *server) execute(ctx context.Context, req Request) (Response, bool) {
	if req.Meta.Directory != "" && !filepath.IsAbs(req.Meta.Directory) {
		return failure(req.ID, &command.Error{Code: "invalid_arguments", Message: "meta.directory must be absolute"}), false
	}
	if req.Method != "daemon.status" && req.Method != "daemon.stop" &&
		((req.Meta.WorktreeRoot != "" && req.Meta.WorktreeRoot != s.options.WorktreeRoot) ||
			(!s.native && req.Meta.TmuxSocket != "" && req.Meta.TmuxSocket != s.options.TmuxSocket)) {
		return failure(req.ID, &command.Error{Code: "configuration_mismatch", Message: "daemon uses different workspace or tmux settings; stop it or select a separate ST_STATE"}), false
	}
	ctx = command.WithOrigin(ctx, command.Origin{Actor: req.Meta.Actor, RequestID: req.Meta.RequestID})
	result, err := s.r.Invoke(context.WithValue(ctx, metaKey{}, req.Meta), req.Method, req.Params)
	if err != nil {
		return failure(req.ID, err), false
	}
	data, err := json.Marshal(result)
	if err != nil {
		return failure(req.ID, err), false
	}
	return Response{JSONRPC: "2.0", ID: req.ID, Result: data}, req.Method == "daemon.stop"
}

func write(conn net.Conn, value any) error {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return json.NewEncoder(conn).Encode(value)
}

func (s *server) connection(ctx context.Context, conn net.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), MaxFrame)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(time.Minute))
		if !scanner.Scan() {
			return
		}
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) > 0 && raw[0] == '[' && json.Valid(raw) {
			var batch []json.RawMessage
			_ = json.Unmarshal(raw, &batch)
			if len(batch) == 0 {
				_ = write(conn, failure(nil, &command.Error{Code: "invalid_request", Message: "empty batch"}))
				continue
			}
			responses := []Response{}
			stopping := false
			for _, item := range batch {
				req, err := request(item)
				if err != nil {
					responses = append(responses, failure(nil, err))
					continue
				}
				response, stop := s.execute(ctx, req)
				stopping = stopping || stop
				if len(req.ID) != 0 {
					responses = append(responses, response)
				}
			}
			if len(responses) != 0 {
				if err := write(conn, responses); err != nil {
					return
				}
			}
			if stopping {
				s.stop()
				return
			}
			continue
		}
		req, err := request(raw)
		if err != nil {
			if write(conn, failure(nil, err)) != nil {
				return
			}
			continue
		}
		slot := &streamSlot{}
		requestCtx := ctx
		if req.Method == "event.subscribe" && len(req.ID) != 0 {
			requestCtx = context.WithValue(ctx, streamKey{}, slot)
		}
		response, stopping := s.execute(requestCtx, req)
		if slot.cancel != nil {
			defer slot.cancel()
		}
		if len(req.ID) != 0 {
			if err := write(conn, response); err != nil {
				return
			}
		}
		if stopping {
			s.stop()
			return
		}
		if slot.events != nil {
			_ = conn.SetReadDeadline(time.Time{})
			// A subscription owns the connection. EOF/cancellation must detach
			// idle clients even when there are no state changes to write.
			go func() { scanner.Scan(); cancel() }()
			for {
				select {
				case <-ctx.Done():
					return
				case event, ok := <-slot.events:
					if !ok {
						_ = write(conn, map[string]any{"jsonrpc": "2.0", "method": "event", "params": map[string]any{
							"type": "resnapshot_required", "reason": "subscriber_queue_full"}})
						return
					}
					if write(conn, map[string]any{"jsonrpc": "2.0", "method": "event", "params": event}) != nil {
						return
					}
				}
			}
		}
	}
}

func IsUnavailable(err error) bool {
	var coded *command.Error
	return errors.As(err, &coded) && coded.Code == "daemon_unavailable"
}
