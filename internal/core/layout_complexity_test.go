package core

import (
	"runtime"
	"strings"
	"testing"
)

func TestValidateLayoutRejectsDeepTreeWithoutExcessiveAllocation(t *testing.T) {
	resources := map[string]bool{"p": true}
	var root map[string]any = map[string]any{"type": "pane", "resource_id": "p"}
	for i := 0; i < 400; i++ {
		root = map[string]any{"type": "split", "direction": "horizontal", "ratio": 0.5, "children": []any{root, map[string]any{"type": "pane", "resource_id": "p"}}}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := validateLayout(root, resources)
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "too complex") {
		t.Fatalf("expected complexity error, got %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 12<<20 {
		t.Fatalf("deep layout allocated %d bytes, want at most 12 MiB", allocated)
	}
}
