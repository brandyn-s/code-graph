package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// buildSCIPTestWorld creates a temp repo with two Go-ish source files, a
// store with matching Function nodes and heuristic CALLS edges, and a
// synthetic SCIP index covering ONE of the files. Layout (1-based lines):
//
//	covered.go:
//	  1: package m
//	  2: func Caller() {
//	  3:     Callee()      <- call-shaped reference
//	  4:     f := Callee   <- value reference (NOT call-shaped)
//	  5: }
//	  6: func Callee() {
//	  7: }
//	uncovered.go:
//	  1: package m
//	  2: func Other() {
//	  3:     Callee()
//	  4: }
//
// The store starts with two heuristic edges: Callee->Caller (WRONG, both
// endpoints in the covered file — the kind of fuzzy false positive SCIP
// replaces) and Other->Callee (caller in the uncovered file — must
// survive untouched).
func buildSCIPTestWorld(t *testing.T) (p *Pipeline, s *store.Store, ids map[string]int64, indexPath string) {
	t.Helper()
	repo := t.TempDir()

	covered := "package m\nfunc Caller() {\n\tCallee()\n\tf := Callee\n}\nfunc Callee() {\n}\n"
	uncovered := "package m\nfunc Other() {\n\tCallee()\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "covered.go"), []byte(covered), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "uncovered.go"), []byte(uncovered), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	p = New(context.Background(), s, repo, discover.ModeFull)
	if err := s.UpsertProject(p.ProjectName, repo); err != nil {
		t.Fatal(err)
	}

	ids = map[string]int64{}
	for _, n := range []struct {
		name, file string
		start, end int
	}{
		{"Caller", "covered.go", 2, 5},
		{"Callee", "covered.go", 6, 7},
		{"Other", "uncovered.go", 2, 4},
	} {
		id, err := s.UpsertNode(&store.Node{
			Project: p.ProjectName, Label: "Function", Name: n.name,
			QualifiedName: p.ProjectName + "." + n.name,
			FilePath:      n.file, StartLine: n.start, EndLine: n.end,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[n.name] = id
	}

	// Heuristic edges: one wrong edge with both endpoints covered, one
	// good edge whose caller is uncovered.
	for _, e := range []*store.Edge{
		{Project: p.ProjectName, SourceID: ids["Callee"], TargetID: ids["Caller"], Type: "CALLS"},
		{Project: p.ProjectName, SourceID: ids["Other"], TargetID: ids["Callee"], Type: "CALLS"},
	} {
		if _, err := s.InsertEdge(e); err != nil {
			t.Fatal(err)
		}
	}

	// Synthetic SCIP index covering covered.go only. SCIP lines are
	// 0-based: Caller defined at line 1, Callee at line 5, the call at
	// line 2 cols 1-7, the value reference at line 3 cols 6-12.
	calleeSym := "scip-go gomod m 1.0 `m`/Callee()."
	callerSym := "scip-go gomod m 1.0 `m`/Caller()."
	idx := &scip.Index{
		Metadata: &scip.Metadata{ProjectRoot: "file://" + repo},
		Documents: []*scip.Document{{
			RelativePath: "covered.go",
			Occurrences: []*scip.Occurrence{
				{Range: []int32{1, 5, 11}, Symbol: callerSym, SymbolRoles: int32(scip.SymbolRole_Definition)},
				{Range: []int32{2, 1, 7}, Symbol: calleeSym},  // Callee() — call
				{Range: []int32{3, 6, 12}, Symbol: calleeSym}, // f := Callee — value ref
				{Range: []int32{5, 5, 11}, Symbol: calleeSym, SymbolRoles: int32(scip.SymbolRole_Definition)},
			},
		}},
	}
	raw, err := proto.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	indexPath = filepath.Join(repo, "index.scip")
	if err := os.WriteFile(indexPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, s, ids, indexPath
}

func callEdges(t *testing.T, s *store.Store, project string) map[[2]int64]map[string]any {
	t.Helper()
	rows, err := s.Q().Query(`SELECT source_id, target_id, properties FROM edges WHERE project = ? AND type = 'CALLS'`, project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[[2]int64]map[string]any{}
	for rows.Next() {
		var src, tgt int64
		var props string
		if err := rows.Scan(&src, &tgt, &props); err != nil {
			t.Fatal(err)
		}
		out[[2]int64{src, tgt}] = store.UnmarshalProps(props)
	}
	return out
}

// The ingest pass replaces heuristic CALLS edges in SCIP-covered files
// with occurrence-derived ones, leaves uncovered files alone, and does not
// emit edges for non-call references (function values).
func TestSCIPIngestReplacesCoveredHeuristicEdges(t *testing.T) {
	p, s, ids, indexPath := buildSCIPTestWorld(t)

	if err := p.runSCIPIngest(indexPath); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	edges := callEdges(t, s, p.ProjectName)

	// The wrong heuristic edge (both endpoints covered) is gone.
	if _, ok := edges[[2]int64{ids["Callee"], ids["Caller"]}]; ok {
		t.Error("heuristic edge between SCIP-covered files survived replacement")
	}
	// The derived edge exists and is marked as SCIP-resolved.
	props, ok := edges[[2]int64{ids["Caller"], ids["Callee"]}]
	if !ok {
		t.Fatalf("derived Caller->Callee edge missing; edges=%v", edges)
	}
	if props["resolver_rule"] != "scip-ingest" {
		t.Errorf("derived edge resolver_rule = %v, want scip-ingest", props["resolver_rule"])
	}
	// The heuristic edge whose caller is uncovered survives (fallback layer).
	if _, ok := edges[[2]int64{ids["Other"], ids["Callee"]}]; !ok {
		t.Error("heuristic edge from uncovered caller was deleted")
	}
	// The value reference (f := Callee) must NOT have produced a
	// Callee-as-callee edge from the value-ref line... the call on line 3
	// already created Caller->Callee; assert the edge COUNT is exactly 2
	// so the value ref did not add anything beyond it.
	if len(edges) != 2 {
		t.Errorf("got %d CALLS edges, want 2 (%v)", len(edges), edges)
	}
}

// Without the env var the pass is inert.
func TestSCIPIngestInertWithoutEnv(t *testing.T) {
	p, s, ids, _ := buildSCIPTestWorld(t)
	t.Setenv(scipIndexPathEnv, "")

	p.passSCIPIngest()

	edges := callEdges(t, s, p.ProjectName)
	if len(edges) != 2 {
		t.Fatalf("pass mutated edges while disabled: %v", edges)
	}
	if _, ok := edges[[2]int64{ids["Callee"], ids["Caller"]}]; !ok {
		t.Error("heuristic edges should be untouched when pass is disabled")
	}
}

// A missing/corrupt index degrades to a warning — indexing must not fail.
func TestSCIPIngestBadIndexIsNonFatal(t *testing.T) {
	p, s, _, _ := buildSCIPTestWorld(t)
	t.Setenv(scipIndexPathEnv, "/nonexistent/index.scip")

	p.passSCIPIngest() // must not panic; logs a warning

	if got := len(callEdges(t, s, p.ProjectName)); got != 2 {
		t.Fatalf("edges changed after failed ingest: %d", got)
	}
}

// Edges from covered callers into files the index does NOT cover (CGO
// sources, platform-gated files) must survive replacement — ground-truth
// eval showed source-file-only deletion cost 210 real edges (the index
// can never re-derive them). Deletion requires BOTH endpoints covered.
func TestSCIPIngestKeepsEdgesIntoUncoveredFiles(t *testing.T) {
	p, s, ids, indexPath := buildSCIPTestWorld(t)
	// Heuristic edge from the covered file INTO the uncovered file: the
	// index has no document for uncovered.go, so this must survive.
	if _, err := s.InsertEdge(&store.Edge{
		Project: p.ProjectName, SourceID: ids["Caller"], TargetID: ids["Other"], Type: "CALLS",
		Properties: map[string]any{"marker": "into-uncovered"},
	}); err != nil && !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatal(err)
	}

	if err := p.runSCIPIngest(indexPath); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	edges := callEdges(t, s, p.ProjectName)
	if _, ok := edges[[2]int64{ids["Caller"], ids["Other"]}]; !ok {
		t.Error("edge from covered caller into uncovered callee file was deleted; CGO-style callees must keep heuristic edges")
	}
}

// A document whose definition sites no longer match the current node spans
// (stale index after the file changed) is excluded from both deletion and
// derivation — its heuristic edges stay authoritative.
func TestSCIPIngestSkipsDriftedFiles(t *testing.T) {
	p, s, ids, _ := buildSCIPTestWorld(t)

	calleeSym := "scip-go gomod m 1.0 `m`/Callee()."
	callerSym := "scip-go gomod m 1.0 `m`/Caller()."
	// Same document, but every definition is shifted far off the real
	// spans — as if covered.go was edited after indexing.
	idx := &scip.Index{
		Metadata: &scip.Metadata{ProjectRoot: "file:///drifted"},
		Documents: []*scip.Document{{
			RelativePath: "covered.go",
			Occurrences: []*scip.Occurrence{
				{Range: []int32{40, 5, 11}, Symbol: callerSym, SymbolRoles: int32(scip.SymbolRole_Definition)},
				{Range: []int32{42, 1, 7}, Symbol: calleeSym},
				{Range: []int32{50, 5, 11}, Symbol: calleeSym, SymbolRoles: int32(scip.SymbolRole_Definition)},
			},
		}},
	}
	raw, err := proto.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(t.TempDir(), "stale.scip")
	if err := os.WriteFile(stalePath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.runSCIPIngest(stalePath); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	edges := callEdges(t, s, p.ProjectName)
	// Both pre-existing heuristic edges survive: the only covered document
	// is drifted, so nothing is deleted and nothing is derived.
	if len(edges) != 2 {
		t.Fatalf("drifted index changed edges: got %d, want 2 (%v)", len(edges), edges)
	}
	if _, ok := edges[[2]int64{ids["Callee"], ids["Caller"]}]; !ok {
		t.Error("heuristic edge deleted despite drifted document")
	}
}
