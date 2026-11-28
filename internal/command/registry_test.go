package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dawksh/supertree/internal/command"
)

func TestRegistryEnforcesAdvertisedArguments(t *testing.T) {
	type args struct {
		Name string `json:"name"`
	}
	r := command.New()
	calls := 0
	handler := func(_ context.Context, a args) (string, error) { calls++; return a.Name, nil }
	d := command.Definition{Name: "test.echo", Version: 1, Repeatable: true, Effects: []string{"read"}}
	if err := command.Register(r, d, handler); err != nil {
		t.Fatal(err)
	}
	if err := command.Register(r, d, handler); err == nil {
		t.Fatal("duplicate command accepted")
	}
	for _, raw := range []string{`{}`, `null`, `[]`, `{"name":null}`, `{"name":4}`, `{"NAME":"x"}`, `{"name":"x","extra":true}`, `{"name":"x"} {}`, `{`} {
		t.Run(raw, func(t *testing.T) {
			_, err := r.Invoke(context.Background(), d.Name, json.RawMessage(raw))
			var coded *command.Error
			if !errors.As(err, &coded) || coded.Code != "invalid_arguments" {
				t.Fatalf("expected invalid_arguments, got %v", err)
			}
		})
	}
	if calls != 0 {
		t.Fatal("invalid arguments reached handler")
	}
	result, err := r.Invoke(context.Background(), d.Name, json.RawMessage(`{"name":"hello"}`))
	if err != nil || result != "hello" || calls != 1 {
		t.Fatalf("valid invocation: %v, %v, calls=%d", result, err, calls)
	}
	definitions := r.Definitions()
	if len(definitions) != 1 || definitions[0].ArgumentSchema["type"] != "object" || definitions[0].ResultSchema["type"] != "string" {
		t.Fatalf("unexpected discovery: %#v", definitions)
	}
	_, err = r.Invoke(context.Background(), "missing", nil)
	var coded *command.Error
	if !errors.As(err, &coded) || coded.Code != "unknown_command" {
		t.Fatalf("unknown command: %v", err)
	}
}
