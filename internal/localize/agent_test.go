package localize

import (
	"reflect"
	"testing"

	"github.com/brandyn-s/code-graph/internal/ranking"
	"github.com/brandyn-s/code-graph/internal/store"
)

func testStoreOrSkip(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenPath(":memory:")
	if err != nil {
		t.Skipf("skip: in-memory store unavailable: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func ensureProject(t *testing.T, st *store.Store, project string) {
	t.Helper()
	if err := st.UpsertProject(project, "/tmp/test-root"); err != nil {
		t.Fatalf("upsert project %q: %v", project, err)
	}
}

func insertNode(t *testing.T, st *store.Store, project, qn, label, file string) int64 {
	t.Helper()
	id, err := st.UpsertNode(&store.Node{
		Project:       project,
		Label:         label,
		Name:          qn,
		QualifiedName: qn,
		FilePath:      file,
	})
	if err != nil {
		t.Fatalf("upsert %s: %v", qn, err)
	}
	return id
}

func insertEdge(t *testing.T, st *store.Store, project, etype string, src, dst int64) {
	t.Helper()
	if _, err := st.InsertEdge(&store.Edge{
		Project:  project,
		SourceID: src,
		TargetID: dst,
		Type:     etype,
	}); err != nil {
		t.Fatalf("insert %s edge %d->%d: %v", etype, src, dst, err)
	}
}

// TestCodeLocalize_BFSExpansion validates that BFS from a seed reaches
// nodes that are 1-2 hops away via allowed edge types and that the
// distance field reflects the actual shortest path.
//
// Graph: handler --CALLS--> validator --CALLS--> sanitizer
//
//	\--CALLS--> logger
//
// Query "validator" should: seed validator, expand to handler (1 hop
// via CALLS) + sanitizer + logger (1 hop via CALLS), total 4 entities.
func TestCodeLocalize_BFSExpansion(t *testing.T) {
	st := testStoreOrSkip(t)
	project := "loc-test"
	ensureProject(t, st, project)

	handler := insertNode(t, st, project, "handler", "Function", "h.go")
	validator := insertNode(t, st, project, "validator", "Function", "v.go")
	sanitizer := insertNode(t, st, project, "sanitizer", "Function", "s.go")
	logger := insertNode(t, st, project, "logger", "Function", "l.go")

	insertEdge(t, st, project, "CALLS", handler, validator)
	insertEdge(t, st, project, "CALLS", validator, sanitizer)
	insertEdge(t, st, project, "CALLS", validator, logger)

	results, err := CodeLocalize(st, project, "validator", 3, 10)
	if err != nil {
		t.Fatalf("CodeLocalize: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 entities (validator + 3 reachable), got %d: %+v", len(results), results)
	}

	byName := map[string]LocalizedEntity{}
	for _, r := range results {
		byName[r.Name] = r
	}

	if got := byName["validator"].Distance; got != 0 {
		t.Errorf("validator should be distance 0 (seed); got %d", got)
	}
	if got := byName["handler"].Distance; got != 1 {
		t.Errorf("handler should be distance 1 (one CALLS hop); got %d", got)
	}
	if got := byName["sanitizer"].Distance; got != 1 {
		t.Errorf("sanitizer should be distance 1; got %d", got)
	}
	if got := byName["logger"].Distance; got != 1 {
		t.Errorf("logger should be distance 1; got %d", got)
	}

	// validator (seed) should be ranked highest.
	if results[0].Name != "validator" {
		t.Errorf("seed should rank highest; got top result %q", results[0].Name)
	}
}

// A seed that matches multiple independent query anchors must outrank a
// generic exact-name seed. The localizer previously discarded the lexical
// match quality established by ranking.MatchSeedNodes and assigned every seed
// score 1.0, so stable file-path order decided this case.
func TestCodeLocalizeRanksMultiAnchorSeedAboveGenericExactName(t *testing.T) {
	st := testStoreOrSkip(t)
	project := "multi-anchor-seed"
	ensureProject(t, st, project)

	insertNode(t, st, project, "pkg.generic.predict", "Method", "a_generic.py")
	insertNode(t, st, project, "pkg.IsolationForest.predict", "Method", "z_iforest.py")

	results, err := CodeLocalize(st, project, "IsolationForest predict", 0, 10)
	if err != nil {
		t.Fatalf("CodeLocalize: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}
	if got := results[0].FilePath; got != "z_iforest.py" {
		t.Fatalf("multi-anchor seed ranked behind generic seed: got %q first; results=%+v", got, results)
	}
}

// TestCodeLocalize_DepthClamp ensures BFS does not expand beyond `depth`.
//
// Graph: a --CALLS--> b --CALLS--> c --CALLS--> d
// Query "a" with depth=1 should reach a + b only; depth=2 reaches a+b+c;
// depth=3 reaches all four.
func TestCodeLocalize_DepthClamp(t *testing.T) {
	st := testStoreOrSkip(t)
	project := "depth-test"
	ensureProject(t, st, project)

	a := insertNode(t, st, project, "alpha", "Function", "f.go")
	b := insertNode(t, st, project, "beta", "Function", "f.go")
	c := insertNode(t, st, project, "gamma", "Function", "f.go")
	d := insertNode(t, st, project, "delta", "Function", "f.go")
	insertEdge(t, st, project, "CALLS", a, b)
	insertEdge(t, st, project, "CALLS", b, c)
	insertEdge(t, st, project, "CALLS", c, d)

	d1, err := CodeLocalize(st, project, "alpha", 1, 10)
	if err != nil {
		t.Fatalf("depth=1: %v", err)
	}
	if len(d1) != 2 {
		t.Errorf("depth=1 should reach 2 nodes (alpha + beta); got %d: %+v", len(d1), d1)
	}

	d3, err := CodeLocalize(st, project, "alpha", 3, 10)
	if err != nil {
		t.Fatalf("depth=3: %v", err)
	}
	if len(d3) != 4 {
		t.Errorf("depth=3 should reach all 4 nodes; got %d: %+v", len(d3), d3)
	}
}

// TestCodeLocalize_EdgeTypeFilter ensures BFS does not traverse edges
// outside AllowedEdgeTypes (e.g., FILE_CHANGES_WITH must not pull in
// historically-coupled files into structural localization).
func TestCodeLocalize_EdgeTypeFilter(t *testing.T) {
	st := testStoreOrSkip(t)
	project := "filter-test"
	ensureProject(t, st, project)

	mainFn := insertNode(t, st, project, "mainFn", "Function", "f.go")
	helper := insertNode(t, st, project, "helper", "Function", "f.go")
	noisy := insertNode(t, st, project, "noisy", "Function", "f.go")

	insertEdge(t, st, project, "CALLS", mainFn, helper)
	// FILE_CHANGES_WITH is intentionally excluded from AllowedEdgeTypes.
	insertEdge(t, st, project, "FILE_CHANGES_WITH", mainFn, noisy)

	results, err := CodeLocalize(st, project, "mainFn", 3, 10)
	if err != nil {
		t.Fatalf("CodeLocalize: %v", err)
	}

	for _, r := range results {
		if r.Name == "noisy" {
			t.Errorf("noisy should NOT be reached via FILE_CHANGES_WITH (not in AllowedEdgeTypes); got %+v", r)
		}
	}
}

// TestCodeLocalize_NoSeedMatch returns a clear error when query tokens
// match nothing in the graph, not an empty result that callers might
// mistake for "this issue has no relevant code".
func TestCodeLocalize_NoSeedMatch(t *testing.T) {
	st := testStoreOrSkip(t)
	project := "noseed"
	ensureProject(t, st, project)
	insertNode(t, st, project, "actualFunction", "Function", "f.go")

	_, err := CodeLocalize(st, project, "totally-unrelated-xyz", 3, 10)
	if err == nil {
		t.Fatal("expected error for no seed match, got nil")
	}
}

func TestSortByScoreDescBreaksTiesByStableSourceIdentity(t *testing.T) {
	want := []string{"a.go", "a.go", "b.go"}
	inputs := [][]LocalizedEntity{
		{
			{FilePath: "b.go", StartLine: 1, EndLine: 2, QualifiedName: "beta", Score: 1},
			{FilePath: "a.go", StartLine: 8, EndLine: 9, QualifiedName: "zeta", Score: 1},
			{FilePath: "a.go", StartLine: 3, EndLine: 4, QualifiedName: "alpha", Score: 1},
		},
		{
			{FilePath: "a.go", StartLine: 3, EndLine: 4, QualifiedName: "alpha", Score: 1},
			{FilePath: "b.go", StartLine: 1, EndLine: 2, QualifiedName: "beta", Score: 1},
			{FilePath: "a.go", StartLine: 8, EndLine: 9, QualifiedName: "zeta", Score: 1},
		},
	}

	for inputIndex, results := range inputs {
		sortByScoreDesc(results)
		for index, path := range want {
			if results[index].FilePath != path {
				t.Fatalf("input %d rank %d: got %q, want %q; results=%+v", inputIndex, index+1, results[index].FilePath, path, results)
			}
		}
		if results[0].StartLine != 3 || results[1].StartLine != 8 {
			t.Fatalf("input %d: same-file ties must use source coordinates; results=%+v", inputIndex, results)
		}
	}
}

func TestBFSExpansionIsIndependentOfEdgeLoadOrder(t *testing.T) {
	seed := ranking.RankedNode{ID: 1, Score: 1.0}
	edges := []struct {
		source int64
		target int64
		kind   string
	}{
		{source: 1, target: 3, kind: "CALLS"},
		{source: 1, target: 2, kind: "IMPORTS"},
		{source: 2, target: 4, kind: "DEFINES"},
		{source: 3, target: 4, kind: "CALLS"},
	}

	build := func(reverse bool) map[int64]*localizedAccumulator {
		out := map[int64][]edgeRef{}
		in := map[int64][]edgeRef{}
		for offset := range edges {
			index := offset
			if reverse {
				index = len(edges) - 1 - offset
			}
			edge := edges[index]
			out[edge.source] = append(out[edge.source], edgeRef{other: edge.target, etype: edge.kind})
			in[edge.target] = append(in[edge.target], edgeRef{other: edge.source, etype: edge.kind})
		}
		for id := range out {
			sortEdgeRefs(out[id])
		}
		for id := range in {
			sortEdgeRefs(in[id])
		}
		visited := map[int64]*localizedAccumulator{}
		bfsExpand(seed, 2, out, in, visited)
		return visited
	}

	forward := build(false)
	reversed := build(true)
	if !reflect.DeepEqual(forward, reversed) {
		t.Fatalf("edge load order changed BFS result:\nforward=%+v\nreversed=%+v", forward, reversed)
	}
}
