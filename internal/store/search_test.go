package store

import "testing"

func TestPaginateResults_NegativeOffset(t *testing.T) {
	results := make([]*SearchResult, 5)
	for i := range results {
		results[i] = &SearchResult{Node: &Node{Name: "n"}}
	}

	out := paginateResults(results, -1, 3)
	if out.Total != 5 {
		t.Fatalf("total = %d, want 5", out.Total)
	}
	if len(out.Results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(out.Results))
	}
}

func TestPaginateResults_NegativeLimit(t *testing.T) {
	results := make([]*SearchResult, 5)
	for i := range results {
		results[i] = &SearchResult{Node: &Node{Name: "n"}}
	}

	out := paginateResults(results, 2, -1)
	if out.Total != 5 {
		t.Fatalf("total = %d, want 5", out.Total)
	}
	if len(out.Results) != 0 {
		t.Fatalf("len(results) = %d, want 0", len(out.Results))
	}
}

func TestPaginateResults_BothNegative(t *testing.T) {
	results := make([]*SearchResult, 5)
	for i := range results {
		results[i] = &SearchResult{Node: &Node{Name: "n"}}
	}

	out := paginateResults(results, -10, -5)
	if out.Total != 5 {
		t.Fatalf("total = %d, want 5", out.Total)
	}
	if len(out.Results) != 0 {
		t.Fatalf("len(results) = %d, want 0", len(out.Results))
	}
}
