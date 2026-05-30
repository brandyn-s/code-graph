package cypher

import "testing"

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
