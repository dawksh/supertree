package stack

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// A deliberately tiny polling protocol for this experiment, not the product
// protocol: no launch RPC, adoption, durable state, leases, or agent commands.
const maxMessage = 8 * 1024 * 1024

type Request struct {
	Op            string
	Index         int
	Width, Height int
	Key           *uv.Key
	Text          string
	Paste         bool
}
type Response struct {
	Frames []Frame
	Error  string
}

func writeMessage(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxMessage {
		return errors.New("experiment frame exceeds limit")
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(data)))
	_, err = io.Copy(w, io.MultiReader(bytes.NewReader(header), bytes.NewReader(data)))
	return err
}

func readMessage(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > maxMessage {
		return errors.New("invalid experiment frame length")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func socketPath(dir string) string { return filepath.Join(dir, "terminal-stack.sock") }

func PrivateDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&0077 != 0 {
		return errors.New("experiment state must be an existing private directory (mode 0700)")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("experiment directory must belong to this user")
	}
	return nil
}

// Serve keeps PTYs independent of client lifetime. A stalled or absent client
// never accumulates output frames. Stop is an explicit experiment-only action.
func Serve(ctx context.Context, dir string, terminals []*Terminal) error {
	if err := PrivateDirectory(dir); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socketPath(dir))
	if err != nil {
		return err
	} // Never unlink a potentially live socket to retry.
	defer listener.Close()
	if err := os.Chmod(socketPath(dir), 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() { <-ctx.Done(); listener.Close() }()
	var clients sync.WaitGroup
	defer func() { cancel(); listener.Close(); clients.Wait() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		clients.Add(1)
		go func() {
			defer clients.Done()
			defer conn.Close()
			closed := make(chan struct{})
			defer close(closed)
			go func() {
				select {
				case <-ctx.Done():
					conn.Close()
				case <-closed:
				}
			}()
			for {
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				var req Request
				if readMessage(conn, &req) != nil {
					return
				}
				var response Response
				switch req.Op {
				case "snapshot":
					for _, t := range terminals {
						response.Frames = append(response.Frames, t.Snapshot())
					}
				case "stop": // Shutdown only this explicitly addressed experiment.
					_ = writeMessage(conn, response)
					cancel()
					return
				case "input", "resize":
					if req.Index < 0 || req.Index >= len(terminals) {
						response.Error = "invalid terminal index"
						break
					}
					t := terminals[req.Index]
					var err error
					if req.Op == "input" {
						err = t.Input(req.Key, req.Text, req.Paste)
					} else {
						err = t.Resize(req.Width, req.Height)
					}
					if err != nil {
						response.Error = err.Error()
					}
				default:
					response.Error = "unknown experiment operation"
				}
				if writeMessage(conn, response) != nil {
					return
				}
			}
		}()
	}
}

type Client struct{ conn net.Conn }

func Dial(dir string) (*Client, error) {
	if err := PrivateDirectory(dir); err != nil {
		return nil, err
	}
	c, err := net.DialTimeout("unix", socketPath(dir), 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{conn: c}, nil
}
func (c *Client) Close() error { return c.conn.Close() }
func (c *Client) Call(req Request) (Response, error) {
	_ = c.conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := writeMessage(c.conn, req); err != nil {
		return Response{}, err
	}
	var result Response
	if err := readMessage(c.conn, &result); err != nil {
		return result, err
	}
	if result.Error != "" {
		return result, fmt.Errorf("terminal experiment: %s", result.Error)
	}
	return result, nil
}
