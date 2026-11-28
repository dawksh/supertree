package core

import "testing"

func TestLayoutRejectsNoncanonicalFieldNames(t *testing.T) {
	pane := func(id string) map[string]any { return map[string]any{"type": "pane", "resource_id": id} }
	tests := map[string]map[string]any{
		"root type":        {"Type": "pane", "resource_id": "a"},
		"root resource":    {"type": "pane", "RESOURCE_ID": "a"},
		"conflicting type": {"Type": "split", "type": "pane", "resource_id": "a"},
		"nested field":     {"type": "split", "direction": "horizontal", "ratio": 0.5, "children": []any{pane("a"), map[string]any{"type": "pane", "Resource_ID": "b"}}},
	}
	for name, root := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateLayout(root, map[string]bool{"a": true, "b": true}); err == nil {
				t.Fatalf("accepted noncanonical layout: %v", root)
			}
		})
	}
}
