package store

import "testing"

func setupBFSTestStore(t *testing.T) (*Store, map[string]int64) {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.UpsertProject("bfstest", "/tmp/bfstest"); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64)
	for _, name := range []string{"root", "childA", "childB", "grandchild"} {
		id, err := s.UpsertNode(&Node{
			Project: "bfstest", Label: "Function", Name: name,
			QualifiedName: "bfstest." + name,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	// root -CALLS-> childA, root -DEFINES-> childB, childA -CALLS-> grandchild
	for _, e := range []*Edge{
		{Project: "bfstest", SourceID: ids["root"], TargetID: ids["childA"], Type: "CALLS"},
		{Project: "bfstest", SourceID: ids["root"], TargetID: ids["childB"], Type: "DEFINES"},
		{Project: "bfstest", SourceID: ids["childA"], TargetID: ids["grandchild"], Type: "CALLS"},
	} {
		if _, err := s.InsertEdge(e); err != nil {
			t.Fatal(err)
		}
	}
	return s, ids
}

// BFS clips the visited set at maxResults; the clip must be reported via
// Truncated instead of silently undercounting (callers like the Cypher
// executor's variable-length expansion aggregate over Visited).
func TestBFSTruncationSignal(t *testing.T) {
	s, ids := setupBFSTestStore(t)

	res, err := s.BFS(ids["root"], "outbound", []string{"CALLS"}, 3, 1)
	if err != nil {
		t.Fatalf("bfs: %v", err)
	}
	if len(res.Visited) != 1 {
		t.Fatalf("got %d visited, want 1", len(res.Visited))
	}
	if !res.Truncated {
		t.Fatal("BFS clipped at maxResults without setting Truncated")
	}

	full, err := s.BFS(ids["root"], "outbound", []string{"CALLS"}, 3, 200)
	if err != nil {
		t.Fatalf("bfs: %v", err)
	}
	if full.Truncated {
		t.Fatal("Truncated set although the visited set was complete")
	}
	if len(full.Visited) != 2 { // childA, grandchild
		t.Fatalf("got %d visited, want 2", len(full.Visited))
	}
}

// An empty edgeTypes list traverses all edge types (consistent with
// FindEdgesBySourceIDs). It used to silently default to CALLS only.
func TestBFSEmptyEdgeTypesTraversesAllTypes(t *testing.T) {
	s, ids := setupBFSTestStore(t)

	res, err := s.BFS(ids["root"], "outbound", nil, 2, 200)
	if err != nil {
		t.Fatalf("bfs: %v", err)
	}
	names := make(map[string]bool)
	for _, nh := range res.Visited {
		names[nh.Node.Name] = true
	}
	for _, want := range []string{"childA", "childB", "grandchild"} {
		if !names[want] {
			t.Fatalf("untyped BFS missing %s (visited: %v)", want, names)
		}
	}
}

// A cycle must not hang or blow up the recursive CTE (UNION dedups the
// (node, hop) frontier; UNION ALL enumerated paths, which is unbounded on
// cycles until the depth cap).
func TestBFSCycleTerminates(t *testing.T) {
	s, ids := setupBFSTestStore(t)
	// grandchild -CALLS-> root closes the cycle.
	if _, err := s.InsertEdge(&Edge{Project: "bfstest", SourceID: ids["grandchild"], TargetID: ids["root"], Type: "CALLS"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.BFS(ids["root"], "outbound", []string{"CALLS"}, 10, 200)
	if err != nil {
		t.Fatalf("bfs on cyclic graph: %v", err)
	}
	if len(res.Visited) == 0 {
		t.Fatal("expected visited nodes on cyclic graph")
	}
}
