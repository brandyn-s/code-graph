package store

import (
	"reflect"
	"testing"
)

// TestLouvainDeterministic pins reproducibility: the same input must always
// produce the same community assignment. Before the fixed-seed RNG and the
// sorted map-key iteration, the partition (and the community IDs) varied run to
// run because of Go's randomized map iteration and auto-seeded global rand,
// which made MEMBER_OF edges non-reproducible across re-indexes.
func TestLouvainDeterministic(t *testing.T) {
	// Three dense triangles (A:1-3, B:4-6, C:7-9) joined by two weak bridges.
	nodes := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9}
	edges := []louvainEdge{
		{src: 1, dst: 2}, {src: 2, dst: 3}, {src: 1, dst: 3},
		{src: 4, dst: 5}, {src: 5, dst: 6}, {src: 4, dst: 6},
		{src: 7, dst: 8}, {src: 8, dst: 9}, {src: 7, dst: 9},
		{src: 3, dst: 4}, // bridge A-B
		{src: 6, dst: 7}, // bridge B-C
	}

	first := louvain(nodes, edges)
	if len(first) != len(nodes) {
		t.Fatalf("expected %d assignments, got %d", len(nodes), len(first))
	}

	// Many repeats: any residual map-order or RNG nondeterminism would surface
	// as a mismatch in at least one of these runs with high probability.
	for run := 0; run < 25; run++ {
		got := louvain(nodes, edges)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs from first run:\n first=%v\n got  =%v", run, first, got)
		}
	}
}
