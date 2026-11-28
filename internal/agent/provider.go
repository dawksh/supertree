package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/dawksh/supertree/internal/command"
)

type providerMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}
type provider struct {
	cmd      *exec.Cmd
	input    io.WriteCloser
	write    sync.Mutex
	mu       sync.Mutex
	sequence int
	pending  map[string]chan providerMessage
	done     chan struct{}
	notify   func(providerMessage)
}

func startProvider(ctx context.Context, executable string, notify func(providerMessage), stderr io.Writer) (*provider, error) {
	cmd := exec.CommandContext(ctx, executable, "app-server", "--stdio")
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		input.Close()
		return nil, err
	}
	p := &provider{cmd: cmd, input: input, pending: map[string]chan providerMessage{}, done: make(chan struct{}), notify: notify}
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			var msg providerMessage
			if json.Unmarshal(scanner.Bytes(), &msg) != nil {
				continue
			}
			if msg.Method != "" {
				notify(msg)
				continue
			}
			p.mu.Lock()
			ch := p.pending[string(msg.ID)]
			delete(p.pending, string(msg.ID))
			p.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		}
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}
func (p *provider) send(value any) error {
	p.write.Lock()
	defer p.write.Unlock()
	return json.NewEncoder(p.input).Encode(value)
}
func (p *provider) call(ctx context.Context, method string, args any) (json.RawMessage, error) {
	p.mu.Lock()
	p.sequence++
	id := fmt.Sprint(p.sequence)
	ch := make(chan providerMessage, 1)
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	if err := p.send(map[string]any{"id": json.RawMessage(id), "method": method, "params": args}); err != nil {
		return nil, err
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, &command.Error{Code: "provider_error", Message: msg.Error.Message}
		}
		return msg.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return nil, errors.New("Codex app-server exited")
	}
}
func (p *provider) reply(id json.RawMessage, result any) error {
	return p.send(map[string]any{"id": id, "result": result})
}
func (p *provider) reject(id json.RawMessage, message string) error {
	return p.send(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": message}})
}
