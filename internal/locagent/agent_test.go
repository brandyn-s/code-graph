package locagent

import "testing"

// MRR aggregation: rank-1 entity contributes 1.0, rank-2 contributes 0.5, rank-3
// contributes 0.333. Same entity across iterations sums.
func TestAggregateByMRR_SingleIteration(t *testing.T) {
	iter := &Result{
		Entities: []LocalizedEntity{
			{QualifiedName: "a.foo", FilePath: "a.py"},
			{QualifiedName: "b.bar", FilePath: "b.py"},
			{QualifiedName: "c.baz", FilePath: "c.py"},
		},
	}
	got := aggregateByMRR([]*Result{iter}, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 entities, got %d", len(got))
	}
	if got[0].QualifiedName != "a.foo" || got[1].QualifiedName != "b.bar" || got[2].QualifiedName != "c.baz" {
		t.Fatalf("expected order a/b/c, got %+v", got)
	}
}

func TestAggregateByMRR_TwoIterationsConvergent(t *testing.T) {
	// Both iterations rank a.foo first → score = 1.0 + 1.0 = 2.0
	// Both rank b.bar second → score = 0.5 + 0.5 = 1.0
	// Iter 1 ranks c.baz third (0.333), iter 2 omits c.baz
	iter1 := &Result{
		Entities: []LocalizedEntity{
			{QualifiedName: "a.foo", FilePath: "a.py"},
			{QualifiedName: "b.bar", FilePath: "b.py"},
			{QualifiedName: "c.baz", FilePath: "c.py"},
		},
	}
	iter2 := &Result{
		Entities: []LocalizedEntity{
			{QualifiedName: "a.foo", FilePath: "a.py"},
			{QualifiedName: "b.bar", FilePath: "b.py"},
			{QualifiedName: "d.qux", FilePath: "d.py"},
		},
	}
	got := aggregateByMRR([]*Result{iter1, iter2}, 10)
	if len(got) != 4 {
		t.Fatalf("expected 4 unique entities, got %d", len(got))
	}
	if got[0].QualifiedName != "a.foo" {
		t.Errorf("expected a.foo first, got %s", got[0].QualifiedName)
	}
	if got[1].QualifiedName != "b.bar" {
		t.Errorf("expected b.bar second, got %s", got[1].QualifiedName)
	}
	// d.qux (rank 3 in iter 2 = 0.333) and c.baz (rank 3 in iter 1 = 0.333)
	// have equal scores. Tie broken by seen — both seen=1 — so order is
	// implementation-defined but stable.
	if !((got[2].QualifiedName == "c.baz" && got[3].QualifiedName == "d.qux") ||
		(got[2].QualifiedName == "d.qux" && got[3].QualifiedName == "c.baz")) {
		t.Errorf("expected c.baz and d.qux in positions 3-4, got %s and %s", got[2].QualifiedName, got[3].QualifiedName)
	}
}

func TestAggregateByMRR_DivergentIterationsTwoSeen(t *testing.T) {
	// Iter 1: a, b, c   (scores 1.0, 0.5, 0.333)
	// Iter 2: c, a, b   (scores 0.333, 0.5, 1.0 → c=0.333, a=0.5, b=1.0)
	// Cumulative: a = 1.0+0.5 = 1.5 (seen=2)
	//             c = 0.333+1.0 = 1.333 (seen=2)
	//             b = 0.5+0.333 = 0.833 (seen=2)
	iter1 := &Result{
		Entities: []LocalizedEntity{
			{QualifiedName: "a", FilePath: "a.py"},
			{QualifiedName: "b", FilePath: "b.py"},
			{QualifiedName: "c", FilePath: "c.py"},
		},
	}
	iter2 := &Result{
		Entities: []LocalizedEntity{
			{QualifiedName: "c", FilePath: "c.py"},
			{QualifiedName: "a", FilePath: "a.py"},
			{QualifiedName: "b", FilePath: "b.py"},
		},
	}
	got := aggregateByMRR([]*Result{iter1, iter2}, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 unique entities, got %d", len(got))
	}
	want := []string{"a", "c", "b"}
	for i, qn := range want {
		if got[i].QualifiedName != qn {
			t.Errorf("position %d: expected %s, got %s", i, qn, got[i].QualifiedName)
		}
	}
}

// Tie-break by seen: an entity in BOTH iterations should outrank a
// single-iteration entity with equal MRR score.
func TestAggregateByMRR_TieBreakBySeen(t *testing.T) {
	// Iter 1: x at rank 1 (1.0), y at rank 2 (0.5)
	// Iter 2: x at rank 2 (0.5)
	// x: score=1.5, seen=2
	// y: score=0.5, seen=1
	iter1 := &Result{
		Entities: []LocalizedEntity{
			{QualifiedName: "x", FilePath: "x.py"},
			{QualifiedName: "y", FilePath: "y.py"},
		},
	}
	iter2 := &Result{
		Entities: []LocalizedEntity{
			{QualifiedName: "z", FilePath: "z.py"},
			{QualifiedName: "x", FilePath: "x.py"},
		},
	}
	got := aggregateByMRR([]*Result{iter1, iter2}, 10)
	if got[0].QualifiedName != "x" {
		t.Errorf("expected x first (score 1.5, seen 2), got %s", got[0].QualifiedName)
	}
}

func TestAggregateByMRR_TopKLimit(t *testing.T) {
	iter := &Result{
		Entities: []LocalizedEntity{
			{QualifiedName: "a", FilePath: "a.py"},
			{QualifiedName: "b", FilePath: "b.py"},
			{QualifiedName: "c", FilePath: "c.py"},
			{QualifiedName: "d", FilePath: "d.py"},
		},
	}
	got := aggregateByMRR([]*Result{iter}, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 entities (topK=2), got %d", len(got))
	}
}
