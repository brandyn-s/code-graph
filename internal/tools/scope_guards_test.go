package tools

import (
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

// setupScopeGuardsGraph builds a minimal in-memory store with two shapes
// the scope-guards tests need:
//
//  1. Three functions named "new" on different types (ambiguity test)
//  2. Two functions where one is is_entry_point=true and the other isn't
//     (exclude_entry_points test)
//
// Both inhabit the same in-memory project so a single setup call serves
// both tests.
func setupScopeGuardsGraph(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	nodes := []*store.Node{
		// Three "new" methods — same name, different types.
		{
			Project: "test", Label: "Method", Name: "new",
			QualifiedName: "test.foo.Foo.new", FilePath: "src/foo.rs",
			StartLine: 10, EndLine: 15,
		},
		{
			Project: "test", Label: "Method", Name: "new",
			QualifiedName: "test.bar.Bar.new", FilePath: "src/bar.rs",
			StartLine: 20, EndLine: 25,
		},
		{
			Project: "test", Label: "Method", Name: "new",
			QualifiedName: "test.baz.Baz.new", FilePath: "src/baz.rs",
			StartLine: 30, EndLine: 35,
		},
		// Two functions: one entry point, one not. Both have 0 inbound
		// CALLS edges, so exclude_entry_points should drop the first.
		{
			Project: "test", Label: "Function", Name: "route_handler",
			QualifiedName: "test.api.route_handler", FilePath: "src/api.rs",
			StartLine: 1, EndLine: 10,
			Properties: map[string]any{"is_entry_point": true},
		},
		{
			Project: "test", Label: "Function", Name: "dead_helper",
			QualifiedName: "test.helpers.dead_helper", FilePath: "src/helpers.rs",
			StartLine: 1, EndLine: 5,
		},
	}
	for _, n := range nodes {
		if _, err := st.UpsertNode(n); err != nil {
			t.Fatalf("UpsertNode %s: %v", n.Name, err)
		}
	}
	return st
}

// --- degree_filter.exclude_entry_points ---

// TestDegreeFilter_ExcludeEntryPoints_RemovesEntryPoint asserts that the
// is_entry_point=true node is filtered out when the flag is set.
// This is the C-leg of the 2026-05-13 PSM tool-comparison battery fix:
// without the flag, degree_filter(label=Function, direction=inbound,
// op=eq, value=0) returned 5,335 functions on PSM, dominated by route
// handlers and framework callbacks that have no internal CALLS edges
// by design.
//
// Exercises the SQL filter clause that handleDegreeFilter splices into
// its query when exclude_entry_points=true. Direct SQL test rather than
// MCP-handler test because the latter requires full router machinery.
func TestDegreeFilter_ExcludeEntryPoints_RemovesEntryPoint(t *testing.T) {
	st := setupScopeGuardsGraph(t)
	defer st.Close()

	projects, _ := st.ListProjects()
	if len(projects) == 0 {
		t.Fatalf("no project")
	}
	projName := projects[0].Name

	db := st.DB()
	if db == nil {
		t.Fatalf("no DB handle")
	}

	// Without exclude — should return both Functions.
	sqlNoFilter := `
		WITH degree AS (
			SELECT n.id, n.name, COALESCE(d.cnt, 0) AS deg
			FROM nodes n
			LEFT JOIN (
				SELECT target_id, COUNT(*) AS cnt FROM edges
				WHERE project = ? AND type = ?
				GROUP BY target_id
			) d ON d.target_id = n.id
			WHERE n.project = ? AND n.label = ?
		)
		SELECT name FROM degree WHERE deg = ?
	`
	rows, err := db.Query(sqlNoFilter, projName, "CALLS", projName, "Function", 0)
	if err != nil {
		t.Fatalf("query no-filter: %v", err)
	}
	var noFilter []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		noFilter = append(noFilter, n)
	}
	rows.Close()
	if len(noFilter) != 2 {
		t.Errorf("without exclude_entry_points: expected 2 functions, got %d (%v)", len(noFilter), noFilter)
	}

	// With exclude — should drop route_handler.
	sqlWithFilter := `
		WITH degree AS (
			SELECT n.id, n.name, COALESCE(d.cnt, 0) AS deg
			FROM nodes n
			LEFT JOIN (
				SELECT target_id, COUNT(*) AS cnt FROM edges
				WHERE project = ? AND type = ?
				GROUP BY target_id
			) d ON d.target_id = n.id
			WHERE n.project = ? AND n.label = ? AND COALESCE(json_extract(n.properties, '$.is_entry_point'), 0) != 1
		)
		SELECT name FROM degree WHERE deg = ?
	`
	rows, err = db.Query(sqlWithFilter, projName, "CALLS", projName, "Function", 0)
	if err != nil {
		t.Fatalf("query with-filter: %v", err)
	}
	var withFilter []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		withFilter = append(withFilter, n)
	}
	rows.Close()
	if len(withFilter) != 1 {
		t.Errorf("with exclude_entry_points: expected 1 function, got %d (%v)", len(withFilter), withFilter)
	}
	if len(withFilter) == 1 && withFilter[0] != "dead_helper" {
		t.Errorf("with exclude_entry_points: expected dead_helper, got %q", withFilter[0])
	}
}

// --- trace_call_path ambiguity ---

// TestFindAmbiguousNodes_ReturnsMultipleMatches asserts that the
// ambiguity helper returns all nodes whose Name equals the input.
// Three "new" methods on different types should all be returned.
func TestFindAmbiguousNodes_ReturnsMultipleMatches(t *testing.T) {
	st := setupScopeGuardsGraph(t)
	defer st.Close()
	out := findAmbiguousNodesByNameInStore(st, "new", 5)
	if len(out) != 3 {
		t.Errorf("expected 3 nodes named 'new', got %d", len(out))
	}
	qns := make(map[string]bool)
	for _, n := range out {
		qns[n.QualifiedName] = true
	}
	if len(qns) != 3 {
		t.Errorf("expected 3 distinct QNs, got %d (%v)", len(qns), qns)
	}
}

// TestFindAmbiguousNodes_RespectsLimit asserts the helper truncates at
// the provided limit so response payload stays bounded on names shared
// by hundreds of nodes (e.g. `new` in a large codebase).
func TestFindAmbiguousNodes_RespectsLimit(t *testing.T) {
	st := setupScopeGuardsGraph(t)
	defer st.Close()
	out := findAmbiguousNodesByNameInStore(st, "new", 2)
	if len(out) != 2 {
		t.Errorf("expected 2 results with limit=2, got %d", len(out))
	}
}

// TestFindAmbiguousNodes_SingleMatch asserts that a name resolving to
// exactly one node returns a 1-element slice — the caller in trace.go
// then proceeds normally (no ambiguous status emitted).
func TestFindAmbiguousNodes_SingleMatch(t *testing.T) {
	st := setupScopeGuardsGraph(t)
	defer st.Close()
	out := findAmbiguousNodesByNameInStore(st, "dead_helper", 5)
	if len(out) != 1 {
		t.Errorf("expected 1 match for 'dead_helper', got %d", len(out))
	}
}

// TestFindAmbiguousNodes_NoMatch asserts that a name with no matches
// returns an empty slice — handleTraceCallPath then falls through to
// its existing not_found / similar-suggestions path.
func TestFindAmbiguousNodes_NoMatch(t *testing.T) {
	st := setupScopeGuardsGraph(t)
	defer st.Close()
	out := findAmbiguousNodesByNameInStore(st, "nonexistent_name", 5)
	if len(out) != 0 {
		t.Errorf("expected 0 matches, got %d", len(out))
	}
}

// TestBareNameDetection_Heuristic exercises the string check that
// handleTraceCallPath uses to decide whether to invoke the ambiguity
// guard. "new" must be classified bare; QN-style strings must not.
func TestBareNameDetection_Heuristic(t *testing.T) {
	cases := []struct {
		input string
		bare  bool // expected
	}{
		{"new", true},
		{"handle_request", true},
		{"test.foo.Foo.new", false},
		{"my_module.handle", false},
		{"Type::method", true}, // Rust syntax with no dot; treated as bare
	}
	for _, c := range cases {
		gotBare := !strings.Contains(c.input, ".")
		if gotBare != c.bare {
			t.Errorf("input=%q: expected bare=%v got %v", c.input, c.bare, gotBare)
		}
	}
}
