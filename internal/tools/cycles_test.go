package tools

import (
	"testing"
)

// --- Pure function tests ---

func TestExtractTopLevelCrate(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"doomper/src/recorder.rs", "doomper"},
		{"ship-os/src/components/layout/Navigation.tsx", "ship-os"},
		{"redacted-platform-terraform/core/modules/environment/main.tf", "redacted-platform-terraform"},
		{"standalone.rs", "standalone.rs"},    // no slash → return full path
		{"svc-api/src/handler.rs", "svc-api"}, // standard crate
		{"", ""},                              // empty
		{"a/b", "a"},                          // minimal path
		{"windows\\path\\file.go", "windows"}, // backslash normalized
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := extractTopLevelCrate(tt.path)
			if got != tt.want {
				t.Errorf("extractTopLevelCrate(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestExtractSubpackage(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"doomper/src/recorder.rs", "doomper/src"},
		{"doomper/tests/test_recorder.rs", "doomper/tests"},
		{"standalone.rs", "standalone.rs"},         // single segment
		{"a/b", "a"},                               // two segments → first only
		{"a/b/c/d", "a/b"},                         // standard: first two
		{"windows\\path\\file.go", "windows/path"}, // backslash normalized
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := extractSubpackage(tt.path)
			if got != tt.want {
				t.Errorf("extractSubpackage(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestCanonicalCycleKey(t *testing.T) {
	tests := []struct {
		name  string
		cycle []string
		want  string
	}{
		{"A->B->C rotation normalized", []string{"B", "C", "A"}, "A -> B -> C"},
		{"A->B->C already canonical", []string{"A", "B", "C"}, "A -> B -> C"},
		{"single node", []string{"A"}, "A"},
		{"empty", []string{}, ""},
		{"two nodes", []string{"B", "A"}, "A -> B"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canonicalCycleKey(tt.cycle)
			if got != tt.want {
				t.Errorf("canonicalCycleKey(%v) = %q, want %q", tt.cycle, got, tt.want)
			}
		})
	}
}

func TestFindCycles(t *testing.T) {
	t.Run("simple two-node cycle", func(t *testing.T) {
		graph := map[string]map[string]bool{
			"A": {"B": true},
			"B": {"A": true},
		}
		cycles := findCycles(graph, 4)
		if len(cycles) != 1 {
			t.Fatalf("expected 1 cycle, got %d: %v", len(cycles), cycles)
		}
		if len(cycles[0]) != 2 {
			t.Errorf("expected cycle length 2, got %d", len(cycles[0]))
		}
	})

	t.Run("three-node cycle", func(t *testing.T) {
		graph := map[string]map[string]bool{
			"A": {"B": true},
			"B": {"C": true},
			"C": {"A": true},
		}
		cycles := findCycles(graph, 4)
		if len(cycles) != 1 {
			t.Fatalf("expected 1 cycle, got %d: %v", len(cycles), cycles)
		}
		if len(cycles[0]) != 3 {
			t.Errorf("expected cycle length 3, got %d", len(cycles[0]))
		}
	})

	t.Run("no cycles in DAG", func(t *testing.T) {
		graph := map[string]map[string]bool{
			"A": {"B": true},
			"B": {"C": true},
		}
		cycles := findCycles(graph, 4)
		if len(cycles) != 0 {
			t.Errorf("expected 0 cycles in DAG, got %d: %v", len(cycles), cycles)
		}
	})

	t.Run("max_depth limits detection", func(t *testing.T) {
		// 4-node cycle should not be found with maxDepth=3
		graph := map[string]map[string]bool{
			"A": {"B": true},
			"B": {"C": true},
			"C": {"D": true},
			"D": {"A": true},
		}
		cycles := findCycles(graph, 3)
		if len(cycles) != 0 {
			t.Errorf("expected 0 cycles at maxDepth=3 for 4-node cycle, got %d", len(cycles))
		}

		// Should find it at depth 4
		cycles = findCycles(graph, 4)
		if len(cycles) != 1 {
			t.Errorf("expected 1 cycle at maxDepth=4, got %d", len(cycles))
		}
	})

	t.Run("deduplicates rotations", func(t *testing.T) {
		// A->B->C->A and B->C->A->B are the same cycle
		graph := map[string]map[string]bool{
			"A": {"B": true},
			"B": {"C": true},
			"C": {"A": true},
		}
		cycles := findCycles(graph, 4)
		if len(cycles) != 1 {
			t.Errorf("expected 1 unique cycle (rotations deduplicated), got %d", len(cycles))
		}
	})

	t.Run("empty graph", func(t *testing.T) {
		graph := map[string]map[string]bool{}
		cycles := findCycles(graph, 4)
		if len(cycles) != 0 {
			t.Errorf("expected 0 cycles in empty graph, got %d", len(cycles))
		}
	})
}

// --- Graph-dependent tests ---

func TestBuildDependencyGraph(t *testing.T) {
	st, _ := setupSecurityGraph(t)
	defer st.Close()

	t.Run("crate level groups by top-level dir", func(t *testing.T) {
		edgeTypes := []string{"CALLS", "HTTP_CALLS"}
		graph := buildDependencyGraph(st, "test", "crate", edgeTypes)

		// svc-api internal calls should be excluded (same crate)
		// Cross-crate: process_order (svc-orders) -> send_notification (svc-notify) via HTTP_CALLS
		if deps, ok := graph["svc-orders"]; ok {
			if !deps["svc-notify"] {
				t.Error("expected svc-orders -> svc-notify edge")
			}
		} else {
			t.Error("expected svc-orders in dependency graph")
		}

		// svc-api internal calls (handle_request -> authenticate) should NOT appear
		if deps, ok := graph["svc-api"]; ok {
			if deps["svc-api"] {
				t.Error("same-crate dependency should be excluded")
			}
		}
	})

	t.Run("file level includes all cross-file edges", func(t *testing.T) {
		edgeTypes := []string{"CALLS"}
		graph := buildDependencyGraph(st, "test", "file", edgeTypes)

		// handle_request (handler.rs) -> authenticate (auth.rs) should be an edge
		handlerDeps, ok := graph["svc-api/src/handler.rs"]
		if !ok {
			t.Fatal("expected svc-api/src/handler.rs in file-level graph")
		}
		if !handlerDeps["svc-api/src/auth.rs"] {
			t.Error("expected handler.rs -> auth.rs edge")
		}
	})
}
