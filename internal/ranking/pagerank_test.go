package ranking

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// testStoreOrSkip opens an in-memory SQLite store for unit tests.
// Skips if sqlite driver isn't registered (defensive — registration
// happens via cgo on the build path used in this repo).
func testStoreOrSkip(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenPath(":memory:")
	if err != nil {
		t.Skipf("skip: in-memory store unavailable: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ensureProject registers the project so node FK constraints succeed.
func ensureProject(t *testing.T, st *store.Store, project string) {
	t.Helper()
	if err := st.UpsertProject(project, "/tmp/test-root"); err != nil {
		t.Fatalf("upsert project %q: %v", project, err)
	}
}

// insertNode is a test helper: upserts a Function node with the given
// qualified_name and returns its ID.
func insertNode(t *testing.T, st *store.Store, project, qn string) int64 {
	t.Helper()
	id, err := st.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Function",
		Name:          qn, // simple: name == qualified_name for tests
		QualifiedName: qn,
		FilePath:      "test.go",
	})
	if err != nil {
		t.Fatalf("upsert node %q: %v", qn, err)
	}
	return id
}

// insertEdge is a test helper: inserts a CALLS edge.
func insertEdge(t *testing.T, st *store.Store, project string, src, dst int64) {
	t.Helper()
	if _, err := st.InsertEdge(&store.Edge{
		Project:  project,
		SourceID: src,
		TargetID: dst,
		Type:     "CALLS",
	}); err != nil {
		t.Fatalf("insert edge %d->%d: %v", src, dst, err)
	}
}

// TestBidirectionalPageRank_PureSourcesStayVisible validates the Gate
// alpha finding from bench/research/pagerank_probe.py: in single-direction
// (forward) PageRank, pure-source nodes A/B that only propagate rank
// outward (no inbound personalization, no inbound edges from the graph)
// collapse to 0. The bidirectional variant runs a reverse PageRank pass
// where those same A/B nodes receive rank from C (their downstream), so
// they stay visible in final ranking.
//
// Graph: A<->B, A->C, B->C, C->D, D->E
// Query: matches "C" → personalization on C only.
//
// Expected: all 5 nodes have non-zero score; ranking order roughly
// C > (D, A, B) > E with the bidirectional sum keeping A/B above 0.
func TestBidirectionalPageRank_PureSourcesStayVisible(t *testing.T) {
	st := testStoreOrSkip(t)
	project := "test-proj"
	ensureProject(t, st, project)

	// Use 2+ char names so the tokenizer's min-length filter accepts them.
	ids := make(map[string]int64)
	for _, name := range []string{"aa", "bb", "cc", "dd", "ee"} {
		ids[name] = insertNode(t, st, project, name)
	}
	// Edges: aa<->bb, aa->cc, bb->cc, cc->dd, dd->ee
	for _, pair := range [][2]string{
		{"aa", "bb"}, {"bb", "aa"}, {"aa", "cc"}, {"bb", "cc"}, {"cc", "dd"}, {"dd", "ee"},
	} {
		insertEdge(t, st, project, ids[pair[0]], ids[pair[1]])
	}

	results, err := RankByQuery(st, project, "cc", 10)
	if err != nil {
		t.Fatalf("RankByQuery: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	scores := make(map[string]float64)
	for _, r := range results {
		scores[r.Name] = r.Score
	}

	// cc must be highest (direct personalization seed).
	if scores["cc"] <= scores["dd"] || scores["cc"] <= scores["ee"] {
		t.Errorf("cc (personalization seed) should outrank dd and ee; got cc=%.4f dd=%.4f ee=%.4f",
			scores["cc"], scores["dd"], scores["ee"])
	}

	// Critical assertion: aa and bb must have non-zero scores. In
	// single-direction forward PageRank on this graph they collapse to
	// 0 (verified in bench/research/pagerank_probe.py). Bidirectional
	// prevents that collapse by summing reverse-pass rank where aa and bb
	// are downstream of cc in the reversed graph.
	if scores["aa"] <= 0 {
		t.Errorf("aa should have non-zero score under bidirectional PageRank; got %.6f", scores["aa"])
	}
	if scores["bb"] <= 0 {
		t.Errorf("bb should have non-zero score under bidirectional PageRank; got %.6f", scores["bb"])
	}
}

// TestRankByQuery_EmptyProject returns a helpful error when the project
// has no nodes, rather than panicking.
func TestRankByQuery_EmptyProject(t *testing.T) {
	st := testStoreOrSkip(t)
	_, err := RankByQuery(st, "nonexistent", "anything", 10)
	if err == nil {
		t.Fatal("expected error for empty project, got nil")
	}
}

// TestRankByQuery_NoSeedMatch returns an error when query tokens match
// no node names or qualified names.
func TestRankByQuery_NoSeedMatch(t *testing.T) {
	st := testStoreOrSkip(t)
	project := "test-proj"
	ensureProject(t, st, project)
	insertNode(t, st, project, "helloWorld")
	insertNode(t, st, project, "goodbye")

	_, err := RankByQuery(st, project, "xyz-not-a-real-identifier", 10)
	if err == nil {
		t.Fatal("expected error for zero seed matches, got nil")
	}
}

// TestRankByQuery_TopKClamp ensures topK is clamped to [1, 200].
func TestRankByQuery_TopKClamp(t *testing.T) {
	st := testStoreOrSkip(t)
	project := "test-proj"
	ensureProject(t, st, project)
	ids := []int64{
		insertNode(t, st, project, "foo"),
		insertNode(t, st, project, "bar"),
		insertNode(t, st, project, "baz"),
	}
	insertEdge(t, st, project, ids[0], ids[1])
	insertEdge(t, st, project, ids[1], ids[2])

	// topK=0 should clamp to 1.
	results, err := RankByQuery(st, project, "foo", 0)
	if err != nil {
		t.Fatalf("topK=0 clamp: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("topK=0 should clamp to 1; got %d results", len(results))
	}

	// topK=9999 should clamp to node count (3 here, well below 200 cap).
	results, err = RankByQuery(st, project, "foo", 9999)
	if err != nil {
		t.Fatalf("topK=9999 clamp: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("topK=9999 with 3 nodes should return 3; got %d", len(results))
	}
}

// TestTokenize verifies stopword removal and length filter.
func TestTokenize(t *testing.T) {
	tokens := tokenize("how does the auth middleware work?")
	// Expected: "auth", "middleware", "work" (the/does/how dropped;
	// trailing ? stripped).
	want := map[string]bool{"auth": true, "middleware": true, "work": true}
	if len(tokens) != len(want) {
		t.Errorf("got %d tokens %v, want %d %v", len(tokens), tokens, len(want), want)
	}
	for _, tok := range tokens {
		if !want[tok] {
			t.Errorf("unexpected token %q in %v", tok, tokens)
		}
	}
}
