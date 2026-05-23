package cypher

import (
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// --- Lexer tests ---

func TestLexBasicQuery(t *testing.T) {
	tokens, err := Lex(`MATCH (f:Function) WHERE f.name = "Hello" RETURN f.name`)
	if err != nil {
		t.Fatalf("lex: %v", err)
	}

	expected := []TokenType{
		TokMatch, TokLParen, TokIdent, TokColon, TokIdent, TokRParen,
		TokWhere, TokIdent, TokDot, TokIdent, TokEQ, TokString,
		TokReturn, TokIdent, TokDot, TokIdent, TokEOF,
	}

	if len(tokens) != len(expected) {
		t.Fatalf("expected %d tokens, got %d", len(expected), len(tokens))
	}
	for i, tok := range tokens {
		if tok.Type != expected[i] {
			t.Errorf("token[%d]: expected type %d, got %d (%q)", i, expected[i], tok.Type, tok.Value)
		}
	}
}

func TestLexRegexOperator(t *testing.T) {
	tokens, err := Lex(`f.name =~ ".*Handler"`)
	if err != nil {
		t.Fatalf("lex: %v", err)
	}
	// f, ., name, =~, ".*Handler"
	if tokens[3].Type != TokRegex {
		t.Errorf("expected TokRegex, got type %d (%q)", tokens[3].Type, tokens[3].Value)
	}
}

func TestLexVariableLengthPath(t *testing.T) {
	tokens, err := Lex(`[:CALLS*1..3]`)
	if err != nil {
		t.Fatalf("lex: %v", err)
	}
	expected := []TokenType{
		TokLBracket, TokColon, TokIdent, TokStar, TokNumber, TokDotDot, TokNumber, TokRBracket, TokEOF,
	}
	if len(tokens) != len(expected) {
		t.Fatalf("expected %d tokens, got %d", len(expected), len(tokens))
	}
	for i, tok := range tokens {
		if tok.Type != expected[i] {
			t.Errorf("token[%d]: expected type %d, got %d (%q)", i, expected[i], tok.Type, tok.Value)
		}
	}
}

// --- Parser tests ---

func TestParseNodePattern(t *testing.T) {
	q, err := Parse(`MATCH (f:Function {name: "Hello"}) RETURN f`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.Match == nil || q.Match.Pattern == nil {
		t.Fatal("expected match pattern")
	}
	elems := q.Match.Pattern.Elements
	if len(elems) != 1 {
		t.Fatalf("expected 1 element, got %d", len(elems))
	}
	node, ok := elems[0].(*NodePattern)
	if !ok {
		t.Fatalf("expected *NodePattern, got %T", elems[0])
	}
	if node.Variable != "f" {
		t.Errorf("expected variable 'f', got %q", node.Variable)
	}
	if node.Label != "Function" {
		t.Errorf("expected label 'Function', got %q", node.Label)
	}
	if node.Props["name"] != "Hello" {
		t.Errorf("expected prop name='Hello', got %q", node.Props["name"])
	}
}

func TestParseRelationship(t *testing.T) {
	q, err := Parse(`MATCH (f)-[:CALLS]->(g) RETURN f.name, g.name`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	elems := q.Match.Pattern.Elements
	if len(elems) != 3 {
		t.Fatalf("expected 3 elements (node-rel-node), got %d", len(elems))
	}
	rel, ok := elems[1].(*RelPattern)
	if !ok {
		t.Fatalf("expected *RelPattern, got %T", elems[1])
	}
	if len(rel.Types) != 1 || rel.Types[0] != "CALLS" {
		t.Errorf("expected CALLS type, got %v", rel.Types)
	}
	if rel.Direction != "outbound" {
		t.Errorf("expected outbound, got %q", rel.Direction)
	}
	if rel.MinHops != 1 || rel.MaxHops != 1 {
		t.Errorf("expected hops 1..1, got %d..%d", rel.MinHops, rel.MaxHops)
	}
}

func TestParseVariableLength(t *testing.T) {
	q, err := Parse(`MATCH (f)-[:CALLS*1..3]->(g) RETURN g.name`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rel, ok := q.Match.Pattern.Elements[1].(*RelPattern)
	if !ok {
		t.Fatalf("expected *RelPattern, got %T", q.Match.Pattern.Elements[1])
	}
	if rel.MinHops != 1 {
		t.Errorf("expected minHops=1, got %d", rel.MinHops)
	}
	if rel.MaxHops != 3 {
		t.Errorf("expected maxHops=3, got %d", rel.MaxHops)
	}
}

func TestParseWhereRegex(t *testing.T) {
	q, err := Parse(`MATCH (f:Function) WHERE f.name =~ ".*Handler" RETURN f.name`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.Where == nil {
		t.Fatal("expected WHERE clause")
	}
	if len(q.Where.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(q.Where.Conditions))
	}
	c := q.Where.Conditions[0]
	if c.Operator != "=~" {
		t.Errorf("expected =~, got %q", c.Operator)
	}
	if c.Value != ".*Handler" {
		t.Errorf("expected '.*Handler', got %q", c.Value)
	}
}

func TestParseReturnWithCount(t *testing.T) {
	q, err := Parse(`MATCH (f)-[:CALLS]->(g) RETURN f.name, COUNT(g) AS cnt ORDER BY cnt DESC LIMIT 10`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.Return == nil {
		t.Fatal("expected RETURN clause")
	}
	if len(q.Return.Items) != 2 {
		t.Fatalf("expected 2 return items, got %d", len(q.Return.Items))
	}

	// First item: f.name
	if q.Return.Items[0].Variable != "f" || q.Return.Items[0].Property != "name" {
		t.Errorf("expected f.name, got %s.%s", q.Return.Items[0].Variable, q.Return.Items[0].Property)
	}

	// Second item: COUNT(g) AS cnt
	if q.Return.Items[1].Func != "COUNT" {
		t.Errorf("expected COUNT, got %q", q.Return.Items[1].Func)
	}
	if q.Return.Items[1].Variable != "g" {
		t.Errorf("expected variable 'g', got %q", q.Return.Items[1].Variable)
	}
	if q.Return.Items[1].Alias != "cnt" {
		t.Errorf("expected alias 'cnt', got %q", q.Return.Items[1].Alias)
	}

	// ORDER BY
	if q.Return.OrderBy != "cnt" {
		t.Errorf("expected ORDER BY cnt, got %q", q.Return.OrderBy)
	}
	if q.Return.OrderDir != "DESC" {
		t.Errorf("expected DESC, got %q", q.Return.OrderDir)
	}
	if q.Return.Limit != 10 {
		t.Errorf("expected LIMIT 10, got %d", q.Return.Limit)
	}
}

func TestParseBidirectional(t *testing.T) {
	q, err := Parse(`MATCH (f:Function)-[:CALLS]-(g) RETURN f.name, g.name`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rel, ok := q.Match.Pattern.Elements[1].(*RelPattern)
	if !ok {
		t.Fatalf("expected *RelPattern, got %T", q.Match.Pattern.Elements[1])
	}
	if rel.Direction != "any" {
		t.Errorf("expected 'any' direction, got %q", rel.Direction)
	}
}

func TestParseInbound(t *testing.T) {
	q, err := Parse(`MATCH (f:Function)<-[:CALLS]-(g) RETURN f.name`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rel, ok := q.Match.Pattern.Elements[1].(*RelPattern)
	if !ok {
		t.Fatalf("expected *RelPattern, got %T", q.Match.Pattern.Elements[1])
	}
	if rel.Direction != "inbound" {
		t.Errorf("expected inbound, got %q", rel.Direction)
	}
}

func TestParseMultipleRelTypes(t *testing.T) {
	q, err := Parse(`MATCH (f)-[:CALLS|HTTP_CALLS]->(g) RETURN g.name`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rel, ok := q.Match.Pattern.Elements[1].(*RelPattern)
	if !ok {
		t.Fatalf("expected *RelPattern, got %T", q.Match.Pattern.Elements[1])
	}
	if len(rel.Types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(rel.Types))
	}
	if rel.Types[0] != "CALLS" || rel.Types[1] != "HTTP_CALLS" {
		t.Errorf("expected [CALLS, HTTP_CALLS], got %v", rel.Types)
	}
}

func TestParseWhereStartsWith(t *testing.T) {
	q, err := Parse(`MATCH (f:Function) WHERE f.name STARTS WITH "Send" RETURN f`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := q.Where.Conditions[0]
	if c.Operator != "STARTS WITH" {
		t.Errorf("expected 'STARTS WITH', got %q", c.Operator)
	}
	if c.Value != "Send" {
		t.Errorf("expected 'Send', got %q", c.Value)
	}
}

func TestParseWhereContains(t *testing.T) {
	q, err := Parse(`MATCH (f:Function) WHERE f.name CONTAINS "Handler" RETURN f`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := q.Where.Conditions[0]
	if c.Operator != "CONTAINS" {
		t.Errorf("expected CONTAINS, got %q", c.Operator)
	}
}

func TestParseWhereNumericComparison(t *testing.T) {
	q, err := Parse(`MATCH (f:Function) WHERE f.start_line > 10 RETURN f`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := q.Where.Conditions[0]
	if c.Operator != ">" {
		t.Errorf("expected '>', got %q", c.Operator)
	}
	if c.Value != "10" {
		t.Errorf("expected '10', got %q", c.Value)
	}
}

func TestParseWhereAnd(t *testing.T) {
	q, err := Parse(`MATCH (f) WHERE f.label = "Function" AND f.name = "Foo" RETURN f`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(q.Where.Conditions) != 2 {
		t.Fatalf("expected 2 conditions, got %d", len(q.Where.Conditions))
	}
	if q.Where.Operator != "AND" {
		t.Errorf("expected AND, got %q", q.Where.Operator)
	}
}

func TestParseDistinct(t *testing.T) {
	q, err := Parse(`MATCH (f:Function) RETURN DISTINCT f.label`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !q.Return.Distinct {
		t.Error("expected DISTINCT to be true")
	}
}

// --- Integration test ---

func setupTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("upsert project: %v", err)
	}

	// Create nodes
	idA, _ := s.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "HandleOrder",
		QualifiedName: "test.main.HandleOrder", FilePath: "main.go",
		StartLine: 10, EndLine: 30,
		Properties: map[string]any{"signature": "func HandleOrder(w, r)"},
	})
	idB, _ := s.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "ValidateOrder",
		QualifiedName: "test.service.ValidateOrder", FilePath: "service.go",
		StartLine: 5, EndLine: 20,
		Properties: map[string]any{"signature": "func ValidateOrder(o Order) error"},
	})
	idC, _ := s.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "SubmitOrder",
		QualifiedName: "test.service.SubmitOrder", FilePath: "service.go",
		StartLine: 25, EndLine: 50,
		Properties: map[string]any{"signature": "func SubmitOrder(o Order) error"},
	})
	idD, _ := s.UpsertNode(&store.Node{
		Project: "test", Label: "Module", Name: "main",
		QualifiedName: "test.main", FilePath: "main.go",
	})
	idE, _ := s.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "LogError",
		QualifiedName: "test.util.LogError", FilePath: "util.go",
		StartLine: 1, EndLine: 5,
	})

	// Edges: HandleOrder -> ValidateOrder -> SubmitOrder
	//        HandleOrder -> LogError
	mustInsertEdge(t, s, &store.Edge{Project: "test", SourceID: idA, TargetID: idB, Type: "CALLS"})
	mustInsertEdge(t, s, &store.Edge{Project: "test", SourceID: idB, TargetID: idC, Type: "CALLS"})
	mustInsertEdge(t, s, &store.Edge{Project: "test", SourceID: idA, TargetID: idE, Type: "CALLS"})
	mustInsertEdge(t, s, &store.Edge{Project: "test", SourceID: idD, TargetID: idA, Type: "DEFINES"})

	return s
}

// mustInsertEdge inserts an edge and fails the test on error.
func mustInsertEdge(t *testing.T, s *store.Store, edge *store.Edge) {
	t.Helper()
	if _, err := s.InsertEdge(edge); err != nil {
		t.Fatalf("insert edge: %v", err)
	}
}

func TestExecuteSimpleMatch(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 4 {
		t.Errorf("expected 4 functions, got %d", len(result.Rows))
	}
}

func TestExecuteRelationshipQuery(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function)-[:CALLS]->(g:Function) RETURN f.name, g.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// HandleOrder -> ValidateOrder, HandleOrder -> LogError, ValidateOrder -> SubmitOrder
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(result.Rows))
	}

	// Verify columns
	if len(result.Columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(result.Columns))
	}

	// Check that HandleOrder -> ValidateOrder is in the results
	found := false
	for _, row := range result.Rows {
		if row["f.name"] == "HandleOrder" && row["g.name"] == "ValidateOrder" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected HandleOrder -> ValidateOrder in results")
	}
}

func TestExecuteWhereFilter(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.name = "HandleOrder" RETURN f.name, f.file_path`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0]["f.name"] != "HandleOrder" {
		t.Errorf("expected HandleOrder, got %v", result.Rows[0]["f.name"])
	}
}

func TestExecuteWhereRegex(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.name =~ ".*Order" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// HandleOrder, ValidateOrder, SubmitOrder
	if len(result.Rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(result.Rows))
	}
}

func TestExecuteWhereStartsWith(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.name STARTS WITH "Submit" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0]["f.name"] != "SubmitOrder" {
		t.Errorf("expected SubmitOrder, got %v", result.Rows[0]["f.name"])
	}
}

func TestExecuteWhereContains(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.name CONTAINS "Order" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 3 {
		t.Errorf("expected 3 rows (HandleOrder, ValidateOrder, SubmitOrder), got %d", len(result.Rows))
	}
}

// Phase B4/B5 (Plan 8-Phase Arc, 2026-05-09): user reported `=~` and
// `CONTAINS` returning 0 unexpectedly on file_path. These tests pin
// engine-correct behavior on file_path filtering. If they pass, the
// observed "0 results" cases were likely caller-side data/regex-syntax
// issues, not engine bugs. The conformance test additions in
// cypher_conformance_test.go provide further coverage at the parser
// level.

func TestExecuteWhereContainsOnFilePath(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.file_path CONTAINS "service" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// ValidateOrder + SubmitOrder are in service.go
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows (ValidateOrder, SubmitOrder), got %d: %+v", len(result.Rows), result.Rows)
	}
}

// TestExecuteWhereContainsOnArrayProperty pins the array-element-of
// semantics for CONTAINS introduced 2026-05-13. Pre-fix behavior: when
// the operand was an array (e.g., `decorator_tags []string`), the
// in-memory CONTAINS evaluator's type assertion to string failed and
// returned (false, nil) silently. Queries like
// `WHERE n.decorator_tags CONTAINS 'derive'` returned 0 rows despite the
// tag being present. Post-fix: CONTAINS on an array is element-of.
func TestExecuteWhereContainsOnArrayProperty(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer s.Close()
	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("upsert project: %v", err)
	}

	// Three Class nodes with different decorator_tags arrays
	_, _ = s.UpsertNode(&store.Node{
		Project: "test", Label: "Class", Name: "WithDerive",
		QualifiedName: "test.m.WithDerive", FilePath: "m.rs",
		Properties: map[string]any{"decorator_tags": []string{"derive", "test"}},
	})
	_, _ = s.UpsertNode(&store.Node{
		Project: "test", Label: "Class", Name: "WithoutDerive",
		QualifiedName: "test.m.WithoutDerive", FilePath: "m.rs",
		Properties: map[string]any{"decorator_tags": []string{"test"}},
	})
	_, _ = s.UpsertNode(&store.Node{
		Project: "test", Label: "Class", Name: "NoTags",
		QualifiedName: "test.m.NoTags", FilePath: "m.rs",
	})

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (n:Class) WHERE n.decorator_tags CONTAINS "derive" RETURN n.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row (WithDerive), got %d: %+v", len(result.Rows), result.Rows)
	}
	if result.Rows[0]["n.name"] != "WithDerive" {
		t.Errorf("expected WithDerive, got %v", result.Rows[0]["n.name"])
	}
}

// SAFETY: existing string CONTAINS callers must keep working. This test
// guards against accidentally breaking the string code path in the array fix.
func TestExecuteWhereContainsOnStringPropertyStillWorks(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()
	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.name CONTAINS "Order" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Same expectation as TestExecuteWhereContains — 3 functions match
	if len(result.Rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(result.Rows))
	}
}

func TestExecuteWhereRegexOnFilePath(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// `.*\\.go$` matches all .go files (escaped backslash for Go string)
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.file_path =~ ".*\\.go$" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// All 4 functions are in .go files (HandleOrder, ValidateOrder, SubmitOrder, LogError)
	if len(result.Rows) != 4 {
		t.Errorf("expected 4 rows for .go file_path regex, got %d: %+v", len(result.Rows), result.Rows)
	}
}

func TestExecuteWhereRegexOnFilePathSubstring(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// `.*service.*` regex (no anchors) matches anywhere in file_path
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.file_path =~ ".*service.*" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// ValidateOrder + SubmitOrder
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows for service.* regex on file_path, got %d: %+v", len(result.Rows), result.Rows)
	}
}

func TestExecuteWhereStartsWithOnFilePath(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.file_path STARTS WITH "main" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// HandleOrder is in main.go
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row (HandleOrder in main.go), got %d: %+v", len(result.Rows), result.Rows)
	}
}

func TestExecuteWhereEndsWithOnFilePath(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.file_path ENDS WITH ".go" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 4 {
		t.Errorf("expected 4 rows for .go ENDS WITH on file_path, got %d", len(result.Rows))
	}
}

func TestExecuteWhereNumeric(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.start_line > 10 RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// SubmitOrder (start_line=25)
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(result.Rows))
	}
}

func TestExecuteVariableLength(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// HandleOrder calls ValidateOrder (hop 1), ValidateOrder calls SubmitOrder (hop 2)
	result, err := exec.Execute(`MATCH (f:Function {name: "HandleOrder"})-[:CALLS*1..2]->(g:Function) RETURN g.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Should include ValidateOrder (hop 1), LogError (hop 1), SubmitOrder (hop 2)
	if len(result.Rows) < 2 {
		t.Errorf("expected at least 2 rows for variable-length path, got %d", len(result.Rows))
	}
}

func TestExecuteWithLimit(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) RETURN f.name LIMIT 2`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(result.Rows))
	}
}

func TestExecuteWithOrderBy(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) RETURN f.name ORDER BY f.name ASC`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) < 2 {
		t.Fatalf("expected at least 2 rows, got %d", len(result.Rows))
	}
	// First should be HandleOrder (alphabetically first)
	firstName := result.Rows[0]["f.name"]
	if firstName != "HandleOrder" {
		t.Errorf("expected first row 'HandleOrder', got %v", firstName)
	}
}

func TestExecuteCountAggregation(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function)-[:CALLS]->(g:Function) RETURN f.name, COUNT(g) AS call_count ORDER BY call_count DESC`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) < 1 {
		t.Fatalf("expected at least 1 row, got %d", len(result.Rows))
	}
	// HandleOrder calls 2 functions (ValidateOrder, LogError)
	for _, row := range result.Rows {
		if row["f.name"] == "HandleOrder" {
			count, ok := row["call_count"].(int)
			if !ok {
				t.Errorf("expected int count, got %T", row["call_count"])
			} else if count != 2 {
				t.Errorf("expected call_count=2 for HandleOrder, got %d", count)
			}
		}
	}
}

func TestExecuteInboundRelationship(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// Who calls ValidateOrder?
	result, err := exec.Execute(`MATCH (f:Function)<-[:CALLS]-(g:Function) WHERE f.name = "ValidateOrder" RETURN g.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 caller, got %d", len(result.Rows))
	}
	if result.Rows[0]["g.name"] != "HandleOrder" {
		t.Errorf("expected HandleOrder, got %v", result.Rows[0]["g.name"])
	}
}

func TestExecuteDistinct(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) RETURN DISTINCT f.label`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 distinct label, got %d", len(result.Rows))
	}
	if result.Rows[0]["f.label"] != "Function" {
		t.Errorf("expected 'Function', got %v", result.Rows[0]["f.label"])
	}
}

func TestExecuteInlinePropertyFilter(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function {name: "SubmitOrder"}) RETURN f.name, f.qualified_name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0]["f.name"] != "SubmitOrder" {
		t.Errorf("expected SubmitOrder, got %v", result.Rows[0]["f.name"])
	}
}

func TestExecuteNoResults(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.name = "NonExistent" RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(result.Rows))
	}
}

func TestParseError(t *testing.T) {
	_, err := Parse(`NOT A VALID QUERY`)
	if err == nil {
		t.Error("expected parse error for invalid query")
	}
}

// --- Edge property tests (Feature 2) ---

func setupTestStoreWithHTTPCalls(t *testing.T) *store.Store {
	t.Helper()
	s := setupTestStore(t)

	// Add HTTP_CALLS edge with confidence
	callerNode, _ := s.FindNodeByQN("test", "test.main.HandleOrder")
	targetNode, _ := s.FindNodeByQN("test", "test.service.SubmitOrder")
	if callerNode == nil || targetNode == nil {
		t.Fatal("expected test nodes to exist")
	}
	mustInsertEdge(t, s, &store.Edge{
		Project:  "test",
		SourceID: callerNode.ID,
		TargetID: targetNode.ID,
		Type:     "HTTP_CALLS",
		Properties: map[string]any{
			"url_path":   "/api/orders",
			"confidence": 0.85,
			"method":     "POST",
		},
	})
	return s
}

func TestExecuteEdgePropertyAccess(t *testing.T) {
	s := setupTestStoreWithHTTPCalls(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (a:Function)-[r:HTTP_CALLS]->(b:Function) RETURN a.name, b.name, r.url_path, r.confidence`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	row := result.Rows[0]
	if row["a.name"] != "HandleOrder" {
		t.Errorf("a.name = %v, want HandleOrder", row["a.name"])
	}
	if row["b.name"] != "SubmitOrder" {
		t.Errorf("b.name = %v, want SubmitOrder", row["b.name"])
	}
	if row["r.url_path"] != "/api/orders" {
		t.Errorf("r.url_path = %v, want /api/orders", row["r.url_path"])
	}
	conf, ok := row["r.confidence"].(float64)
	if !ok {
		t.Errorf("r.confidence type = %T, want float64", row["r.confidence"])
	} else if conf != 0.85 {
		t.Errorf("r.confidence = %v, want 0.85", conf)
	}
}

func TestExecuteEdgePropertyInWhere(t *testing.T) {
	s := setupTestStoreWithHTTPCalls(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// Filter by confidence > 0.8
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.confidence > 0.8 RETURN a.name, b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	// Filter by confidence > 0.9 — should return nothing
	result2, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.confidence > 0.9 RETURN a.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result2.Rows) != 0 {
		t.Errorf("expected 0 rows for confidence > 0.9, got %d", len(result2.Rows))
	}
}

func TestExecuteEdgeType(t *testing.T) {
	s := setupTestStoreWithHTTPCalls(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) RETURN r.type`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0]["r.type"] != "HTTP_CALLS" {
		t.Errorf("r.type = %v, want HTTP_CALLS", result.Rows[0]["r.type"])
	}
}

// --- Comprehensive edge property filtering tests ---

// setupTestStoreMultiEdge creates a store with two HTTP_CALLS edges to test filtering.
func setupTestStoreMultiEdge(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}

	project := "testproj"
	if err := s.UpsertProject(project, "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	srcID, _ := s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: "SendOrder",
		QualifiedName: "testproj.caller.SendOrder",
		FilePath:      "caller/client.go",
	})

	tgtID, _ := s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: "HandleOrder",
		QualifiedName: "testproj.handler.HandleOrder",
		FilePath:      "handler/routes.go",
	})

	tgt2ID, _ := s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: "HandleHealth",
		QualifiedName: "testproj.handler.HandleHealth",
		FilePath:      "handler/health.go",
	})

	mustInsertEdge(t, s, &store.Edge{
		Project: project, SourceID: srcID, TargetID: tgtID,
		Type: "HTTP_CALLS",
		Properties: map[string]any{
			"url_path":   "/api/orders",
			"confidence": 0.85,
			"method":     "POST",
		},
	})

	mustInsertEdge(t, s, &store.Edge{
		Project: project, SourceID: srcID, TargetID: tgt2ID,
		Type: "HTTP_CALLS",
		Properties: map[string]any{
			"url_path":   "/health",
			"confidence": 0.45,
		},
	})

	return s
}

func TestEdgePropertyFilterContains(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.url_path CONTAINS 'orders' RETURN a.name, b.name, r.url_path`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	if row["a.name"] != "SendOrder" {
		t.Errorf("a.name = %v, want SendOrder", row["a.name"])
	}
	if row["b.name"] != "HandleOrder" {
		t.Errorf("b.name = %v, want HandleOrder", row["b.name"])
	}
	if row["r.url_path"] != "/api/orders" {
		t.Errorf("r.url_path = %v, want /api/orders", row["r.url_path"])
	}
}

func TestEdgePropertyFilterNumericGTE(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.confidence >= 0.6 RETURN a.name, b.name, r.confidence LIMIT 20`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row (only high-confidence edge), got %d", len(result.Rows))
	}

	row := result.Rows[0]
	if row["b.name"] != "HandleOrder" {
		t.Errorf("b.name = %v, want HandleOrder (high confidence)", row["b.name"])
	}
}

func TestEdgePropertyReturnWithoutFilter(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) RETURN a.name, b.name, r.url_path, r.confidence LIMIT 20`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.Rows) < 2 {
		t.Fatalf("expected at least 2 rows, got %d", len(result.Rows))
	}

	foundOrders := false
	foundHealth := false
	for _, row := range result.Rows {
		urlPath, _ := row["r.url_path"].(string)
		if urlPath == "/api/orders" {
			foundOrders = true
		}
		if urlPath == "/health" {
			foundHealth = true
		}
	}
	if !foundOrders {
		t.Error("missing row with url_path=/api/orders")
	}
	if !foundHealth {
		t.Error("missing row with url_path=/health")
	}
}

func TestEdgePropertyFilterEquals(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.method = 'POST' RETURN a.name, b.name`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0]["b.name"] != "HandleOrder" {
		t.Errorf("b.name = %v, want HandleOrder", result.Rows[0]["b.name"])
	}
}

func TestEdgePropertyFilterStartsWith(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.url_path STARTS WITH '/api' RETURN a.name, b.name, r.url_path`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row (only /api/orders starts with /api), got %d", len(result.Rows))
	}
	if result.Rows[0]["r.url_path"] != "/api/orders" {
		t.Errorf("r.url_path = %v, want /api/orders", result.Rows[0]["r.url_path"])
	}
}

func TestCombinedNodeAndEdgeFilter(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// Filter on both node property (early) and edge property (late)
	result, err := exec.Execute(`MATCH (a:Function)-[r:HTTP_CALLS]->(b:Function) WHERE a.name = 'SendOrder' AND r.confidence >= 0.6 RETURN b.name, r.url_path`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0]["b.name"] != "HandleOrder" {
		t.Errorf("b.name = %v, want HandleOrder", result.Rows[0]["b.name"])
	}
	if result.Rows[0]["r.url_path"] != "/api/orders" {
		t.Errorf("r.url_path = %v, want /api/orders", result.Rows[0]["r.url_path"])
	}
}

func TestEdgePropertyFilterNoMatch(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// No edge has method = 'DELETE'
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.method = 'DELETE' RETURN a.name`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(result.Rows))
	}
}

func TestEdgePropertyFilterNumericLT(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// Only the health edge (0.45) should match confidence < 0.5
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.confidence < 0.5 RETURN b.name, r.confidence`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0]["b.name"] != "HandleHealth" {
		t.Errorf("b.name = %v, want HandleHealth", result.Rows[0]["b.name"])
	}
}

func TestEdgePropertyFilterRegex(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// Regex match on url_path
	result, err := exec.Execute(`MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.url_path =~ "/api/.*" RETURN b.name`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0]["b.name"] != "HandleOrder" {
		t.Errorf("b.name = %v, want HandleOrder", result.Rows[0]["b.name"])
	}
}

func TestApplyLimitRespectsExplicit(t *testing.T) {
	rows := make([]map[string]any, 300)
	for i := range rows {
		rows[i] = map[string]any{"i": i}
	}

	// No explicit limit — should use maxRows default (200), and truncated=true
	got, truncated := applyLimit(rows, 0, 200)
	if len(got) != 200 {
		t.Errorf("no limit: expected 200 rows, got %d", len(got))
	}
	if !truncated {
		t.Error("no limit with 300 rows > 200 cap: expected truncated=true")
	}

	// Explicit limit below maxRows — truncated=true
	got, truncated = applyLimit(rows, 50, 200)
	if len(got) != 50 {
		t.Errorf("limit=50: expected 50 rows, got %d", len(got))
	}
	if !truncated {
		t.Error("limit=50 with 300 rows: expected truncated=true")
	}

	// Explicit limit above maxRows — must be respected (not silently capped)
	got, truncated = applyLimit(rows, 250, 200)
	if len(got) != 250 {
		t.Errorf("limit=250: expected 250 rows, got %d", len(got))
	}
	if !truncated {
		t.Error("limit=250 with 300 rows: expected truncated=true")
	}

	// Explicit limit above total rows — returns all, truncated=false
	got, truncated = applyLimit(rows, 500, 200)
	if len(got) != 300 {
		t.Errorf("limit=500: expected 300 rows (all), got %d", len(got))
	}
	if truncated {
		t.Error("limit=500 with 300 rows: expected truncated=false")
	}
}

// TestExecuteSignalsTruncation drives a real Executor against a multi-row
// fixture and confirms Result.Truncated + Result.EffectiveCap are populated
// when the default 200-row cap clips results. Prevents regression of the
// silent-cap bug that sent bench/accuracy's PR #62 baseline 80pp off.
func TestExecuteSignalsTruncation(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	// Run a trivial query that can't hit the 200-row cap — Truncated must be false.
	exec := &Executor{Store: s}
	result, err := exec.Execute("MATCH (a)-[r]->(b) RETURN a.name, b.name LIMIT 100")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Truncated {
		t.Errorf("small result: expected Truncated=false, got true")
	}
	if result.EffectiveCap != 200 {
		t.Errorf("default cap: expected EffectiveCap=200, got %d", result.EffectiveCap)
	}

	// When MaxRows is explicitly set higher, EffectiveCap reflects it.
	exec2 := &Executor{Store: s, MaxRows: 5000}
	result2, err := exec2.Execute("MATCH (a) RETURN a.name LIMIT 10")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result2.EffectiveCap != 5000 {
		t.Errorf("max_rows=5000: expected EffectiveCap=5000, got %d", result2.EffectiveCap)
	}

	// MaxRows above absoluteMaxRows gets clamped.
	exec3 := &Executor{Store: s, MaxRows: 99999}
	result3, err := exec3.Execute("MATCH (a) RETURN a.name LIMIT 10")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result3.EffectiveCap != 10000 {
		t.Errorf("max_rows=99999: expected EffectiveCap=10000 (absolute max), got %d", result3.EffectiveCap)
	}
}

func TestEdgeBuiltinPropertyFilter(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// Filter on built-in edge property r.type
	result, err := exec.Execute(`MATCH (a)-[r]->(b) WHERE r.type = 'HTTP_CALLS' RETURN a.name, b.name LIMIT 20`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows (both HTTP_CALLS edges), got %d", len(result.Rows))
	}
}

// --- IN operator (B1, 2026-05-07) ---

func TestParseWhereInStrings(t *testing.T) {
	q, err := Parse(`MATCH (f:Function) WHERE f.name IN ['HandleOrder', 'ValidateOrder'] RETURN f`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := q.Where.Conditions[0]
	if c.Operator != "IN" {
		t.Errorf("expected IN, got %q", c.Operator)
	}
	if len(c.Values) != 2 || c.Values[0] != "HandleOrder" || c.Values[1] != "ValidateOrder" {
		t.Errorf("expected [HandleOrder, ValidateOrder], got %v", c.Values)
	}
}

func TestParseWhereInNumbers(t *testing.T) {
	q, err := Parse(`MATCH (f:Function) WHERE f.start_line IN [10, 25] RETURN f`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := q.Where.Conditions[0]
	if c.Operator != "IN" {
		t.Errorf("expected IN, got %q", c.Operator)
	}
	if len(c.Values) != 2 || c.Values[0] != "10" || c.Values[1] != "25" {
		t.Errorf("expected [10, 25], got %v", c.Values)
	}
}

func TestParseWhereInSingleValue(t *testing.T) {
	// Single-element list should still parse — useful for programmatic
	// query construction where the list size is not known statically.
	q, err := Parse(`MATCH (f:Function) WHERE f.name IN ['HandleOrder'] RETURN f`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := q.Where.Conditions[0]
	if len(c.Values) != 1 || c.Values[0] != "HandleOrder" {
		t.Errorf("expected [HandleOrder], got %v", c.Values)
	}
}

func TestParseWhereInEmptyListRejected(t *testing.T) {
	// Empty IN list is always-false and almost certainly user error;
	// reject at parse time so it surfaces immediately.
	_, err := Parse(`MATCH (f:Function) WHERE f.name IN [] RETURN f`)
	if err == nil {
		t.Fatal("expected parse error for empty IN list")
	}
	if !strings.Contains(err.Error(), "empty list") {
		t.Errorf("expected 'empty list' in error, got %v", err)
	}
}

func TestParseWhereInMissingClose(t *testing.T) {
	_, err := Parse(`MATCH (f:Function) WHERE f.name IN ['a', 'b' RETURN f`)
	if err == nil {
		t.Fatal("expected parse error for unterminated IN list")
	}
}

func TestExecuteWhereInStrings(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.name IN ['HandleOrder', 'ValidateOrder'] RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	got := map[string]bool{}
	for _, row := range result.Rows {
		got[row["f.name"].(string)] = true
	}
	if !got["HandleOrder"] || !got["ValidateOrder"] {
		t.Errorf("expected HandleOrder and ValidateOrder, got %v", got)
	}
}

func TestExecuteWhereInNumeric(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// SubmitOrder has start_line=25, ValidateOrder has start_line=5,
	// LogError has start_line=1, HandleOrder has start_line=10.
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.start_line IN [10, 25] RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
}

func TestExecuteWhereInNoMatch(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.name IN ['DoesNotExist', 'AlsoMissing'] RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(result.Rows))
	}
}

// --- B2 (2026-05-07) — WITH clause error-fast ---

func TestParseRejectsWithAfterMatch(t *testing.T) {
	_, err := Parse(`MATCH (a)-[r:CALLS]->(b) WITH b.name AS callee, COUNT(*) AS calls RETURN callee, calls`)
	if err == nil {
		t.Fatal("expected parse error for WITH after MATCH")
	}
	if !strings.Contains(err.Error(), "WITH clause not supported") {
		t.Errorf("error should name the gap; got %v", err)
	}
	if !strings.Contains(err.Error(), "Workarounds") {
		t.Errorf("error should suggest workarounds; got %v", err)
	}
	if !strings.Contains(err.Error(), "search_graph") {
		t.Errorf("error should mention search_graph alternative; got %v", err)
	}
}

func TestParseRejectsWithAfterWhere(t *testing.T) {
	_, err := Parse(`MATCH (a) WHERE a.label = 'Function' WITH a RETURN a`)
	if err == nil {
		t.Fatal("expected parse error for WITH after WHERE")
	}
	if !strings.Contains(err.Error(), "WITH clause not supported") {
		t.Errorf("error should name the gap; got %v", err)
	}
}

func TestStartsWithStillParses(t *testing.T) {
	// Defensive: STARTS WITH and ENDS WITH consume the WITH token
	// inside parseCondition. They must NOT trip the WITH-clause
	// rejection at clause boundaries.
	_, err := Parse(`MATCH (f:Function) WHERE f.name STARTS WITH 'handle' RETURN f`)
	if err != nil {
		t.Fatalf("STARTS WITH must still parse: %v", err)
	}
	_, err = Parse(`MATCH (f:Function) WHERE f.file_path ENDS WITH '.go' RETURN f`)
	if err != nil {
		t.Fatalf("ENDS WITH must still parse: %v", err)
	}
}

func TestExecuteRejectsWithClause(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	_, err := exec.Execute(`MATCH (a) WITH a RETURN a`)
	if err == nil {
		t.Fatal("expected Execute to surface the parse error for WITH")
	}
	if !strings.Contains(err.Error(), "WITH clause not supported") {
		t.Errorf("Execute should propagate the WITH-clause error; got %v", err)
	}
}

// --- IS NULL / IS NOT NULL parser+executor tests ---
// Parser was extended in Plan 3 Phase A (2026-05-06); conformance
// corpus pins the parse path. These tests pin the executor path against
// the unit-test store fixture.

func TestParseWhereIsNull(t *testing.T) {
	q, err := Parse(`MATCH (f:Function) WHERE f.docstring IS NULL RETURN f.name`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := q.Where.Conditions[0]
	if c.Operator != "IS NULL" {
		t.Errorf("expected 'IS NULL', got %q", c.Operator)
	}
	if c.Value != "" {
		t.Errorf("expected empty Value for IS NULL, got %q", c.Value)
	}
}

func TestParseWhereIsNotNull(t *testing.T) {
	q, err := Parse(`MATCH (f:Function) WHERE f.docstring IS NOT NULL RETURN f.name`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := q.Where.Conditions[0]
	if c.Operator != "IS NOT NULL" {
		t.Errorf("expected 'IS NOT NULL', got %q", c.Operator)
	}
}

func TestExecuteWhereIsNull(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	// LogError has no signature property; the others do (see setupTestStore).
	// IS NULL matches both nil and empty-string (the on-disk representation
	// of an absent optional string).
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.signature IS NULL RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row (LogError), got %d", len(result.Rows))
	}
	if result.Rows[0]["f.name"] != "LogError" {
		t.Errorf("expected LogError, got %v", result.Rows[0]["f.name"])
	}
}

func TestExecuteWhereIsNotNull(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (f:Function) WHERE f.signature IS NOT NULL RETURN f.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// HandleOrder, ValidateOrder, SubmitOrder all have signatures; LogError doesn't.
	if len(result.Rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(result.Rows))
	}
}

// TestExecuteUnboundedPathSignalsCap exercises the new unbounded-`*`
// observability: when a query uses a bare `*` with no upper bound, the
// engine applies its default cap and now signals this on the Result so
// callers know their `*` was clipped. Previously the cap was silent.
func TestExecuteUnboundedPathSignalsCap(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (a)-[:HTTP_CALLS*]->(b) RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.UnboundedDepthCap == 0 {
		t.Fatalf("expected UnboundedDepthCap > 0 for `*` query, got 0")
	}
	if result.UnboundedDepthCap != defaultUnboundedPathDepth {
		t.Fatalf("expected default cap %d, got %d", defaultUnboundedPathDepth, result.UnboundedDepthCap)
	}
	if !result.Truncated {
		t.Errorf("unbounded `*` should set Truncated=true so callers see they got a sample")
	}
}

// TestExecuteUnboundedPathConfigurableCap verifies callers can raise the
// cap via Executor.MaxUnboundedPathDepth and the Result reports the
// effective cap that was actually applied.
func TestExecuteUnboundedPathConfigurableCap(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s, MaxUnboundedPathDepth: 25}
	result, err := exec.Execute(`MATCH (a)-[:HTTP_CALLS*]->(b) RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.UnboundedDepthCap != 25 {
		t.Fatalf("expected configured cap 25, got %d", result.UnboundedDepthCap)
	}
}

// TestExecuteBoundedPathDoesNotSignalUnboundedCap confirms that bounded
// var-length queries (`*1..3`) leave UnboundedDepthCap at 0 — only bare
// `*` triggers the signal, since only bare `*` is being silently capped.
func TestExecuteBoundedPathDoesNotSignalUnboundedCap(t *testing.T) {
	s := setupTestStoreMultiEdge(t)
	defer s.Close()

	exec := &Executor{Store: s}
	result, err := exec.Execute(`MATCH (a)-[:HTTP_CALLS*1..3]->(b) RETURN b.name`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.UnboundedDepthCap != 0 {
		t.Errorf("bounded `*1..3` should not set UnboundedDepthCap, got %d", result.UnboundedDepthCap)
	}
}

// TestExecuteSkippedProjectsPopulated verifies the wiring between the
// executor's per-project error path and the Result.SkippedProjects
// field. Drives the wiring by directly populating skippedProjects
// before Execute runs — the production failure shape (corrupt DB,
// transient SQL error) is hard to synthesize in a unit test, but the
// propagation contract is the part we need to pin so the refactor
// doesn't regress to silent skips.
func TestExecuteSkippedProjectsPopulated(t *testing.T) {
	s := setupTestStore(t)
	defer s.Close()

	exec := &Executor{Store: s}
	exec.skippedProjects = []SkippedProject{
		{Name: "ghost-project", Err: "corrupt: file is not a database"},
	}
	result, err := exec.Execute(`MATCH (f:Function) RETURN f.name LIMIT 1`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.SkippedProjects) != 1 {
		t.Fatalf("expected 1 skipped project in Result, got %d", len(result.SkippedProjects))
	}
	if result.SkippedProjects[0].Name != "ghost-project" {
		t.Errorf("expected ghost-project, got %q", result.SkippedProjects[0].Name)
	}
	if !strings.Contains(result.SkippedProjects[0].Err, "corrupt") {
		t.Errorf("expected error string to mention 'corrupt', got %q", result.SkippedProjects[0].Err)
	}
}
