package cypher

// Metamorphic property tests for the query engine. Each relation asserts an
// algebraic equivalence between two queries that exercise DIFFERENT internal
// execution paths (fused JOIN vs batch expand, SQL aggregate vs Go
// aggregation, scan-side pushdown vs Go evaluation) on randomized graphs.
// The 2026-06-10 correctness sweep showed the fast paths and their fallbacks
// drifted apart silently; these relations make that class of bug a
// deterministic test failure instead of a code-review catch.
//
// CI runs metamorphicSeeds deterministic seeds. For a longer soak:
//
//	CBM_METAMORPHIC_SEEDS=2000 go test ./internal/cypher/ -run TestMetamorphic -count=1

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

const metamorphicSeeds = 50

// metaGraph is a randomly generated small graph plus the vocabulary used to
// generate it (so queries can reference values that actually occur).
type metaGraph struct {
	s      *store.Store
	labels []string
	types  []string
	names  []string
}

func buildMetaGraph(t *testing.T, seed int64) *metaGraph {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.UpsertProject("meta", "/tmp/meta"); err != nil {
		t.Fatal(err)
	}

	g := &metaGraph{
		s:      s,
		labels: []string{"A", "B"},
		types:  []string{"T", "U"},
		names:  []string{"x", "y", "z", "w"},
	}

	nNodes := 6 + rng.Intn(10)
	ids := make([]int64, 0, nNodes)
	for i := 0; i < nNodes; i++ {
		id, err := s.UpsertNode(&store.Node{
			Project:       "meta",
			Label:         g.labels[rng.Intn(len(g.labels))],
			Name:          g.names[rng.Intn(len(g.names))],
			QualifiedName: fmt.Sprintf("meta.n%d", i),
			FilePath:      fmt.Sprintf("f%d.go", rng.Intn(3)),
			StartLine:     1 + rng.Intn(50),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	nEdges := 8 + rng.Intn(20)
	for i := 0; i < nEdges; i++ {
		src := ids[rng.Intn(len(ids))]
		tgt := ids[rng.Intn(len(ids))]
		// InsertEdge upserts on (source, target, type); duplicates collapse,
		// which matches the store's modeling (one edge per typed pair).
		if _, err := s.InsertEdge(&store.Edge{
			Project: "meta", SourceID: src, TargetID: tgt,
			Type: g.types[rng.Intn(len(g.types))],
		}); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

// rowsKey canonicalizes a result into a sorted multiset of row strings over
// the given columns, so two queries can be compared independent of row and
// map ordering.
func rowsKey(t *testing.T, res *Result, cols ...string) []string {
	t.Helper()
	out := make([]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		parts := make([]string, len(cols))
		for i, c := range cols {
			parts[i] = fmt.Sprintf("%v", r[c])
		}
		out = append(out, strings.Join(parts, "|"))
	}
	sort.Strings(out)
	return out
}

func mustExec(t *testing.T, g *metaGraph, q string) *Result {
	t.Helper()
	exec := &Executor{Store: g.s}
	res, err := exec.Execute(q)
	if err != nil {
		t.Fatalf("execute %q: %v", q, err)
	}
	return res
}

func assertSameRows(t *testing.T, seed int64, relation, q1, q2 string, r1, r2 []string) {
	t.Helper()
	if strings.Join(r1, "\n") != strings.Join(r2, "\n") {
		t.Errorf("[seed %d] %s diverged:\n  Q1: %s -> %d rows %v\n  Q2: %s -> %d rows %v",
			seed, relation, q1, len(r1), r1, q2, len(r2), r2)
	}
}

func metamorphicSeedCount() int {
	if v := os.Getenv("CBM_METAMORPHIC_SEEDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return metamorphicSeeds
}

func TestMetamorphicEngineProperties(t *testing.T) {
	for seed := int64(1); seed <= int64(metamorphicSeedCount()); seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			g := buildMetaGraph(t, seed)
			rng := rand.New(rand.NewSource(seed * 7919))
			label := g.labels[rng.Intn(len(g.labels))]
			typ := g.types[rng.Intn(len(g.types))]
			name1 := g.names[rng.Intn(len(g.names))]
			name2 := g.names[rng.Intn(len(g.names))]

			// R1 — inline-prop filter ≡ WHERE equality. The inline-prop form
			// blocks JOIN fusion (batch expand path); the WHERE form uses the
			// fused JOIN with SQL pushdown. Same logical query, two paths.
			q1 := fmt.Sprintf(`MATCH (a:%s {name: "%s"})-[:%s]->(b) RETURN a.qualified_name, b.qualified_name`, label, name1, typ)
			q2 := fmt.Sprintf(`MATCH (a:%s)-[:%s]->(b) WHERE a.name = "%s" RETURN a.qualified_name, b.qualified_name`, label, typ, name1)
			cols := []string{"a.qualified_name", "b.qualified_name"}
			assertSameRows(t, seed, "R1 inline-prop vs WHERE",
				q1, q2, rowsKey(t, mustExec(t, g, q1), cols...), rowsKey(t, mustExec(t, g, q2), cols...))

			// R2 — OR ≡ union of branches (scan pushdown path vs two scans).
			qOr := fmt.Sprintf(`MATCH (n:%s) WHERE n.name = "%s" OR n.name = "%s" RETURN n.qualified_name`, label, name1, name2)
			qA := fmt.Sprintf(`MATCH (n:%s) WHERE n.name = "%s" RETURN n.qualified_name`, label, name1)
			qB := fmt.Sprintf(`MATCH (n:%s) WHERE n.name = "%s" RETURN n.qualified_name`, label, name2)
			union := map[string]bool{}
			for _, r := range rowsKey(t, mustExec(t, g, qA), "n.qualified_name") {
				union[r] = true
			}
			for _, r := range rowsKey(t, mustExec(t, g, qB), "n.qualified_name") {
				union[r] = true
			}
			want := make([]string, 0, len(union))
			for r := range union {
				want = append(want, r)
			}
			sort.Strings(want)
			assertSameRows(t, seed, "R2 OR vs union", qOr, qA+" ∪ "+qB,
				rowsKey(t, mustExec(t, g, qOr), "n.qualified_name"), want)

			// R3 — ungrouped COUNT(*) ≡ row count of the projected query
			// (SQL aggregate fast path vs fused JOIN row path).
			qCount := fmt.Sprintf(`MATCH (a:%s)-[:%s]->(b) RETURN COUNT(*)`, label, typ)
			qRows := fmt.Sprintf(`MATCH (a:%s)-[:%s]->(b) RETURN a.qualified_name, b.qualified_name`, label, typ)
			cnt := mustExec(t, g, qCount)
			rows := mustExec(t, g, qRows)
			if len(cnt.Rows) != 1 {
				t.Fatalf("[seed %d] R3: COUNT(*) returned %d rows", seed, len(cnt.Rows))
			}
			if got := fmt.Sprintf("%v", cnt.Rows[0]["COUNT(*)"]); got != fmt.Sprintf("%d", len(rows.Rows)) {
				t.Errorf("[seed %d] R3 COUNT mismatch: COUNT(*)=%v but %d projected rows\n  q: %s",
					seed, cnt.Rows[0]["COUNT(*)"], len(rows.Rows), qCount)
			}

			// R4 — LIMIT law: len == min(k, total) and every limited row is
			// drawn from the full multiset.
			k := 1 + rng.Intn(4)
			qLim := fmt.Sprintf(`MATCH (a:%s)-[:%s]->(b) RETURN a.qualified_name, b.qualified_name LIMIT %d`, label, typ, k)
			lim := rowsKey(t, mustExec(t, g, qLim), cols...)
			full := rowsKey(t, mustExec(t, g, qRows), cols...)
			wantLen := len(full)
			if k < wantLen {
				wantLen = k
			}
			if len(lim) != wantLen {
				t.Errorf("[seed %d] R4 LIMIT %d: got %d rows, want %d", seed, k, len(lim), wantLen)
			}
			fullSet := map[string]int{}
			for _, r := range full {
				fullSet[r]++
			}
			for _, r := range lim {
				if fullSet[r] == 0 {
					t.Errorf("[seed %d] R4: limited row %q not in full result", seed, r)
				}
				fullSet[r]--
			}

			// R5 — DISTINCT ≡ set of full rows.
			qDist := fmt.Sprintf(`MATCH (a:%s)-[:%s]->(b) RETURN DISTINCT b.name`, label, typ)
			qAll := fmt.Sprintf(`MATCH (a:%s)-[:%s]->(b) RETURN b.name`, label, typ)
			distinct := rowsKey(t, mustExec(t, g, qDist), "b.name")
			allSet := map[string]bool{}
			for _, r := range rowsKey(t, mustExec(t, g, qAll), "b.name") {
				allSet[r] = true
			}
			wantD := make([]string, 0, len(allSet))
			for r := range allSet {
				wantD = append(wantD, r)
			}
			sort.Strings(wantD)
			assertSameRows(t, seed, "R5 DISTINCT vs set", qDist, qAll, distinct, wantD)

			// R6 — direction symmetry: (a)-[:T]->(b) ≡ (b)<-[:T]-(a). The
			// reversed form scans the target side and expands inbound — a
			// different fused-JOIN orientation that must agree exactly.
			qFwd := fmt.Sprintf(`MATCH (a)-[:%s]->(b) RETURN a.qualified_name, b.qualified_name`, typ)
			qRev := fmt.Sprintf(`MATCH (b)<-[:%s]-(a) RETURN a.qualified_name, b.qualified_name`, typ)
			assertSameRows(t, seed, "R6 direction symmetry", qFwd, qRev,
				rowsKey(t, mustExec(t, g, qFwd), cols...), rowsKey(t, mustExec(t, g, qRev), cols...))

			// R7 — any-direction ≡ set(outbound pairs ∪ flipped outbound
			// pairs). Compared as sets: a self-loop yields one any-direction
			// binding but appears in both orientations of the union.
			qAny := fmt.Sprintf(`MATCH (a)-[:%s]-(b) RETURN a.qualified_name, b.qualified_name`, typ)
			anySet := map[string]bool{}
			for _, r := range rowsKey(t, mustExec(t, g, qAny), cols...) {
				anySet[r] = true
			}
			wantAny := map[string]bool{}
			for _, r := range rowsKey(t, mustExec(t, g, qFwd), cols...) {
				wantAny[r] = true
				parts := strings.SplitN(r, "|", 2)
				wantAny[parts[1]+"|"+parts[0]] = true
			}
			if len(anySet) != len(wantAny) {
				t.Errorf("[seed %d] R7 any-direction set mismatch: got %d distinct pairs, want %d\n  any: %v\n  want: %v",
					seed, len(anySet), len(wantAny), anySet, wantAny)
			} else {
				for r := range wantAny {
					if !anySet[r] {
						t.Errorf("[seed %d] R7: pair %q missing from any-direction result", seed, r)
					}
				}
			}

			// R8 — *1..1 variable-length ≡ fixed-length single hop (BFS path
			// vs batch/fused expand), compared as node-pair sets (the BFS
			// path deliberately doesn't bind edges).
			qVar := fmt.Sprintf(`MATCH (a:%s)-[:%s*1..1]->(b) RETURN a.qualified_name, b.qualified_name`, label, typ)
			qFix := fmt.Sprintf(`MATCH (a:%s)-[:%s]->(b) RETURN a.qualified_name, b.qualified_name`, label, typ)
			assertSameRows(t, seed, "R8 var-length 1..1 vs fixed", qVar, qFix,
				rowsKey(t, mustExec(t, g, qVar), cols...), rowsKey(t, mustExec(t, g, qFix), cols...))

			// R10 — IN [x, y] ≡ the OR of equalities.
			qIn := fmt.Sprintf(`MATCH (n:%s) WHERE n.name IN ["%s", "%s"] RETURN n.qualified_name`, label, name1, name2)
			assertSameRows(t, seed, "R10 IN vs OR", qIn, qOr,
				rowsKey(t, mustExec(t, g, qIn), "n.qualified_name"),
				rowsKey(t, mustExec(t, g, qOr), "n.qualified_name"))

			// R11 — grouped COUNT ≡ manual group-by over projected rows
			// (SQL aggregate path vs row path).
			qGrp := fmt.Sprintf(`MATCH (a)-[r:%s]->(b) RETURN a.qualified_name, COUNT(r)`, typ)
			grp := mustExec(t, g, qGrp)
			manual := map[string]int{}
			for _, r := range rowsKey(t, mustExec(t, g, qFwd), cols...) {
				caller := strings.SplitN(r, "|", 2)[0]
				manual[caller]++
			}
			if len(grp.Rows) != len(manual) {
				t.Errorf("[seed %d] R11 group count: %d groups, want %d", seed, len(grp.Rows), len(manual))
			}
			for _, row := range grp.Rows {
				caller := fmt.Sprintf("%v", row["a.qualified_name"])
				if got := fmt.Sprintf("%v", row["COUNT(r)"]); got != fmt.Sprintf("%d", manual[caller]) {
					t.Errorf("[seed %d] R11 group %s: COUNT(r)=%v, manual=%d", seed, caller, row["COUNT(r)"], manual[caller])
				}
			}

			// R12 — equality and its complement partition the label scan
			// (name is never empty in generated graphs).
			qNeq := fmt.Sprintf(`MATCH (n:%s) WHERE n.name <> "%s" RETURN n.qualified_name`, label, name1)
			qScan := fmt.Sprintf(`MATCH (n:%s) RETURN n.qualified_name`, label)
			eq := rowsKey(t, mustExec(t, g, qA), "n.qualified_name")
			neq := rowsKey(t, mustExec(t, g, qNeq), "n.qualified_name")
			scan := rowsKey(t, mustExec(t, g, qScan), "n.qualified_name")
			both := append(append([]string{}, eq...), neq...)
			sort.Strings(both)
			assertSameRows(t, seed, "R12 partition by <>", qA+" ⊎ "+qNeq, qScan, both, scan)
		})
	}
}
