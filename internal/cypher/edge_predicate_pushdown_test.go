package cypher

// Edge-predicate pushdown regression tests.
//
// THE BUG (2026-07-27): execJoinScanExpand fetched only expandLimit edge rows
// from SQL, and WHERE predicates on the relationship variable were then applied
// in Go over that already-truncated window. A predicate whose matching rows sat
// PAST the cap therefore returned an empty result set that was
// indistinguishable from "no such edges exist" — with total: 0 and no error.
//
// Found while verifying SCIP ingestion on a real index. The same query answered
// differently depending only on the row cap:
//
//	WHERE r.resolver_rule = 'scip-ingest'   max_rows=200  -> total 0
//	                                        max_rows=2000 -> total 5
//
// while the identical filter run directly against SQLite matched 2,638 rows the
// entire time. The zero looked authoritative, and it caused a wrong conclusion
// ("SCIP edges aren't queryable") before the row-cap dependence was noticed.
//
// The fix pushes edge predicates into the JOIN's SQL so the filter sees every
// candidate row, not just the first expandLimit of them. The Go-side filter
// still runs afterward, so conditions SQL cannot express stay correct.

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// setupStoreWithManyTaggedEdges builds a fixture whose ONE interesting edge is
// deliberately far down the scan order, behind many decoy edges. That is the
// shape that made the production bug invisible: with a low cap the interesting
// row is never fetched, so the Go filter never sees it.
func setupStoreWithManyTaggedEdges(t *testing.T, decoys int) *store.Store {
	t.Helper()
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("upsert project: %v", err)
	}

	caller, err := s.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "caller",
		QualifiedName: "test.m.caller", FilePath: "m.go", StartLine: 1, EndLine: 2,
	})
	if err != nil {
		t.Fatalf("upsert caller: %v", err)
	}

	// Decoys first, so they occupy the low rowids the cap would keep.
	for i := 0; i < decoys; i++ {
		tgt, err := s.UpsertNode(&store.Node{
			Project: "test", Label: "Function", Name: "decoy",
			QualifiedName: "test.m.decoy" + string(rune('A'+i%26)) + itoa(i),
			FilePath:      "m.go", StartLine: 1, EndLine: 2,
		})
		if err != nil {
			t.Fatalf("upsert decoy %d: %v", i, err)
		}
		mustInsertEdge(t, s, &store.Edge{
			Project: "test", SourceID: caller, TargetID: tgt, Type: "CALLS",
			Properties: map[string]any{"resolver_rule": "fuzzy-resolve"},
		})
	}

	// The needle: inserted LAST, so it lands past a small cap.
	needle, err := s.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "needle",
		QualifiedName: "test.m.needle", FilePath: "m.go", StartLine: 1, EndLine: 2,
	})
	if err != nil {
		t.Fatalf("upsert needle: %v", err)
	}
	mustInsertEdge(t, s, &store.Edge{
		Project: "test", SourceID: caller, TargetID: needle, Type: "CALLS",
		Properties: map[string]any{"resolver_rule": "scip-ingest"},
	})

	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestEdgePredicateSurvivesRowCap is the regression test. With a cap far below
// the decoy count, the needle edge is only findable if the predicate ran in
// SQL. Pre-fix this returned 0 rows.
func TestEdgePredicateSurvivesRowCap(t *testing.T) {
	s := setupStoreWithManyTaggedEdges(t, 50)
	defer s.Close()

	// MaxRows (not expandLimit) is the lever: Execute() overwrites expandLimit
	// per-run from bindingCap(), which is maxRows()*2. Setting expandLimit in
	// the literal is silently discarded — a mutation test caught these tests
	// passing with the fix disabled because the small cap never took effect.
	exec := &Executor{Store: s, MaxRows: 2} // binding cap = 4, far below 50 decoys
	res, err := exec.Execute(
		`MATCH (a)-[r:CALLS]->(b) WHERE r.resolver_rule = "scip-ingest" RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("edge predicate past the row cap: got %d rows, want 1 — the "+
			"predicate did not reach SQL and the Go filter saw only the first "+
			"few rows (%v)", len(res.Rows), res.Rows)
	}
}

// TestEdgePredicateAgreesAcrossRowCaps pins the property that actually broke:
// the ANSWER must not depend on the cap. This is the invariant the production
// symptom violated (total 0 at max_rows=200, total 5 at 2000).
func TestEdgePredicateAgreesAcrossRowCaps(t *testing.T) {
	for _, cap := range []int{1, 2, 5, 500} {
		s := setupStoreWithManyTaggedEdges(t, 50)
		exec := &Executor{Store: s, MaxRows: cap}
		res, err := exec.Execute(
			`MATCH (a)-[r:CALLS]->(b) WHERE r.resolver_rule = "scip-ingest" RETURN b.name`)
		if err != nil {
			s.Close()
			t.Fatalf("cap=%d: execute: %v", cap, err)
		}
		if len(res.Rows) != 1 {
			s.Close()
			t.Errorf("cap=%d: got %d rows, want 1 — the result must not depend "+
				"on the row cap", cap, len(res.Rows))
			continue
		}
		s.Close()
	}
}

// TestEdgePredicateNonMatchStillEmpty guards the other direction: pushdown must
// not turn a genuine no-match into a false positive.
func TestEdgePredicateNonMatchStillEmpty(t *testing.T) {
	s := setupStoreWithManyTaggedEdges(t, 5)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(
		`MATCH (a)-[r:CALLS]->(b) WHERE r.resolver_rule = "no-such-resolver" RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Errorf("genuine non-match returned %d rows, want 0 (%v)", len(res.Rows), res.Rows)
	}
}

// TestEdgePredicateORNotPushed pins the OR safety rule for EDGE predicates:
// pushing a subset of OR branches as `AND c1 AND c2` would turn a union into an
// intersection — the class of bug execScan already guards against. Both
// branches match here, and the union is every CALLS edge in the fixture
// (3 decoys + 1 needle = 4).
//
// CORRECTION (2026-07-27): an earlier version of this comment claimed a
// "separate pre-existing defect" where an OR mixing an edge predicate with a
// TARGET-NODE predicate dropped a branch. That was wrong — the executor is
// correct and there is no such bug. The claim came from a probe of mine that
// queried `b.name = "decoyA0"`, but setupStoreWithManyTaggedEdges names every
// decoy node plain "decoy" and only varies the QUALIFIED name. So that branch
// matched zero nodes by construction, `needle OR decoyA0` correctly returned 1
// row, and I read the right answer as a defect.
//
// Stashing the pushdown and re-running "confirmed" it only because the fault was
// in the query, not the code — so it reproduced identically either way. A
// stash-test shows "my change didn't cause this"; it cannot show that "this" is
// real. Verified after the fact: target-node OR returns 4/4 rows, source-node OR
// returns 4/4, and each branch alone returns 1 and 3.
//
// TestTargetNodeORReturnsUnion below pins the correct behavior so the false
// claim cannot be reintroduced from this comment's history.
func TestEdgePredicateORNotPushed(t *testing.T) {
	s := setupStoreWithManyTaggedEdges(t, 3)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(
		`MATCH (a)-[r:CALLS]->(b) WHERE r.resolver_rule = "scip-ingest" OR r.resolver_rule = "fuzzy-resolve" RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 4 {
		t.Errorf("OR over two edge predicates: got %d rows, want 4 (3 decoys + "+
			"needle) — an OR must not be pushed as AND (%v)", len(res.Rows), res.Rows)
	}
}

// TestEdgePredicateBuiltinColumnStillWorks: `type` is a real column, not a JSON
// member, so it takes the other pushdown branch.
func TestEdgePredicateBuiltinColumnStillWorks(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(
		`MATCH (a)-[r:CALLS]->(b) WHERE r.type = "CALLS" RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 3 { // 3 CALLS edges in the shared fixture
		t.Errorf("builtin edge column predicate: got %d rows, want 3 (%v)",
			len(res.Rows), res.Rows)
	}
}

// TestEdgePredicateAbsentPropertyExcluded: an edge with no such property must
// not match, matching the Go path where getEdgeProperty returns nil.
func TestEdgePredicateAbsentPropertyExcluded(t *testing.T) {
	s := setupTestStore(t) // fixture edges carry no resolver_rule at all
	defer s.Close()

	exec := &Executor{Store: s}
	res, err := exec.Execute(
		`MATCH (a)-[r:CALLS]->(b) WHERE r.resolver_rule = "anything" RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Errorf("edges without the property matched: got %d rows, want 0 (%v)",
			len(res.Rows), res.Rows)
	}
}

// TestTargetNodeORReturnsUnion pins that an OR on the TARGET node of a fused
// scan+expand returns the union, not a subset.
//
// This exists because I briefly recorded a nonexistent bug here (see the
// CORRECTION above): a probe used `b.name = "decoyA0"`, but the fixture varies
// only the QUALIFIED name — every decoy's `name` is plain "decoy" — so the
// branch matched nothing and the correct 1-row answer looked like a dropped
// branch. Asserting against BOTH fields keeps a future reader from repeating it:
// if the two disagree, the fixture's shape is the explanation, not the executor.
func TestTargetNodeORReturnsUnion(t *testing.T) {
	s := setupStoreWithManyTaggedEdges(t, 3)
	defer s.Close()
	exec := &Executor{Store: s}

	// Branches alone, so the expected union is derived rather than hardcoded.
	needle, err := exec.Execute(`MATCH (a)-[r:CALLS]->(b) WHERE b.name = "needle" RETURN b.name`)
	if err != nil {
		t.Fatalf("needle branch: %v", err)
	}
	decoy, err := exec.Execute(`MATCH (a)-[r:CALLS]->(b) WHERE b.name = "decoy" RETURN b.name`)
	if err != nil {
		t.Fatalf("decoy branch: %v", err)
	}
	want := len(needle.Rows) + len(decoy.Rows) // disjoint by construction
	if want != 4 {
		t.Fatalf("fixture changed shape: branches gave %d+%d, expected 1+3",
			len(needle.Rows), len(decoy.Rows))
	}

	got, err := exec.Execute(
		`MATCH (a)-[r:CALLS]->(b) WHERE b.name = "needle" OR b.name = "decoy" RETURN b.name`)
	if err != nil {
		t.Fatalf("OR: %v", err)
	}
	if len(got.Rows) != want {
		t.Errorf("target-node OR returned %d rows, want the union %d — an OR must "+
			"not collapse to an intersection (%v)", len(got.Rows), want, got.Rows)
	}

	// The name-vs-QN distinction that caused the false report: a QN-suffixed
	// value is NOT a node name. Both must behave, and differently.
	byName, err := exec.Execute(`MATCH (a)-[r:CALLS]->(b) WHERE b.name = "decoyA0" RETURN b.name`)
	if err != nil {
		t.Fatalf("name probe: %v", err)
	}
	if len(byName.Rows) != 0 {
		t.Errorf("b.name = \"decoyA0\" matched %d rows; the fixture puts that "+
			"suffix only in the qualified_name", len(byName.Rows))
	}
	byQN, err := exec.Execute(
		`MATCH (a)-[r:CALLS]->(b) WHERE b.qualified_name = "test.m.decoyA0" RETURN b.name`)
	if err != nil {
		t.Fatalf("qn probe: %v", err)
	}
	if len(byQN.Rows) != 1 {
		t.Errorf("b.qualified_name = \"test.m.decoyA0\" matched %d rows, want 1",
			len(byQN.Rows))
	}
}
