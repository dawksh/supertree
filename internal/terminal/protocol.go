package terminal

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/dawksh/supertree/internal/command"
)

func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxMessage {
		return failure("resource_limit", "terminal frame exceeds bound")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(b)))
	_, err = io.Copy(w, io.MultiReader(bytes.NewReader(header[:]), bytes.NewReader(b)))
	return err
}
func readFrame(r io.Reader, v any, limit uint32) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > limit {
		return failure("invalid_frame", "terminal frame length is invalid")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return failure("invalid_frame", "terminal frame schema is invalid")
	}
	if dec.Decode(new(any)) != io.EOF {
		return failure("invalid_frame", "expected one terminal frame object")
	}
	return nil
}
func responseError(err error) Response {
	var coded *command.Error
	if !errors.As(err, &coded) {
		coded = &command.Error{Code: "host_error", Message: "terminal host operation failed"}
	}
	return Response{Error: coded}
}

// ServeReady owns only the terminal namespace. Canceling a control daemon's
// context is unrelated to this process. Explicit shutdown/context cancellation
// stops this host's own terminal sessions before releasing its namespace lock.
func ServeReady(ctx context.Context, dir string, ready func()) error {
	dir, err := privateDir(dir, true)
	if err != nil {
		return err
	}
	if err = testScope(dir); err != nil {
		return err
	}
	endpoint, err := listen(dir)
	if err != nil {
		return err
	}
	defer endpoint.Close()
	h, err := loadHost(dir)
	if err != nil {
		return err
	}
	defer h.close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var clients sync.WaitGroup
	// Limit active connections even when a peer stops before sending hello.
	slots := make(chan struct{}, 32)
	go func() { <-ctx.Done(); _ = endpoint.Listener.Close() }()
	defer func() { cancel(); clients.Wait() }()
	if ready != nil {
		ready()
	}
	for {
		conn, err := endpoint.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		clients.Add(1)
		go func() {
			defer clients.Done()
			defer func() { <-slots }()
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			hello := false
			for {
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				var req Request
				if readFrame(conn, &req, MaxRequest) != nil {
					return
				}
				if req.Version != ProtocolVersion || req.Namespace != namespace(dir) || (!hello && req.Op != "hello") {
					_ = writeFrame(conn, responseError(failure("protocol_mismatch", "terminal host version, namespace or handshake differs")))
					return
				}
				hello = true
				if req.Op == "attach" {
					h.serveAttachment(ctx, conn, req)
					return
				}
				result, shutdown := h.handle(ctx, req)
				err := writeFrame(conn, result)
				if shutdown {
					cancel()
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
}

// Call opens a fresh authenticated-by-filesystem connection. A lost mutation
// reply is returned to the caller; Call never retries an operation itself.
func Call(ctx context.Context, dir string, req Request) (Response, error) {
	conn, hello, err := dialHost(ctx, dir)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if req.Op == "hello" {
		return Response{Status: hello}, nil
	}
	req.Version, req.Namespace = ProtocolVersion, hello.Namespace
	if err = writeFrame(conn, req); err != nil {
		return Response{}, failure("delivery_uncertain", "host request delivery is uncertain; reconcile inventory")
	}
	var result Response
	if err = readFrame(conn, &result, MaxMessage); err != nil {
		return result, failure("delivery_uncertain", "host reply was lost; reconcile inventory")
	}
	if result.Error != nil {
		return result, result.Error
	}
	return result, nil
}

func dialHost(ctx context.Context, dir string) (net.Conn, *Status, error) {
	dir, err := privateDir(dir, false)
	if err != nil {
		return nil, nil, err
	}
	if err = testScope(dir); err != nil {
		return nil, nil, err
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socketPath(dir))
	if err != nil {
		return nil, nil, failure("host_unavailable", "native terminal host is unavailable; start or inspect it explicitly")
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	ns := namespace(dir)
	if err = writeFrame(conn, Request{Version: ProtocolVersion, Namespace: ns, Op: "hello"}); err != nil {
		return nil, nil, err
	}
	var hello Response
	if err = readFrame(conn, &hello, MaxMessage); err != nil {
		return nil, nil, err
	}
	if hello.Error != nil {
		return nil, nil, hello.Error
	}
	if hello.Status == nil || hello.Status.Version != ProtocolVersion || hello.Status.Namespace != ns {
		return nil, nil, failure("protocol_mismatch", "terminal host handshake is invalid")
	}
	ok = true
	return conn, hello.Status, nil
}
