// Package command owns the discoverable actions shared by workspace clients.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

const SchemaVersion = 1

// Error is suitable for structured clients as well as human-readable output.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Definition describes the public invocation, including future macro behavior.
type Definition struct {
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	Version            int            `json:"version"`
	ArgumentSchema     map[string]any `json:"argument_schema"`
	ResultSchema       map[string]any `json:"result_schema"`
	Effects            []string       `json:"effects"`
	Recordable         bool           `json:"recordable"`
	Repeatable         bool           `json:"repeatable"`
	SensitiveArguments []string       `json:"sensitive_arguments"`
	Undoable           bool           `json:"undoable"`
}

type action struct {
	definition Definition
	invoke     func(context.Context, json.RawMessage) (any, error)
}

type Registry struct {
	actions map[string]action
}

func New() *Registry { return &Registry{actions: make(map[string]action)} }

// Register derives schemas from the same types used to decode and execute an
// action. There is no second argument model for the CLI or a future RPC client.
func Register[A, R any](r *Registry, d Definition, handler func(context.Context, A) (R, error)) error {
	if d.Name == "" || strings.ContainsAny(d.Name, " \t\r\n") || d.Version < 1 || handler == nil {
		return fmt.Errorf("invalid command definition: %q", d.Name)
	}
	if _, exists := r.actions[d.Name]; exists {
		return fmt.Errorf("duplicate command: %s", d.Name)
	}
	args, err := schema(reflect.TypeFor[A]())
	if err != nil {
		return fmt.Errorf("%s arguments: %w", d.Name, err)
	}
	if args["type"] != "object" {
		return fmt.Errorf("%s arguments must be an object", d.Name)
	}
	result, err := schema(reflect.TypeFor[R]())
	if err != nil {
		return fmt.Errorf("%s result: %w", d.Name, err)
	}
	d.ArgumentSchema, d.ResultSchema = args, result
	if d.Effects == nil {
		d.Effects = []string{}
	}
	if d.SensitiveArguments == nil {
		d.SensitiveArguments = []string{}
	}
	r.actions[d.Name] = action{d, func(ctx context.Context, raw json.RawMessage) (any, error) {
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = json.RawMessage(`{}`)
		}
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
			return nil, &Error{"invalid_arguments", "command arguments must be a JSON object"}
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, &Error{"invalid_arguments", err.Error()}
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, &Error{"invalid_arguments", "expected one JSON object"}
		}
		if err := validate(value, d.ArgumentSchema); err != nil {
			return nil, &Error{"invalid_arguments", err.Error()}
		}
		var a A
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, &Error{"invalid_arguments", err.Error()}
		}
		return handler(ctx, a)
	}}
	return nil
}

func (r *Registry) Definitions() []Definition {
	definitions := make([]Definition, 0, len(r.actions))
	for _, a := range r.actions {
		definitions = append(definitions, a.definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions
}

func (r *Registry) Invoke(ctx context.Context, name string, args json.RawMessage) (any, error) {
	a, ok := r.actions[name]
	if !ok {
		return nil, &Error{"unknown_command", fmt.Sprintf("unknown command: %s", name)}
	}
	return a.invoke(ctx, args)
}

func validate(value any, s map[string]any) error {
	switch s["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			break
		}
		properties, restricted := s["properties"].(map[string]any)
		if !restricted {
			return nil
		}
		for _, name := range s["required"].([]string) {
			if _, ok := object[name]; !ok {
				return fmt.Errorf("missing argument: %s", name)
			}
		}
		for name, item := range object {
			property, ok := properties[name].(map[string]any)
			if !ok {
				return fmt.Errorf("unknown argument: %s", name)
			}
			if err := validate(item, property); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		return nil
	case "array":
		if items, ok := value.([]any); ok {
			for _, item := range items {
				if err := validate(item, s["items"].(map[string]any)); err != nil {
					return err
				}
			}
			return nil
		}
	case "string":
		if _, ok := value.(string); ok {
			return nil
		}
	case "boolean":
		if _, ok := value.(bool); ok {
			return nil
		}
	case "integer":
		if number, ok := value.(json.Number); ok {
			if _, err := number.Int64(); err == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("expected %s", s["type"])
}

// schema intentionally supports only the concrete JSON types currently used by
// commands. Unsupported types fail registration instead of publishing a guess.
func schema(t reflect.Type) (map[string]any, error) {
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Slice:
		items, err := schema(t.Elem())
		return map[string]any{"type": "array", "items": items}, err
	case reflect.Map:
		// Command definitions expose JSON Schema documents as arbitrary objects.
		if t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.Interface {
			return map[string]any{"type": "object"}, nil
		}
	case reflect.Struct:
		properties := make(map[string]any)
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			property, err := schema(field.Type)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			properties[name] = property
			if !strings.Contains(options, "omitempty") {
				required = append(required, name)
			}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}, nil
	}
	return nil, fmt.Errorf("unsupported schema type: %s", t)
}
