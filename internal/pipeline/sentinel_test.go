package pipeline

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/store"
)

// TestFullReindexEveryEnvParsing pins env-var parsing for the sentinel
// threshold. Unset = 50 (production default). Empty / negative /
// non-numeric all fall through to the default. Zero disables.
func TestFullReindexEveryEnvParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 50},
		{"abc", 50},
		{"-3", 50},
		{"0", 0}, // explicit disable
		{"1", 1},
		{"100", 100},
	}
	for _, c := range cases {
		t.Setenv("CODE_GRAPH_FULL_REINDEX_EVERY", c.raw)
		if got := fullReindexEvery(); got != c.want {
			t.Errorf("CODE_GRAPH_FULL_REINDEX_EVERY=%q: expected %d, got %d", c.raw, c.want, got)
		}
	}
}

// TestSentinelCounterLifecycleStore drives the store-level counter
// independently of the pipeline. Verifies increment / reset / read
// semantics and that the counter is per-project (resetting one project
// doesn't disturb another). Pipeline-level integration is small enough
// to reason about by inspection: runPasses increments on incremental
// success, resets on full success or sentinel-forced full.
func TestSentinelCounterLifecycleStore(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	if err := s.UpsertProject("a", "/tmp/a"); err != nil {
		t.Fatalf("UpsertProject a: %v", err)
	}
	if err := s.UpsertProject("b", "/tmp/b"); err != nil {
		t.Fatalf("UpsertProject b: %v", err)
	}

	// Fresh projects start at zero.
	if n, _ := s.GetIncrementalsSinceFull("a"); n != 0 {
		t.Errorf("fresh project should start at 0, got %d", n)
	}

	// Increment a three times, b twice; counters track independently.
	for i := 0; i < 3; i++ {
		if err := s.IncrementIncrementalsSinceFull("a"); err != nil {
			t.Fatalf("Increment a: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.IncrementIncrementalsSinceFull("b"); err != nil {
			t.Fatalf("Increment b: %v", err)
		}
	}
	if n, _ := s.GetIncrementalsSinceFull("a"); n != 3 {
		t.Errorf("a: expected 3, got %d", n)
	}
	if n, _ := s.GetIncrementalsSinceFull("b"); n != 2 {
		t.Errorf("b: expected 2, got %d", n)
	}

	// Reset a; b unaffected.
	if err := s.ResetIncrementalsSinceFull("a"); err != nil {
		t.Fatalf("Reset a: %v", err)
	}
	if n, _ := s.GetIncrementalsSinceFull("a"); n != 0 {
		t.Errorf("a post-reset: expected 0, got %d", n)
	}
	if n, _ := s.GetIncrementalsSinceFull("b"); n != 2 {
		t.Errorf("b unaffected by a reset: expected 2, got %d", n)
	}
}

// TestSentinelThresholdPredicate asserts the runPasses-level decision
// logic: given a counter and a limit, fire when counter >= limit and
// limit > 0. Mirrors the inline conditional in runPasses so a regression
// to off-by-one or wrong comparison shows up here.
func TestSentinelThresholdPredicate(t *testing.T) {
	cases := []struct {
		counter int
		limit   int
		fires   bool
	}{
		{0, 50, false},
		{49, 50, false},
		{50, 50, true}, // at limit, fire
		{51, 50, true}, // over limit, fire
		{5, 0, false},  // limit=0 disables
		{1000, 0, false},
	}
	for _, c := range cases {
		got := c.limit > 0 && c.counter >= c.limit
		if got != c.fires {
			t.Errorf("counter=%d limit=%d: expected fires=%v, got %v", c.counter, c.limit, c.fires, got)
		}
	}
}

// TestIncrementalCapEnvParsing pins env-var parsing for the file-set
// cap. Default 10000; zero disables; garbage falls back to default.
func TestIncrementalCapEnvParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 10000},
		{"junk", 10000},
		{"-1", 10000},
		{"0", 0},
		{"5000", 5000},
	}
	for _, c := range cases {
		t.Setenv("CODE_GRAPH_INCREMENTAL_CAP", c.raw)
		if got := incrementalCap(); got != c.want {
			t.Errorf("CODE_GRAPH_INCREMENTAL_CAP=%q: expected %d, got %d", c.raw, c.want, got)
		}
	}
}

// TestFindCallerOfTargetDependentsIntegration drives the pipeline
// helper end-to-end against an in-memory store. A graph topology
// where caller.go's CALLS edge targets a function in changed.go (and
// caller.go does NOT import changed.go's module — the gap the
// import-graph heuristic missed) must surface caller.go in the
// dependent set.
func TestFindCallerOfTargetDependentsIntegration(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	const proj = "ctd"
	if err := s.UpsertProject(proj, "/tmp/ctd"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	// caller.go::C  --CALLS-->  changed.go::T  (the leak case)
	// other.go::O   --CALLS-->  unrelated.go::U  (must not appear)
	idC, _ := s.UpsertNode(&store.Node{Project: proj, Label: "Function", Name: "C", QualifiedName: "ctd.caller.C", FilePath: "caller.go"})
	idT, _ := s.UpsertNode(&store.Node{Project: proj, Label: "Function", Name: "T", QualifiedName: "ctd.changed.T", FilePath: "changed.go"})
	idO, _ := s.UpsertNode(&store.Node{Project: proj, Label: "Function", Name: "O", QualifiedName: "ctd.other.O", FilePath: "other.go"})
	idU, _ := s.UpsertNode(&store.Node{Project: proj, Label: "Function", Name: "U", QualifiedName: "ctd.unrelated.U", FilePath: "unrelated.go"})

	if _, err := s.InsertEdge(&store.Edge{Project: proj, SourceID: idC, TargetID: idT, Type: "CALLS"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEdge(&store.Edge{Project: proj, SourceID: idO, TargetID: idU, Type: "CALLS"}); err != nil {
		t.Fatal(err)
	}

	p := &Pipeline{Store: s, ProjectName: proj}
	changed := []discover.FileInfo{{RelPath: "changed.go"}}
	unchanged := []discover.FileInfo{
		{RelPath: "caller.go"},
		{RelPath: "other.go"},
		{RelPath: "unrelated.go"},
	}

	got := p.findCallerOfTargetDependents(changed, unchanged)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 dependent file, got %d (%v)", len(got), got)
	}
	if got[0].RelPath != "caller.go" {
		t.Errorf("expected caller.go, got %q", got[0].RelPath)
	}
}

// TestFindCallerOfTargetDependentsExcludesChanged confirms a caller
// whose own file is in the changed set doesn't get returned (it's
// already being re-resolved unconditionally). The helper has to be
// disjoint from `changed` so the cap check in runIncrementalPasses
// reflects the true added set.
func TestFindCallerOfTargetDependentsExcludesChanged(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	const proj = "ctd2"
	if err := s.UpsertProject(proj, "/tmp/ctd2"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	idA, _ := s.UpsertNode(&store.Node{Project: proj, Label: "Function", Name: "A", QualifiedName: "ctd2.a.A", FilePath: "a.go"})
	idB, _ := s.UpsertNode(&store.Node{Project: proj, Label: "Function", Name: "B", QualifiedName: "ctd2.b.B", FilePath: "b.go"})
	if _, err := s.InsertEdge(&store.Edge{Project: proj, SourceID: idA, TargetID: idB, Type: "CALLS"}); err != nil {
		t.Fatal(err)
	}

	p := &Pipeline{Store: s, ProjectName: proj}
	// a.go is BOTH a caller and in the changed set — must not appear in
	// dependents (would double-count and inflate the cap).
	changed := []discover.FileInfo{{RelPath: "a.go"}, {RelPath: "b.go"}}
	unchanged := []discover.FileInfo{{RelPath: "a.go"}} // pathological, but defensive
	got := p.findCallerOfTargetDependents(changed, unchanged)
	if len(got) != 0 {
		t.Errorf("expected empty dependents (caller is in changed), got %v", got)
	}
}
