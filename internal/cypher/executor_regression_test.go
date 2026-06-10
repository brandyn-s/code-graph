package cypher

// Regression tests for the 2026-06-10 executor correctness sweep. Each test
// pins a bug that produced silently-wrong results (see CONFORMANCE.md
// "Resolved 2026-06-10" for the inventory). The shared fixture graph is
// setupTestStore in cypher_test.go:
//
//	HandleOrder -CALLS-> ValidateOrder -CALLS-> SubmitOrder
//	HandleOrder -CALLS-> LogError
//	main (Module) -DEFINES-> HandleOrder

import (
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// WHERE with OR on a single-node pattern was pushed into SQL as AND
// (`a OR b` returned only rows matching BOTH conditions — usually none).
func TestExecuteWhereORSingleNode(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (n:Function) WHERE n.name = "HandleOrder" OR n.name = "ValidateOrder" RETURN n.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("OR query: got %d rows, want 2 (%v)", len(res.Rows), res.Rows)
	}
}

// OR where one branch is not SQL-pushable (numeric comparison) must fall
// back to evaluating ALL conditions in Go with OR — not push a subset as AND.
func TestExecuteWhereORMixedPushability(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// LogError has start_line 1; HandleOrder is at 10 and SubmitOrder at 25.
	res, err := exec.Execute(`MATCH (n:Function) WHERE n.name = "LogError" OR n.start_line > 9 RETURN n.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("mixed-pushability OR: got %d rows, want 3 (%v)", len(res.Rows), res.Rows)
	}
}

// All-pushable OR goes through the SQL pushdown as a single parenthesized
// OR group.
func TestExecuteWhereORAllPushable(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (n:Function) WHERE n.name = "LogError" OR n.file_path ENDS WITH "service.go" RETURN n.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// LogError + ValidateOrder + SubmitOrder (both in service.go)
	if len(res.Rows) != 3 {
		t.Fatalf("all-pushable OR: got %d rows, want 3 (%v)", len(res.Rows), res.Rows)
	}
}

// The JOIN-fusion fast path used to overwrite the plan's expand step with a
// marker. The plan is shared across the per-project loop, so every project
// after the first skipped the expand entirely and leaked scan-only bindings
// (rows with unbound target variables) into the merged result.
func TestExecuteMultiProjectFusedExpand(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	if err := s.UpsertProject("test2", "/tmp/test2"); err != nil {
		t.Fatal(err)
	}
	p1, _ := s.UpsertNode(&store.Node{Project: "test2", Label: "Function", Name: "Alpha", QualifiedName: "test2.Alpha"})
	p2, _ := s.UpsertNode(&store.Node{Project: "test2", Label: "Function", Name: "Beta", QualifiedName: "test2.Beta"})
	mustInsertEdge(t, s, &store.Edge{Project: "test2", SourceID: p1, TargetID: p2, Type: "CALLS"})

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (a:Function)-[:CALLS]->(b:Function) RETURN a.name, b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// project "test": 3 CALLS edges between Functions; "test2": 1.
	if len(res.Rows) != 4 {
		t.Fatalf("multi-project: got %d rows, want 4 (%v)", len(res.Rows), res.Rows)
	}
	for _, r := range res.Rows {
		if r["b.name"] == nil {
			t.Fatalf("row with unbound b — expand was skipped for a later project: %v", r)
		}
	}
}

// Fused COUNT(*) with no group-by items emitted `... GROUP BY ` (empty),
// which is invalid SQL — every project was skipped and the result was empty.
func TestExecuteFusedCountStarNoGroupBy(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (a:Function)-[:CALLS]->(b:Function) RETURN COUNT(*)`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.SkippedProjects) > 0 {
		t.Fatalf("projects skipped (invalid SQL): %v", res.SkippedProjects)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("got %d rows, want 1 (%v)", len(res.Rows), res.Rows)
	}
	if got := res.Rows[0]["COUNT(*)"]; got != 3 {
		t.Fatalf("COUNT(*) = %v, want 3", got)
	}
}

// The SQL aggregate path assumed the COUNT column was last in the RETURN
// list; `RETURN COUNT(*) AS calls, a.name` wrote the count into "a.name"
// and left "calls" absent.
func TestExecuteCountFirstWithGroupItem(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (a:Function)-[:CALLS]->(b:Function) RETURN COUNT(*) AS calls, a.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 2 { // HandleOrder (2 callees), ValidateOrder (1)
		t.Fatalf("got %d rows, want 2 (%v)", len(res.Rows), res.Rows)
	}
	for _, r := range res.Rows {
		if _, ok := r["calls"].(int); !ok {
			t.Fatalf("'calls' missing or non-int in row %v", r)
		}
		if _, ok := r["a.name"].(string); !ok {
			t.Fatalf("'a.name' missing or non-string (count overwrote group column) in row %v", r)
		}
	}
}

// An ungrouped COUNT over an empty match returns one row with 0 (openCypher),
// not zero rows — on both the Go path (single-node pattern) and the SQL
// aggregate path (scan+expand pattern).
func TestExecuteCountStarEmptyMatch(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	for _, q := range []string{
		`MATCH (n:DoesNotExist) RETURN COUNT(*)`,
		`MATCH (a:DoesNotExist)-[:CALLS]->(b) RETURN COUNT(*)`,
	} {
		res, err := exec.Execute(q)
		if err != nil {
			t.Fatalf("execute %q: %v", q, err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("%q: got %d rows, want 1 row with 0 (%v)", q, len(res.Rows), res.Rows)
		}
		if got := res.Rows[0]["COUNT(*)"]; got != 0 {
			t.Fatalf("%q: COUNT(*) = %v, want 0", q, got)
		}
	}
}

// Grouped COUNT over an empty match stays zero rows (openCypher: no groups).
func TestExecuteGroupedCountEmptyMatchNoRows(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (a:DoesNotExist)-[:CALLS]->(b) RETURN a.name, COUNT(*)`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("grouped count over empty match: got %d rows, want 0 (%v)", len(res.Rows), res.Rows)
	}
}

// Parallel edges between the same node pair are distinct matches. The batch
// expand path used to dedup by target node, collapsing them — so the same
// logical query returned different multiplicities (and different COUNTs)
// depending on which internal path executed.
func TestExecuteParallelEdgesMultiplicity(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	a, _ := s.FindNodeByQN("test", "test.main.HandleOrder")
	v, _ := s.FindNodeByQN("test", "test.service.ValidateOrder")
	mustInsertEdge(t, s, &store.Edge{Project: "test", SourceID: a.ID, TargetID: v.ID, Type: "INVOKES"})

	exec := &Executor{Store: s}

	// Batch path (inline props block JOIN fusion): one row per edge.
	batch, err := exec.Execute(`MATCH (x:Function {name: "HandleOrder"})-[r:CALLS|INVOKES]->(y:Function) RETURN y.name, r.type`)
	if err != nil {
		t.Fatalf("batch path: %v", err)
	}
	// ValidateOrder via CALLS, ValidateOrder via INVOKES, LogError via CALLS
	if len(batch.Rows) != 3 {
		t.Fatalf("batch path: got %d rows, want 3 (%v)", len(batch.Rows), batch.Rows)
	}

	// Any-direction (also batch path) between the pair: both edges match.
	anyDir, err := exec.Execute(`MATCH (x:Function {name: "HandleOrder"})-[r:CALLS|INVOKES]-(y:Function {name: "ValidateOrder"}) RETURN y.name, r.type`)
	if err != nil {
		t.Fatalf("any-direction: %v", err)
	}
	if len(anyDir.Rows) != 2 {
		t.Fatalf("any-direction: got %d rows, want 2 (%v)", len(anyDir.Rows), anyDir.Rows)
	}
}

// The SQL aggregate fast path and the Go aggregation path must agree on
// counts. Before the parallel-edge fix they disagreed (2 vs 1) on this pair.
func TestExecuteAggregatePathConsistency(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	a, _ := s.FindNodeByQN("test", "test.main.HandleOrder")
	v, _ := s.FindNodeByQN("test", "test.service.ValidateOrder")
	mustInsertEdge(t, s, &store.Edge{Project: "test", SourceID: a.ID, TargetID: v.ID, Type: "INVOKES"})

	exec := &Executor{Store: s}
	// SQL aggregate path (pushable WHERE, no inline props).
	sqlPath, err := exec.Execute(`MATCH (x:Function)-[r:CALLS|INVOKES]->(y:Function) WHERE x.name = "HandleOrder" RETURN y.name, COUNT(r)`)
	if err != nil {
		t.Fatalf("sql path: %v", err)
	}
	// Go path (inline props block SQL aggregation and JOIN fusion).
	goPath, err := exec.Execute(`MATCH (x:Function {name: "HandleOrder"})-[r:CALLS|INVOKES]->(y:Function) RETURN y.name, COUNT(r)`)
	if err != nil {
		t.Fatalf("go path: %v", err)
	}

	counts := func(rows []map[string]any) map[string]int {
		m := make(map[string]int)
		for _, r := range rows {
			name, _ := r["y.name"].(string)
			if c, ok := r["COUNT(r)"].(int); ok {
				m[name] = c
			}
		}
		return m
	}
	sqlCounts, goCounts := counts(sqlPath.Rows), counts(goPath.Rows)
	if sqlCounts["ValidateOrder"] != 2 || goCounts["ValidateOrder"] != 2 {
		t.Fatalf("path disagreement on parallel edges: sql=%v go=%v (want ValidateOrder=2 on both)", sqlCounts, goCounts)
	}
	if sqlCounts["LogError"] != 1 || goCounts["LogError"] != 1 {
		t.Fatalf("path disagreement: sql=%v go=%v (want LogError=1 on both)", sqlCounts, goCounts)
	}
}

// Anonymous source nodes broke the (non-fused) batch expand path: the
// binding was never stored, so the expand found no source and returned
// empty. Anonymous intermediate nodes in chains had the same failure.
func TestExecuteAnonymousSourceAnyDirection(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (:Module)-[:DEFINES]-(b) RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0]["b.name"] != "HandleOrder" {
		t.Fatalf("anonymous source + any-direction: got %v, want [HandleOrder]", res.Rows)
	}
}

func TestExecuteAnonymousIntermediateNode(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (m:Module)-[:DEFINES]->()-[:CALLS]->(g) RETURN g.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 2 { // HandleOrder calls ValidateOrder + LogError
		t.Fatalf("anonymous intermediate: got %d rows, want 2 (%v)", len(res.Rows), res.Rows)
	}
}

// A two-hop pattern's plan ([scan, expand, expand]) used to satisfy the
// 3-step fusible-aggregate shape with the middle hop silently ignored,
// joining the scan label directly against the second hop's edge type.
func TestExecuteTwoHopAggregateNotMisfused(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (m:Module)-[:DEFINES]->(f)-[:CALLS]->(g) RETURN m.name, COUNT(*)`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("two-hop aggregate: got %d rows, want 1 (%v)", len(res.Rows), res.Rows)
	}
	if res.Rows[0]["m.name"] != "main" || res.Rows[0]["COUNT(*)"] != 2 {
		t.Fatalf("two-hop aggregate: got %v, want main/2", res.Rows[0])
	}
}

// *0..N includes the zero-length path: the start node binds as the target.
func TestExecuteZeroHopVariableLength(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (a:Function {name: "HandleOrder"})-[:CALLS*0..1]->(b) RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	names := make(map[string]bool)
	for _, r := range res.Rows {
		if n, ok := r["b.name"].(string); ok {
			names[n] = true
		}
	}
	for _, want := range []string{"HandleOrder", "ValidateOrder", "LogError"} {
		if !names[want] {
			t.Fatalf("*0..1: missing %s in %v", want, res.Rows)
		}
	}
	if len(res.Rows) != 3 {
		t.Fatalf("*0..1: got %d rows, want 3 (%v)", len(res.Rows), res.Rows)
	}
}

// An untyped variable-length pattern traverses ALL edge types — it was
// silently narrowed to CALLS only.
func TestExecuteUntypedVariableLengthAllTypes(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// main -DEFINES-> HandleOrder -CALLS-> {ValidateOrder, LogError}
	res, err := exec.Execute(`MATCH (m:Module {name: "main"})-[*1..2]->(b) RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("untyped var-length: got %d rows, want 3 (%v)", len(res.Rows), res.Rows)
	}
}

// <> (not-equals) was documented in CONFORMANCE.md and the query_graph tool
// description but never implemented — `<>` lexed as `<` followed by `>` and
// produced a parse error.
func TestExecuteNotEquals(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// SQL-pushdown path (pushable column).
	res, err := exec.Execute(`MATCH (n:Function) WHERE n.name <> "HandleOrder" RETURN n.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("<> pushdown: got %d rows, want 3 (%v)", len(res.Rows), res.Rows)
	}
	// Go-evaluation path (non-pushable property).
	res, err = exec.Execute(`MATCH (n:Function) WHERE n.signature <> "func HandleOrder(w, r)" RETURN n.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("<> Go path: got %d rows, want 3 (%v)", len(res.Rows), res.Rows)
	}
}

// --- Parse-time rejections added in the same sweep ---

// Mixed AND/OR silently collapsed to a flat OR over all conditions (the
// clause has a single operator and no precedence). Now rejected.
func TestParseMixedAndOrRejected(t *testing.T) {
	_, err := Parse(`MATCH (n) WHERE n.a = "1" AND n.b = "2" OR n.c = "3" RETURN n`)
	if err == nil {
		t.Fatal("mixed AND/OR parsed; want rejection")
	}
	if !strings.Contains(err.Error(), "mixed AND/OR") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Hop-range validation: *0 silently became unbounded (MaxHops==0 encodes
// "unbounded"); explicit zero upper bounds and inverted ranges were
// accepted with surprising semantics.
func TestParseHopRangeValidation(t *testing.T) {
	cases := []struct {
		query, wantSubstr string
	}{
		{`MATCH (a)-[:CALLS*0]->(b) RETURN b`, "*0"},
		{`MATCH (a)-[:CALLS*1..0]->(b) RETURN b`, "upper bound 0"},
		{`MATCH (a)-[:CALLS*3..1]->(b) RETURN b`, "less than lower"},
		{`MATCH (a)-[:CALLS*..0]->(b) RETURN b`, "upper bound 0"},
	}
	for _, c := range cases {
		_, err := Parse(c.query)
		if err == nil {
			t.Fatalf("%q parsed; want rejection", c.query)
		}
		if !strings.Contains(err.Error(), c.wantSubstr) {
			t.Fatalf("%q: error %v does not contain %q", c.query, err, c.wantSubstr)
		}
	}
	// Valid forms still parse.
	for _, q := range []string{
		`MATCH (a)-[:CALLS*0..2]->(b) RETURN b`,
		`MATCH (a)-[:CALLS*2]->(b) RETURN b`,
		`MATCH (a)-[:CALLS*1..]->(b) RETURN b`,
		`MATCH (a)-[:CALLS*]->(b) RETURN b`,
	} {
		if _, err := Parse(q); err != nil {
			t.Fatalf("%q failed to parse: %v", q, err)
		}
	}
}

// A second COUNT item was silently dropped (last one won). Now rejected.
func TestParseMultipleCountRejected(t *testing.T) {
	_, err := Parse(`MATCH (a)-[:CALLS]->(b) RETURN COUNT(a), COUNT(b)`)
	if err == nil {
		t.Fatal("multiple COUNT items parsed; want rejection")
	}
	if !strings.Contains(err.Error(), "multiple COUNT") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Default projection (no RETURN clause) clips at maxRows; the clip must set
// Truncated like every other clipping site.
func TestExecuteDefaultProjectionTruncationSignal(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s, MaxRows: 1}
	res, err := exec.Execute(`MATCH (n:Function)`) // 4 functions, cap 1
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(res.Rows))
	}
	if !res.Truncated {
		t.Fatal("default projection clipped rows without setting Truncated")
	}
}

// --- 2026-06-10 TCK-survey fixes (openCypher TCK ran against the engine
// found these; see internal/cypher/tck/) ---

// LIMIT 0 is a valid empty result. applyLimit conflated it with "no LIMIT
// clause" (limit <= 0 meant "use the default cap") and returned rows.
func TestExecuteLimitZero(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (n:Function) RETURN n.name LIMIT 0`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("LIMIT 0: got %d rows, want 0 (%v)", len(res.Rows), res.Rows)
	}
}

// LIMIT with a non-integer literal was silently accepted: the ignored Atoi
// error produced LIMIT 0, which then meant "no limit".
func TestParseLimitNonInteger(t *testing.T) {
	_, err := Parse(`MATCH (n) RETURN n LIMIT 1.7`)
	if err == nil {
		t.Fatal("LIMIT 1.7 parsed; want rejection")
	}
	if !strings.Contains(err.Error(), "non-negative integer") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A user property named "id" was shadowed by the internal SQLite row ID —
// n.id returned the rowid instead of the stored property value.
func TestExecuteUserIDPropertyNotShadowed(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	if _, err := s.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "WithID",
		QualifiedName: "test.withid",
		Properties:    map[string]any{"id": 4242},
	}); err != nil {
		t.Fatal(err)
	}

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (n:Function {name: "WithID"}) RETURN n.id`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(res.Rows))
	}
	got, ok := toFloat(res.Rows[0]["n.id"])
	if !ok || got != 4242 {
		t.Fatalf("n.id = %v, want user property 4242 (not the internal row ID)", res.Rows[0]["n.id"])
	}

	// And the internal row ID is still reachable when no user property exists.
	res, err = exec.Execute(`MATCH (n:Function {name: "HandleOrder"}) RETURN n.id`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if v, ok := toFloat(res.Rows[0]["n.id"]); !ok || v <= 0 {
		t.Fatalf("n.id fallback to row ID broken: %v", res.Rows[0]["n.id"])
	}
}

// Large integer properties (> 2^53) used to corrupt through the JSON
// round-trip (json.Unmarshal -> float64): distinct values collapsed to the
// same float64 and equality filters matched nothing. UnmarshalProps now
// preserves them as int64. (Found by the TCK Comparison1 scenarios.)
func TestExecuteLargeIntPropertyPrecision(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	for _, n := range []struct {
		name string
		big  int64
	}{{"big905", 4611686018427387905}, {"big904", 4611686018427387904}} {
		if _, err := s.UpsertNode(&store.Node{
			Project: "test", Label: "Big", Name: n.name,
			QualifiedName: "test." + n.name,
			Properties:    map[string]any{"big": n.big},
		}); err != nil {
			t.Fatal(err)
		}
	}

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (n:Big) WHERE n.big = 4611686018427387905 RETURN n.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0]["n.name"] != "big905" {
		t.Fatalf("large-int equality: got %v, want exactly big905", res.Rows)
	}
}

// Mixed-type ordered comparisons used to abort the whole project's
// execution (strconv error surfaced as a skipped project). openCypher
// semantics: the comparison is unknown and the row is filtered.
func TestExecuteMixedTypeComparisonFiltersNotAborts(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// n.name is a string on every node; comparing against a number must
	// return zero rows — not error, not skip the project.
	res, err := exec.Execute(`MATCH (n:Function) WHERE n.name > 5 RETURN n.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.SkippedProjects) > 0 {
		t.Fatalf("mixed-type comparison aborted the project: %v", res.SkippedProjects)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("mixed-type comparison matched rows: %v", res.Rows)
	}
}

// String literals on ordered comparisons compare lexicographically
// (openCypher type-aware comparison); previously they errored.
func TestExecuteStringOrderedComparison(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(`MATCH (n:Function) WHERE n.name >= "S" RETURN n.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Fixture functions: HandleOrder, ValidateOrder, SubmitOrder, LogError.
	if len(res.Rows) != 2 { // SubmitOrder, ValidateOrder
		t.Fatalf(`name >= "S": got %d rows, want 2 (%v)`, len(res.Rows), res.Rows)
	}
}
