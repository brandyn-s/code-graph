package cypher

import (
	"fmt"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

// buildNFunctions creates a store with n Function nodes (no edges needed).
func buildNFunctions(t *testing.T, n int) *store.Store {
	t.Helper()
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := s.UpsertNode(&store.Node{
			Project: "test", Label: "Function",
			Name:          fmt.Sprintf("fn%05d", i),
			QualifiedName: fmt.Sprintf("test.fn%05d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}

// TestBattery_AggregationNotUndercountedAtCap pins that COUNT over a node set
// larger than the default row cap (200) returns the TRUE count — not a count of
// the capped rows. The README warned "aggregations (COUNT) may undercount at
// the default cap"; this asserts the aggregation path's higher cap is honored.
func TestBattery_AggregationNotUndercountedAtCap(t *testing.T) {
	const n = 300 // > defaultMaxRows (200)
	s := buildNFunctions(t, n)
	defer s.Close()

	exec := &Executor{Store: s} // default MaxRows => 200
	res, err := exec.Execute(`MATCH (f:Function) RETURN COUNT(f) AS c`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("COUNT should yield exactly 1 row, got %d", len(res.Rows))
	}
	got, ok := asInt(res.Rows[0]["c"])
	if !ok {
		t.Fatalf("COUNT value not numeric: %#v", res.Rows[0]["c"])
	}
	if got != n {
		t.Errorf("SILENT UNDERCOUNT: COUNT(f)=%d, want %d (capped at default rows?)", got, n)
	}
}

// TestBattery_TruncationIsSurfacedNotSilent pins that when the row cap drops
// rows, the result is FLAGGED Truncated — never a silent sample. A silent
// truncation (Truncated=false while rows were dropped) is a wrong-answer bug.
func TestBattery_TruncationIsSurfacedNotSilent(t *testing.T) {
	const n = 300
	s := buildNFunctions(t, n)
	defer s.Close()

	// MaxRows=50 => non-aggregation cap = 50*2 = 100 < 300, forcing a drop.
	exec := &Executor{Store: s, MaxRows: 50}
	res, err := exec.Execute(`MATCH (f:Function) RETURN f.name`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows) >= n {
		t.Skipf("no rows dropped (returned %d of %d) — cap did not engage", len(res.Rows), n)
	}
	if !res.Truncated {
		t.Errorf("SILENT TRUNCATION: returned %d of %d rows but Truncated=false (EffectiveCap=%d)",
			len(res.Rows), n, res.EffectiveCap)
	}
}
