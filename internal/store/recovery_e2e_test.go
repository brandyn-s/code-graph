package store

// End-to-end recovery test (Phase C1).
// Index a fixture → corrupt the DB → call DeleteProject → re-index →
// assert final state matches a clean baseline.

import (
	"context"
	"fmt"
	"os"
	"testing"
)

// TestEndToEndCorruptionRecovery walks the full operator-side recovery flow
// for Mode 4 (corrupt header):
//
//  1. Open a project via the router and seed N nodes (the "index" step).
//  2. Close all router state.
//  3. Corrupt the .db header (simulate disk corruption / bit-flip).
//  4. Verify the next ForProject call fails with the actionable error.
//  5. Call DeleteProject to clean up the corrupt DB + sidecars.
//  6. Re-open via ForProject (creates fresh) and re-seed N nodes.
//  7. Assert final node count matches the clean-baseline count.
//
// This pins the recovery procedure documented in CLAUDE.md "Recovery
// procedures" — if the procedure changes (e.g., DeleteProject's API or
// ForProject's open semantics), this test is the canary.
func TestEndToEndCorruptionRecovery(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	defer r.CloseAll()

	const project = "e2e-recovery"
	const fixtureSize = 5

	// (1) Index: seed nodes via the router.
	indexFixture := func(label string) {
		s, err := r.ForProject(project)
		if err != nil {
			t.Fatalf("ForProject (%s): %v", label, err)
		}
		if err := s.UpsertProject(project, "/tmp/"+project); err != nil {
			t.Fatalf("UpsertProject (%s): %v", label, err)
		}
		for i := 0; i < fixtureSize; i++ {
			_, err := s.UpsertNode(&Node{
				Project:       project,
				Label:         "Function",
				Name:          fmt.Sprintf("Fn%d", i),
				QualifiedName: fmt.Sprintf("e2e.%s.Fn%d", label, i),
				FilePath:      "main.go",
				StartLine:     i*10 + 1,
				EndLine:       i*10 + 5,
			})
			if err != nil {
				t.Fatalf("UpsertNode (%s) #%d: %v", label, i, err)
			}
		}
	}
	countNodes := func() int64 {
		s, err := r.ForProject(project)
		if err != nil {
			t.Fatalf("ForProject (count): %v", err)
		}
		var n int64
		row := s.db.QueryRowContext(context.Background(),
			"SELECT count(*) FROM nodes WHERE project = ?", project)
		if err := row.Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	indexFixture("initial")
	baseline := countNodes()
	if baseline != fixtureSize {
		t.Fatalf("baseline count = %d, want %d", baseline, fixtureSize)
	}

	// (2) Close all router state so we can mutate the .db file.
	r.CloseAll()

	// (3) Corrupt the header.
	dbPath := r.Dir() + string(os.PathSeparator) + project + ".db"
	// Remove WAL/SHM so recoverStaleSHM doesn't paper over the corruption.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	corruptHeader(t, dbPath)

	// (4) Re-open the router (fresh state) and verify ForProject surfaces
	// the actionable error.
	r2, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatalf("NewRouterWithDir (post-corrupt): %v", err)
	}
	defer r2.CloseAll()

	_, err = r2.ForProject(project)
	if err == nil {
		t.Fatal("expected ForProject to fail on corrupt DB, got nil")
	}
	t.Logf("Post-corruption ForProject error: %v", err)

	// (5) Run the recovery: DeleteProject.
	if err := r2.DeleteProject(project); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	// (6) Re-index (fresh project; ForProject creates new DB).
	indexFixture("recovered")

	// (7) Final state matches baseline.
	final := countNodes()
	if final != fixtureSize {
		t.Fatalf("post-recovery count = %d, want %d (baseline = %d)",
			final, fixtureSize, baseline)
	}
	t.Logf("End-to-end recovery: baseline=%d, post-recovery=%d ✓", baseline, final)
}
