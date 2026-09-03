package pipeline

// Rigorous test battery (keyless): determinism, metamorphic invariances, and
// mutation testing for the indexing pipeline. These catch the bug CLASSES that
// bit this project — nondeterministic output (the Louvain bug), formatting
// sensitivity, and the graph not reflecting source reality — without any
// hand-oracle or API key. Reuses setupTestRepo/writeFile from pipeline_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/store"
)

// indexToMemory runs the full pipeline over dir into a fresh in-memory store.
func indexToMemory(t *testing.T, dir string) (*store.Store, string) {
	t.Helper()
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	p := New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}
	return s, p.ProjectName
}

var batteryNodeLabels = []string{
	"Project", "Package", "Folder", "File", "Module", "Class", "Function",
	"Method", "Interface", "Enum", "Type", "Route", "EnvVar",
}

// batteryStructuralEdges excludes MEMBER_OF (community membership), which is
// covered by the dedicated TestLouvainDeterministic; here we pin the structural
// graph (definitions, calls, imports, types, ...).
var batteryStructuralEdges = []string{
	"CONTAINS_PACKAGE", "CONTAINS_FOLDER", "CONTAINS_FILE", "DEFINES",
	"DEFINES_METHOD", "IMPORTS", "CALLS", "HTTP_CALLS", "ASYNC_CALLS",
	"IMPLEMENTS", "HANDLES", "USAGE", "CONFIGURES", "WRITES", "TESTS",
	"USES_TYPE", "READS_ENV",
}

// canonicalGraph returns ID-independent, sorted canonical representations of the
// node set ("label|qualified_name") and edge set ("srcQN|TYPE|tgtQN"). Because
// node IDs are auto-increment (differ run to run), edges are keyed by the
// qualified names of their endpoints — so any difference reflects a real
// structural or naming change, not ID churn.
func canonicalGraph(t *testing.T, s *store.Store, project string) (nodeKeys, edgeKeys []string) {
	t.Helper()
	// QNs embed the (path-derived) project name; strip it so graphs indexed at
	// different filesystem paths compare structurally.
	strip := func(qn string) string {
		if qn == project {
			return "<ROOT>"
		}
		return strings.TrimPrefix(qn, project+".")
	}
	idToQN := map[int64]string{}
	for _, lbl := range batteryNodeLabels {
		ns, err := s.FindNodesByLabel(project, lbl)
		if err != nil {
			t.Fatalf("FindNodesByLabel(%s): %v", lbl, err)
		}
		for _, n := range ns {
			nodeKeys = append(nodeKeys, lbl+"|"+strip(n.QualifiedName))
			idToQN[n.ID] = lbl + ":" + strip(n.QualifiedName)
		}
	}
	qn := func(id int64) string {
		if v, ok := idToQN[id]; ok {
			return v
		}
		return "?" // endpoint outside the enumerated labels (e.g. Community)
	}
	for _, et := range batteryStructuralEdges {
		es, err := s.FindEdgesByType(project, et)
		if err != nil {
			t.Fatalf("FindEdgesByType(%s): %v", et, err)
		}
		for _, e := range es {
			edgeKeys = append(edgeKeys, qn(e.SourceID)+"|"+et+"|"+qn(e.TargetID))
		}
	}
	sort.Strings(nodeKeys)
	sort.Strings(edgeKeys)
	return nodeKeys, edgeKeys
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// diff returns up to `max` symmetric-difference entries for a readable failure.
func diff(a, b []string) []string {
	sa := map[string]bool{}
	for _, x := range a {
		sa[x] = true
	}
	sb := map[string]bool{}
	for _, x := range b {
		sb[x] = true
	}
	var out []string
	for _, x := range a {
		if !sb[x] {
			out = append(out, "-"+x)
		}
	}
	for _, x := range b {
		if !sa[x] {
			out = append(out, "+"+x)
		}
	}
	sort.Strings(out)
	if len(out) > 25 {
		out = out[:25]
	}
	return out
}

// callExists reports whether a CALLS edge src->dst (by simple name) exists.
func callExists(t *testing.T, s *store.Store, project, srcName, dstName string) bool {
	t.Helper()
	srcs, _ := s.FindNodesByName(project, srcName)
	for _, src := range srcs {
		edges, _ := s.FindEdgesBySourceAndType(src.ID, "CALLS")
		for _, e := range edges {
			if tgt, err := s.FindNodeByID(e.TargetID); err == nil && tgt != nil && tgt.Name == dstName {
				return true
			}
		}
	}
	return false
}

// --- B1: Determinism ---------------------------------------------------------

func TestBattery_PipelineDeterminism(t *testing.T) {
	dir, cleanup := setupTestRepo(t)
	defer cleanup()

	var baseNodes, baseEdges []string
	for run := 0; run < 3; run++ {
		s, proj := indexToMemory(t, dir)
		nodes, edges := canonicalGraph(t, s, proj)
		s.Close()
		if run == 0 {
			baseNodes, baseEdges = nodes, edges
			if len(baseNodes) == 0 || len(baseEdges) == 0 {
				t.Fatalf("empty graph (nodes=%d edges=%d)", len(baseNodes), len(baseEdges))
			}
			continue
		}
		if !eq(baseNodes, nodes) {
			t.Errorf("run %d node set differs from run 0:\n  %s", run, strings.Join(diff(baseNodes, nodes), "\n  "))
		}
		if !eq(baseEdges, edges) {
			t.Errorf("run %d edge set differs from run 0:\n  %s", run, strings.Join(diff(baseEdges, edges), "\n  "))
		}
	}
}

// --- B2/metamorphic: invariances --------------------------------------------

// writeBatteryFixture writes a small Go project with a known call structure:
//
//	main -> helper -> compute   (compute is a leaf; unused is dead code)
func writeBatteryFixture(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "main.go"), `package main

func main() {
	_ = helper()
}

func helper() int {
	return compute(1, 2)
}

func compute(a, b int) int {
	return a + b
}

func unused() int {
	return 0
}
`)
}

func TestBattery_WhitespaceCommentInvariance(t *testing.T) {
	d1 := t.TempDir()
	writeBatteryFixture(t, d1)
	s1, p1 := indexToMemory(t, d1)
	defer s1.Close()
	n1, e1 := canonicalGraph(t, s1, p1)

	// Semantically identical: add comments + blank lines, reformat spacing.
	d2 := t.TempDir()
	writeFile(t, filepath.Join(d2, "main.go"), `package main

// Package comment and extra blank lines below must not change the graph.


func main() {

	// call the helper
	_ = helper()
}

func helper() int { return compute(1, 2) } // inlined brace style

func compute(a, b int) int {
	// add the two operands
	return a + b
}

func unused() int { return 0 }
`)
	s2, p2 := indexToMemory(t, d2)
	defer s2.Close()
	n2, e2 := canonicalGraph(t, s2, p2)

	if !eq(n1, n2) {
		t.Errorf("whitespace/comment change altered the node set:\n  %s", strings.Join(diff(n1, n2), "\n  "))
	}
	if !eq(e1, e2) {
		t.Errorf("whitespace/comment change altered the edge set:\n  %s", strings.Join(diff(e1, e2), "\n  "))
	}
}

// A comment-only edit must be equivalent on the incremental path too. The
// separate fresh-store metamorphic test above cannot catch a reindex that
// accidentally deletes unchanged structural nodes or edges.
func TestBattery_IncrementalCommentOnlyEquivalentToFull(t *testing.T) {
	dir := t.TempDir()
	writeBatteryFixture(t, dir)
	writeFile(t, filepath.Join(dir, "unchanged.go"), `package main

func untouched() int { return helper() }
`)
	writeFile(t, filepath.Join(dir, "nested", "index.ts"), `export function envValue(): string | undefined {
	return process.env.UNCHANGED_TOKEN;
}
`)
	s, project := indexToMemory(t, dir)
	defer s.Close()
	beforeNodes, beforeEdges := canonicalGraph(t, s, project)

	mainGo := filepath.Join(dir, "main.go")
	source, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- mainGo is a fixed child of the test-owned TempDir.
	if err := os.WriteFile(
		mainGo,
		append(source, []byte("\n// incremental comment-only probe\n")...),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	p := New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("incremental Pipeline.Run: %v", err)
	}
	if p.LastIndexDelta.Mode != "incremental" {
		t.Fatalf("index mode = %q, want incremental", p.LastIndexDelta.Mode)
	}
	afterNodes, afterEdges := canonicalGraph(t, s, project)
	if !eq(beforeNodes, afterNodes) {
		t.Errorf("comment-only incremental altered nodes:\n  %s", strings.Join(diff(beforeNodes, afterNodes), "\n  "))
	}
	if !eq(beforeEdges, afterEdges) {
		t.Errorf("comment-only incremental altered edges:\n  %s", strings.Join(diff(beforeEdges, afterEdges), "\n  "))
	}
}

func TestBattery_DeadFileInvariance(t *testing.T) {
	d1 := t.TempDir()
	writeBatteryFixture(t, d1)
	s1, p1 := indexToMemory(t, d1)
	defer s1.Close()

	// Adding a file whose function references nothing existing must not change
	// the CALLS edges among the original symbols.
	d2 := t.TempDir()
	writeBatteryFixture(t, d2)
	writeFile(t, filepath.Join(d2, "extra.go"), `package main

func standalone() int { return 42 }
`)
	s2, p2 := indexToMemory(t, d2)
	defer s2.Close()

	for _, c := range [][2]string{{"main", "helper"}, {"helper", "compute"}} {
		if got := callExists(t, s2, p2, c[0], c[1]); !callExists(t, s1, p1, c[0], c[1]) || !got {
			t.Errorf("CALLS %s->%s should hold with and without the dead file (base=%v, +deadfile=%v)",
				c[0], c[1], callExists(t, s1, p1, c[0], c[1]), got)
		}
	}
}

func TestBattery_RenameIsomorphism(t *testing.T) {
	d1 := t.TempDir()
	writeBatteryFixture(t, d1)
	s1, p1 := indexToMemory(t, d1)
	defer s1.Close()
	n1, e1 := canonicalGraph(t, s1, p1)

	// Rename compute -> calculate consistently. The graph must stay isomorphic:
	// same node/edge counts, and resolution must FOLLOW the rename
	// (helper -> calculate, and no `compute` node remains).
	d2 := t.TempDir()
	writeBatteryFixture(t, d2)
	mainGo := filepath.Join(d2, "main.go")
	b, _ := os.ReadFile(mainGo)
	if err := os.WriteFile(mainGo, []byte(strings.ReplaceAll(string(b), "compute", "calculate")), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, p2 := indexToMemory(t, d2)
	defer s2.Close()
	n2, e2 := canonicalGraph(t, s2, p2)

	if len(n1) != len(n2) {
		t.Errorf("rename changed node count: %d -> %d", len(n1), len(n2))
	}
	if len(e1) != len(e2) {
		t.Errorf("rename changed edge count: %d -> %d", len(e1), len(e2))
	}
	if !callExists(t, s2, p2, "helper", "calculate") {
		t.Error("resolution did not follow rename: helper->calculate missing")
	}
	if got, _ := s2.FindNodesByName(p2, "compute"); len(got) != 0 {
		t.Errorf("stale `compute` node survived the rename (%d found)", len(got))
	}
}

// --- B3: Mutation testing ----------------------------------------------------

func TestBattery_MutationAddCall(t *testing.T) {
	d := t.TempDir()
	writeBatteryFixture(t, d)
	s1, p1 := indexToMemory(t, d)
	if callExists(t, s1, p1, "unused", "compute") {
		t.Fatal("precondition: unused->compute should NOT exist before mutation")
	}
	s1.Close()

	// Mutation: make unused() call compute(). The CALLS edge must appear.
	mainGo := filepath.Join(d, "main.go")
	b, _ := os.ReadFile(mainGo)
	mutated := strings.Replace(string(b), "func unused() int {\n\treturn 0\n}",
		"func unused() int {\n\treturn compute(3, 4)\n}", 1)
	if mutated == string(b) {
		t.Fatal("mutation did not apply (fixture text drift)")
	}
	if err := os.WriteFile(mainGo, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, p2 := indexToMemory(t, d)
	defer s2.Close()
	if !callExists(t, s2, p2, "unused", "compute") {
		t.Error("mutation not reflected: unused->compute CALLS edge did not appear")
	}
}

func TestBattery_MutationDeleteCall(t *testing.T) {
	d := t.TempDir()
	writeBatteryFixture(t, d)
	s1, p1 := indexToMemory(t, d)
	if !callExists(t, s1, p1, "helper", "compute") {
		t.Fatal("precondition: helper->compute should exist before mutation")
	}
	s1.Close()

	// Mutation: helper no longer calls compute. The CALLS edge must disappear.
	mainGo := filepath.Join(d, "main.go")
	b, _ := os.ReadFile(mainGo)
	mutated := strings.Replace(string(b), "return compute(1, 2)", "return 0", 1)
	if mutated == string(b) {
		t.Fatal("mutation did not apply (fixture text drift)")
	}
	if err := os.WriteFile(mainGo, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, p2 := indexToMemory(t, d)
	defer s2.Close()
	if callExists(t, s2, p2, "helper", "compute") {
		t.Error("mutation not reflected: helper->compute CALLS edge survived deletion")
	}
}
