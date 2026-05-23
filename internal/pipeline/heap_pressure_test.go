package pipeline

import (
	"context"
	"errors"
	"testing"
)

// TestHeapLimitBytesParsing pins the env-var parsing contract. Unset,
// empty, "0", and non-numeric values all return 0 (disabled). A valid
// positive integer returns its byte-equivalent. Read each call so test
// flips of the env take effect without process restart.
func TestHeapLimitBytesParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want uint64
	}{
		{"", 0},                 // unset → disabled
		{"0", 0},                // explicit zero → disabled
		{"not-a-number", 0},     // garbage → disabled (fail-safe)
		{"1", 1 << 20},          // 1 MB
		{"4096", 4096 << 20},    // 4 GB
		{"16384", 16384 << 20},  // 16 GB
	}
	for _, c := range cases {
		t.Setenv("CODE_GRAPH_HEAP_LIMIT_MB", c.raw)
		got := heapLimitBytes()
		if got != c.want {
			t.Errorf("CODE_GRAPH_HEAP_LIMIT_MB=%q: expected %d, got %d", c.raw, c.want, got)
		}
	}
}

// TestHeapPressureCheckDisabledByDefault verifies the production default
// (env unset) is a no-op — checkCancel returns nil even if HeapAlloc is
// non-zero. Indexing must not regress when the operator hasn't opted in
// to the limit.
func TestHeapPressureCheckDisabledByDefault(t *testing.T) {
	t.Setenv("CODE_GRAPH_HEAP_LIMIT_MB", "")
	p := &Pipeline{ctx: context.Background()}
	if err := p.checkCancel(); err != nil {
		t.Fatalf("unset env should disable heap check, got %v", err)
	}
}

// TestHeapPressureCheckTrips uses an absurdly low limit (1 MB) so the
// running test binary's own heap exceeds it. Asserts that checkCancel
// returns the sentinel ErrHeapPressure, which the pass orchestrator
// treats like a context cancel — clean abort, not OOM.
func TestHeapPressureCheckTrips(t *testing.T) {
	t.Setenv("CODE_GRAPH_HEAP_LIMIT_MB", "1")
	p := &Pipeline{ctx: context.Background()}
	err := p.checkCancel()
	if err == nil {
		t.Fatal("expected heap-pressure error with limit=1MB, got nil")
	}
	if !errors.Is(err, ErrHeapPressure) {
		t.Errorf("expected ErrHeapPressure sentinel, got %v", err)
	}
}

// TestHeapPressureCheckCancelStillFires confirms a cancelled context
// short-circuits ahead of the heap check. Both signals abort the pass;
// context-cancel must win so callers see the user's intent rather than
// a misleading heap-pressure message.
func TestHeapPressureCheckCancelStillFires(t *testing.T) {
	t.Setenv("CODE_GRAPH_HEAP_LIMIT_MB", "1") // would otherwise trip
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Pipeline{ctx: ctx}
	err := p.checkCancel()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled to win, got %v", err)
	}
}
