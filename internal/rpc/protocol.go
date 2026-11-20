// Package rpc implements newline-delimited JSON-RPC 2.0 over an owner-only Unix
// socket. Subscription connections are dedicated to server event notifications.
package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/dawksh/supertree/internal/command"
)

const MaxFrame = 8 << 20

type Meta struct {
	Directory    string `json:"directory,omitempty"`
	Actor        string `json:"actor,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	WorktreeRoot string `json:"worktree_root,omitempty"`
	TmuxSocket   string `json:"tmux_socket,omitempty"`
}

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Meta    Meta            `json:"meta,omitempty"`
}

type Failure struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    *command.Error `json:"data,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Failure        `json:"error,omitempty"`
}

func failure(id json.RawMessage, err error) Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	var coded *command.Error
	if !errors.As(err, &coded) {
		coded = &command.Error{Code: "internal_error", Message: err.Error()}
	}
	code := -32000
	switch coded.Code {
	case "parse_error":
		code = -32700
	case "invalid_request":
		code = -32600
	case "unknown_command":
		code = -32601
	case "invalid_arguments":
		code = -32602
	case "internal_error":
		code = -32603
	}
	return Response{JSONRPC: "2.0", ID: id, Error: &Failure{code, coded.Message, coded}}
}

func request(raw []byte) (Request, error) {
	var req Request
	if !json.Valid(raw) {
		return req, &command.Error{Code: "parse_error", Message: "invalid JSON"}
	}
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || json.Unmarshal(raw, &req) != nil || req.JSONRPC != "2.0" || req.Method == "" {
		return Request{}, &command.Error{Code: "invalid_request", Message: "expected a JSON-RPC 2.0 request"}
	}
	if len(req.ID) > 0 && string(req.ID) != "null" {
		var id any
		_ = json.Unmarshal(req.ID, &id)
		switch id.(type) {
		case string, float64:
		default:
			return Request{}, &command.Error{Code: "invalid_request", Message: "id must be a string, number or null"}
		}
	}
	return req, nil
}

func dial(ctx context.Context, socket string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, &command.Error{Code: "daemon_unavailable", Message: fmt.Sprintf("connect to daemon %s: %v", socket, err)}
	}
	return conn, nil
}

func callOn(ctx context.Context, conn net.Conn, method string, params json.RawMessage, meta Meta) (*bufio.Scanner, Response, error) {
	// Cancellation must interrupt both writes and blocked reads, including a
	// subscription with no new events. A canceled CLI never hangs on the socket.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(Request{"2.0", json.RawMessage("1"), method, params, meta}); err != nil {
		return nil, Response{}, err
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), MaxFrame)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, Response{}, err
		}
		return nil, Response{}, errors.New("daemon closed before responding")
	}
	var response Response
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		return nil, response, err
	}
	if response.JSONRPC != "2.0" || string(response.ID) != "1" || (response.Error == nil && len(response.Result) == 0) {
		return nil, response, errors.New("invalid daemon response")
	}
	if response.Error != nil {
		if response.Error.Data != nil {
			return nil, response, response.Error.Data
		}
		return nil, response, errors.New(response.Error.Message)
	}
	return scanner, response, nil
}

func Call(ctx context.Context, socket, method string, params json.RawMessage, meta Meta) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := dial(ctx, socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_, response, err := callOn(ctx, conn, method, params, meta)
	return response.Result, err
}

// Stream emits the subscription acknowledgement followed by event params as
// JSON objects. Cursor handling is explicit: consumers save epoch and sequence.
func Stream(ctx context.Context, socket string, params json.RawMessage, meta Meta, emit func(json.RawMessage) error) error {
	conn, err := dial(ctx, socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	scanner, response, err := callOn(ctx, conn, "event.subscribe", params, meta)
	if err != nil {
		return err
	}
	if err := emit(response.Result); err != nil {
		return err
	}
	for scanner.Scan() {
		var notification struct {
			JSONRPC string          `json:"jsonrpc"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &notification); err != nil {
			return err
		}
		if notification.JSONRPC != "2.0" || notification.Method != "event" {
			return errors.New("invalid event notification")
		}
		if err := emit(notification.Params); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return &command.Error{Code: "disconnected", Message: "event connection closed; reconnect with the last epoch and sequence"}
}
