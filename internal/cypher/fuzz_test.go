package cypher

import (
	"fmt"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// FuzzParse asserts the read-only Cypher parser is robust to arbitrary input:
// it must NEVER panic and must always terminate (a malformed query from an
// agent must not crash or hang the server). Run:
//
//	go test -run=^$ -fuzz=FuzzParse -fuzztime=30s ./internal/cypher/
func FuzzParse(f *testing.F) {
	seeds := []string{
		`MATCH (f:Function) RETURN f`,
		`MATCH (f)-[:CALLS*1..3]->(g) WHERE f.name =~ ".*x" RETURN g.name, COUNT(g) AS c ORDER BY c DESC LIMIT 10`,
		`MATCH (f:Function) WHERE f.name STARTS WITH "S" AND f.params IS NOT NULL RETURN f`,
		`MATCH (a)-[:CALLS|HTTP_CALLS]->(b)<-[:DEFINES]-(c) RETURN DISTINCT a.name`,
		`MATCH (f) WHERE f.x IN ["a","b"] RETURN labels(f), COUNT(DISTINCT f)`,
		``, `MATCH`, `RETURN`, `MATCH ( RETURN`, `MATCH (f {a:}) RETURN f`,
		`MATCH (f)-[:CALLS*..]->(g) RETURN g`, `MATCH (f)-[:CALLS*999999999]->(g) RETURN g`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, q string) {
		// Property under test: Parse never panics, always returns.
		_, _ = Parse(q)
	})
}

// FuzzExecute asserts the FULL query path — parse, plan, execute against a
// populated store — never panics on arbitrary input. FuzzParse (above) only
// covers the parser; the 2026-06-10 sweeps found executor-level bugs in
// queries that parse fine, so plan/execute need crash coverage too. Run:
//
//	go test -run=^$ -fuzz=FuzzExecute -fuzztime=60s ./internal/cypher/
func FuzzExecute(f *testing.F) {
	seeds := []string{
		`MATCH (f:Function) RETURN f`,
		`MATCH (a:Function)-[:CALLS]->(b:Function) RETURN COUNT(*)`,
		`MATCH (a)-[r:CALLS|HTTP_CALLS*0..3]-(b) WHERE r.confidence >= 0.5 RETURN b.name LIMIT 0`,
		`MATCH (n:Function) WHERE n.name = "HandleOrder" OR n.name = "x" RETURN DISTINCT n.name ORDER BY n.name DESC LIMIT 3`,
		`MATCH (m:Module)-[:DEFINES]->()-[:CALLS]->(g) RETURN m.name, COUNT(g) AS c ORDER BY c`,
		`MATCH (n) WHERE n.start_line IN [1, 10, 25] RETURN labels(n), n.id`,
		`MATCH (a)<-[:CALLS]-(b) WHERE a.name =~ "(?i).*order" RETURN a`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	store := setupFuzzStore(f)
	f.Fuzz(func(t *testing.T, q string) {
		exec := &Executor{Store: store, MaxRows: 50}
		// Property under test: Execute never panics, always returns.
		_, _ = exec.Execute(q)
	})
}

// setupFuzzStore builds the shared fixture graph once per fuzz process.
func setupFuzzStore(f *testing.F) *store.Store {
	f.Helper()
	s, err := store.OpenMemory()
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { s.Close() })
	if err := s.UpsertProject("test", "/tmp/fuzz"); err != nil {
		f.Fatal(err)
	}
	ids := make([]int64, 0, 6)
	for i, n := range []struct {
		label, name string
	}{
		{"Function", "HandleOrder"}, {"Function", "ValidateOrder"}, {"Function", "SubmitOrder"},
		{"Function", "LogError"}, {"Module", "main"}, {"Class", "Order"},
	} {
		id, err := s.UpsertNode(&store.Node{
			Project: "test", Label: n.label, Name: n.name,
			QualifiedName: fmt.Sprintf("test.f%d.%s", i, n.name),
			FilePath:      "main.go", StartLine: i*10 + 1, EndLine: i*10 + 9,
			Properties: map[string]any{"signature": n.name + "()", "id": i},
		})
		if err != nil {
			f.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, e := range [][3]any{
		{ids[0], ids[1], "CALLS"}, {ids[1], ids[2], "CALLS"}, {ids[0], ids[3], "CALLS"},
		{ids[4], ids[0], "DEFINES"}, {ids[0], ids[2], "HTTP_CALLS"}, {ids[2], ids[0], "CALLS"},
		{ids[3], ids[3], "CALLS"},
	} {
		if _, err := s.InsertEdge(&store.Edge{
			Project: "test", SourceID: e[0].(int64), TargetID: e[1].(int64), Type: e[2].(string),
			Properties: map[string]any{"confidence": 0.8},
		}); err != nil {
			f.Fatal(err)
		}
	}
	return s
}
