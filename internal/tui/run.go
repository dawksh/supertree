package tui

import (
	"context"
	"encoding/json"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/core"
	"github.com/dawksh/supertree/internal/rpc"
	"io"
	"time"
)

type terminalExecution struct{ run func() error }

func (t terminalExecution) Run() error          { return t.run() }
func (t terminalExecution) SetStdin(io.Reader)  {}
func (t terminalExecution) SetStdout(io.Writer) {}
func (t terminalExecution) SetStderr(io.Writer) {}

func Run(ctx context.Context, options Options, socket string, meta rpc.Meta, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	options.Context = ctx
	program := tea.NewProgram(New(options), tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cursor := core.Cursor{}
		for ctx.Err() == nil {
			params, _ := json.Marshal(cursor)
			err := rpc.Stream(ctx, socket, params, meta, func(data json.RawMessage) error {
				var envelope struct {
					Epoch    string `json:"epoch"`
					Sequence int64  `json:"sequence"`
					Type     string `json:"type"`
				}
				if err := json.Unmarshal(data, &envelope); err != nil {
					return err
				}
				if envelope.Type == "resnapshot_required" {
					cursor = core.Cursor{}
					return &command.Error{Code: "resnapshot", Message: "taking a fresh workspace snapshot"}
				}
				if envelope.Epoch != "" {
					cursor.Epoch, cursor.After = envelope.Epoch, envelope.Sequence
				}
				program.Send(connectionMsg{Connected: true})
				program.Send(invalidateMsg{})
				return nil
			})
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				program.Send(connectionMsg{Message: "Core disconnected; reconnecting. Terminals keep running."})
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	final, err := program.Run()
	cancel()
	<-done
	if model, ok := final.(Model); ok && model.view.ID != "" {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_, _ = options.Invoke(closeCtx, "view.close", core.ViewTarget{ViewID: model.view.ID})
	}
	return err
}
