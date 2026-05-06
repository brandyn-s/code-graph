package cypher

// Phase D2 (Plan 1): Cypher conformance corpus.
// See CONFORMANCE.md for the design rationale + scope.
//
// This test runs a curated set of queries against the parser+planner
// and verifies expected outcomes. It complements cypher_test.go (which
// tests lexer/parser/executor units) by asserting end-to-end behavior
// for documented features.
//
// Each fixture is a query the user-facing tool description claims to
// support. If a fixture starts failing, either:
//   (a) The planner regressed — investigate before merging.
//   (b) We intentionally dropped support — update the fixture AND the
//       tool description in internal/tools/tools.go.

import (
	"strings"
	"testing"
)

// positiveCypherFixtures: queries that MUST parse without error.
// We don't execute them (would need a fixture graph); we verify
// parse + plan succeed. This catches the most common drift class
// (parser refactor breaks a previously-working query).
//
// IMPORTANT: this corpus is the AUTHORITATIVE list of supported
// Cypher features. The user-facing tool description in
// internal/tools/tools.go MUST stay in sync. If you add a feature
// here, add it there. If you drop a feature here, drop it there.
var positiveCypherFixtures = []struct {
	name  string
	query string
	doc   string
}{
	{
		"basic_node_match",
		"MATCH (n) RETURN n",
		"bare match — no label, no where",
	},
	{
		"label_match",
		"MATCH (f:Function) RETURN f",
		"node pattern with label",
	},
	{
		"property_filter_eq",
		`MATCH (f:Function) WHERE f.name = "Hello" RETURN f.name`,
		"WHERE with equality",
	},
	{
		"property_filter_inequality",
		`MATCH (f:Function) WHERE f.complexity > 10 RETURN f`,
		"WHERE with numeric comparison",
	},
	{
		"property_regex",
		`MATCH (f:Function) WHERE f.name =~ '(?i)handle.*' RETURN f`,
		"WHERE with regex (=~)",
	},
	{
		"starts_with",
		`MATCH (f:Function) WHERE f.name STARTS WITH 'handle' RETURN f`,
		"WHERE with STARTS WITH",
	},
	{
		"ends_with",
		`MATCH (f:Function) WHERE f.file_path ENDS WITH '.go' RETURN f`,
		"WHERE with ENDS WITH (Plan 3 Phase A: parser extended 2026-05-06)",
	},
	{
		"contains",
		`MATCH (f:Function) WHERE f.name CONTAINS 'Test' RETURN f`,
		"WHERE with CONTAINS",
	},
	{
		"and_or",
		`MATCH (f:Function) WHERE f.complexity > 5 AND f.name CONTAINS 'handle' RETURN f`,
		"WHERE with AND",
	},
	{
		"directional_relationship",
		"MATCH (a)-[:CALLS]->(b) RETURN a.name, b.name",
		"directional relationship pattern",
	},
	{
		"inbound_relationship",
		"MATCH (a)<-[:CALLS]-(b) RETURN a.name, b.name",
		"inbound relationship",
	},
	{
		"bidirectional_relationship",
		"MATCH (a)-[:CALLS]-(b) RETURN a.name, b.name",
		"bidirectional relationship",
	},
	{
		"multiple_relationship_types",
		"MATCH (a)-[:CALLS|IMPORTS]->(b) RETURN a.name, b.name",
		"multiple relationship types separated by |",
	},
	{
		"variable_length_path",
		"MATCH (a)-[:CALLS*1..3]->(b) RETURN a.name, b.name",
		"variable-length path 1..3",
	},
	{
		"count_star",
		"MATCH (f:Function) RETURN COUNT(*)",
		"COUNT(*) aggregation (Plan 3 Phase A: parser extended 2026-05-06 to accept '*' as the COUNT argument; openCypher standard form)",
	},
	{
		"count_variable",
		"MATCH (f:Function) RETURN COUNT(f)",
		"COUNT(variable) aggregation (existing form, retained)",
	},
	{
		"distinct",
		"MATCH (f:Function) RETURN DISTINCT f.name",
		"DISTINCT projection",
	},
	{
		"order_by_limit",
		"MATCH (f:Function) RETURN f.name ORDER BY f.name LIMIT 10",
		"ORDER BY + LIMIT",
	},
	{
		"limit_offset",
		"MATCH (f:Function) RETURN f LIMIT 10",
		"LIMIT only",
	},
	{
		"is_null_filter",
		`MATCH (f:Function) WHERE f.docstring IS NULL RETURN f.name`,
		"IS NULL (Plan 3 Phase A: parser extended 2026-05-06)",
	},
	{
		"is_not_null_filter",
		`MATCH (f:Function) WHERE f.docstring IS NOT NULL RETURN f.name`,
		"IS NOT NULL (Plan 3 Phase A: parser extended 2026-05-06)",
	},
	{
		"edge_confidence_filter",
		`MATCH (a)-[r:CALLS]->(b) WHERE r.confidence >= 0.7 RETURN a.name, b.name`,
		"edge property filter (r.confidence)",
	},
}

// negativeCypherFixtures: queries that MUST fail (lex / parse / plan).
// Each entry includes the expected error substring so test failure
// distinguishes "wrong error" from "wrong outcome".
var negativeCypherFixtures = []struct {
	name              string
	query             string
	expectedErrSubstr string
	doc               string
}{
	{
		"create_rejected",
		"CREATE (n:Function {name: 'X'})",
		"CREATE not supported in read-only Cypher subset",
		"CREATE is rejected at parse-time (Plan 3 Phase A: 2026-05-06)",
	},
	{
		"delete_rejected",
		"MATCH (n) DELETE n",
		"DELETE not supported in read-only Cypher subset",
		"DELETE is rejected at parse-time (Plan 3 Phase A: 2026-05-06)",
	},
	{
		"set_rejected",
		"MATCH (n) SET n.name = 'X'",
		"SET not supported in read-only Cypher subset",
		"SET is rejected at parse-time (Plan 3 Phase A: 2026-05-06)",
	},
	{
		"merge_rejected",
		"MERGE (n:Function {name: 'X'})",
		"MERGE not supported in read-only Cypher subset",
		"MERGE is rejected at parse-time (Plan 3 Phase A: 2026-05-06)",
	},
	{
		"remove_rejected",
		"MATCH (n) REMOVE n.name",
		"REMOVE not supported in read-only Cypher subset",
		"REMOVE is rejected at parse-time (Plan 3 Phase A: 2026-05-06)",
	},
	{
		"unclosed_paren",
		"MATCH (f:Function RETURN f",
		"",
		"missing close paren",
	},
	{
		"missing_match",
		"WHERE f.name = 'X' RETURN f",
		"",
		"WHERE without MATCH",
	},
}

func TestCypherConformance_Positive(t *testing.T) {
	for _, fx := range positiveCypherFixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			_, err := Parse(fx.query)
			if err != nil {
				t.Fatalf("parse(%q) failed: %v\nfixture doc: %s", fx.query, err, fx.doc)
			}
			// Plan/execute would need a fixture graph; for now, parse
			// success is the conformance signal. Drift in execution
			// behavior is caught by the existing executor tests.
		})
	}
}

func TestCypherConformance_Negative(t *testing.T) {
	for _, fx := range negativeCypherFixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			_, parseErr := Parse(fx.query)
			if parseErr == nil {
				t.Fatalf("expected parse failure for %q (doc: %s) but it parsed",
					fx.query, fx.doc)
			}
			if fx.expectedErrSubstr != "" && !strings.Contains(parseErr.Error(), fx.expectedErrSubstr) {
				t.Errorf("parse error did not contain %q: %v", fx.expectedErrSubstr, parseErr)
			}
		})
	}
}
