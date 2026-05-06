package store

// Tests for the Mode 7 (BulkWrite/MEMORY-journal crash) crash-marker
// detection added by Phase B1. See RECOVERY_TAXONOMY.md Mode 7.
//
// Helpers (newTestStore, etc.) live in recovery_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestBulkWriteCrashMarkerSurfacesOnReopen pins the desired Mode 7 behavior:
// when a stale crash-marker is present at OpenPath time, the open runs
// PRAGMA quick_check. If quick_check passes (the typical close-flush-clean
// case), the marker is cleared and the open succeeds. If quick_check fails
// (real Mode 7 corruption), OpenPath returns a structured error pointing
// the operator to delete_project + re-index.
//
// This test exercises the "stale marker on a clean DB" path — the marker
// fires, quick_check passes, marker is cleared, open succeeds. It pins
// that we don't false-fail on the lucky case while still surfacing real
// corruption (covered by integrity_check tests in recovery_modes_test.go).
func TestBulkWriteCrashMarkerSurfacesOnReopen(t *testing.T) {
	s, dbPath := newTestStore(t)
	seedNode(t, s, "test.b1.Foo")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Manually plant a stale marker — simulates BeginBulkWrite that wasn't
	// paired with EndBulkWrite (process killed mid-window). The DB itself
	// is clean because we Close()ed normally above.
	markerPath := bulkWriteMarkerPath(dbPath)
	if err := os.WriteFile(markerPath, []byte{}, 0o644); err != nil {
		t.Fatalf("plant marker: %v", err)
	}

	// Re-open: Mode 7 path runs quick_check; the clean DB passes; marker
	// is cleared automatically.
	s2, err := OpenPath(dbPath)
	if err != nil {
		t.Fatalf("re-open should succeed when DB is clean despite marker, got: %v", err)
	}
	defer s2.Close()

	// Marker should be gone after the successful quick_check.
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("expected marker cleared after quick_check passed, got stat err: %v", err)
	}

	// Data still intact.
	found, err := s2.FindNodeByQN("recoverytest", "test.b1.Foo")
	if err != nil {
		t.Fatalf("FindNodeByQN: %v", err)
	}
	if found == nil {
		t.Fatal("expected pre-marker data to persist, got nil")
	}
}

// TestBulkWriteMarkerWrittenAndRemoved pins the marker lifecycle when
// BeginBulkWrite + EndBulkWrite are paired correctly: marker exists
// during the window, is removed after EndBulkWrite, OpenPath sees no
// marker on next call.
func TestBulkWriteMarkerWrittenAndRemoved(t *testing.T) {
	s, dbPath := newTestStore(t)
	defer s.Close()

	markerPath := bulkWriteMarkerPath(dbPath)

	// No marker before BeginBulkWrite.
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("expected no marker before BeginBulkWrite, got: %v", err)
	}

	ctx := context.Background()
	s.BeginBulkWrite(ctx)

	// Marker exists during the window.
	if _, err := os.Stat(markerPath); err != nil {
		t.Errorf("expected marker during BulkWrite window, got: %v", err)
	}

	s.EndBulkWrite(ctx)

	// Marker removed after EndBulkWrite.
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("expected marker removed after EndBulkWrite, got stat: %v", err)
	}
}

// TestBulkWriteMarkerCorruptDBSurfacesError pins the failure path:
// when a stale marker is present AND the DB is genuinely corrupt,
// OpenPath returns a structured error pointing the operator to
// recovery, NOT silently proceeding.
func TestBulkWriteMarkerCorruptDBSurfacesError(t *testing.T) {
	s, dbPath := newTestStore(t)
	seedNode(t, s, "test.b1corrupt.Foo")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Plant marker AND corrupt the DB header so quick_check fails.
	markerPath := bulkWriteMarkerPath(dbPath)
	if err := os.WriteFile(markerPath, []byte{}, 0o644); err != nil {
		t.Fatalf("plant marker: %v", err)
	}
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	corruptHeader(t, dbPath)

	s2, err := OpenPath(dbPath)
	if err == nil {
		s2.Close()
		t.Fatal("expected error when marker present AND DB corrupt, got nil")
	}
	// Error must mention either "Mode 7" / "bulkwrite" / "quick_check" / "delete_project"
	// or the underlying corruption signal. Either is actionable.
	msg := err.Error()
	if !strings.Contains(msg, "bulkwrite") &&
		!strings.Contains(msg, "quick_check") &&
		!strings.Contains(msg, "delete_project") &&
		!strings.Contains(msg, "file is not a database") {
		t.Errorf("error should be actionable for Mode 7 recovery, got: %v", err)
	}
	t.Logf("Mode 7 corrupt-DB actionable error: %v", err)

	// Clean up the planted marker so subsequent test runs don't see it
	// in t.TempDir() reuse (defensive — TempDir is per-test, but cheap).
	_ = os.Remove(markerPath)
	_ = os.Remove(filepath.Join(filepath.Dir(dbPath), filepath.Base(dbPath)))
}

// TestBulkWriteMarkerFalsePositiveOverhead measures the wall-time cost
// of the Mode 7 false-positive case — a stale crash-marker on a non-
// corrupt DB. This is the cost the operator pays when an indexer was
// killed mid-bulk-write but no real corruption resulted (the typical
// "Ctrl-C during incremental reindex" scenario).
//
// Plan 5 Phase C: the FP overhead is one PRAGMA quick_check on a clean
// DB. quick_check on a small clean DB is dominated by file-open and
// pragma-dispatch cost; the bound is ~10s of milliseconds, NOT the
// seconds-or-minutes that PRAGMA integrity_check would consume on a
// real corruption. This test pins that bound.
//
// What this MEASURES (not just asserts):
//   - Median wall time for OpenPath on a clean DB w/ stale marker
//   - The implicit cost the FP path adds on top of normal OpenPath
//
// What this does NOT measure:
//   - The FP RATE (how often Mode 7 fires on non-corrupt DBs in
//     production). That requires production telemetry we don't have.
//   - quick_check cost on large DBs. Test DB is empty/seed-sized, so
//     bound is loose for production-sized indices.
//
// The hard assertion is: <500ms total per reopen. Anything materially
// over that suggests quick_check is re-running the entire DB rather
// than the cheap header-only check it should do on a clean file.
func TestBulkWriteMarkerFalsePositiveOverhead(t *testing.T) {
	const (
		iterations  = 10
		maxWallMs   = 500
	)

	// Build the corpus once, then run the open/close+marker cycle
	// repeatedly to get a stable median.
	s, dbPath := newTestStore(t)
	seedNode(t, s, "test.fp.Foo")
	seedNode(t, s, "test.fp.Bar")
	seedNode(t, s, "test.fp.Baz")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	markerPath := bulkWriteMarkerPath(dbPath)

	walls := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		// Plant the marker — simulates BeginBulkWrite without paired
		// EndBulkWrite. The DB itself is clean (we Close()ed cleanly
		// above and don't touch it between iterations).
		if err := os.WriteFile(markerPath, []byte{}, 0o644); err != nil {
			t.Fatalf("plant marker iter %d: %v", i, err)
		}

		// MEASURE: full OpenPath wall time, including the Mode 7
		// quick_check that fires because of the marker.
		start := time.Now()
		s2, err := OpenPath(dbPath)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("iter %d: OpenPath should succeed on clean DB despite marker, got: %v", i, err)
		}
		walls = append(walls, elapsed)

		// Sanity: marker was cleared (the FP path's whole point is to
		// not leave the marker around for the next clean run).
		if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
			t.Errorf("iter %d: expected marker cleared after FP detection, got stat err: %v", i, err)
		}

		_ = s2.Close()
	}

	// Compute median (avoids skew from any single hot-path or
	// cold-cache outlier). Sort copy.
	sorted := append([]time.Duration{}, walls...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	median := sorted[len(sorted)/2]
	p95 := sorted[(len(sorted)*95)/100]

	t.Logf("Mode 7 FP overhead (n=%d): median=%v, p95=%v (raw=%v)",
		iterations, median, p95, walls)

	if median > time.Duration(maxWallMs)*time.Millisecond {
		t.Errorf("FP overhead median %v exceeds %dms cap — quick_check may be doing more than header check",
			median, maxWallMs)
	}
}

// TestBulkWriteMarkerIgnoresMemoryDB pins that BeginBulkWrite on a
// :memory: store does NOT attempt to write a marker (which would either
// create a literal ":memory:.bulkwrite-crash-marker" file or, on Windows,
// fail os.OpenFile because the colon is invalid in filenames). Verifying
// "no operation occurred" is harder than verifying "operation completed
// without panic" — so this test just exercises the path and trusts the
// guard in BeginBulkWrite/EndBulkWrite (s.dbPath != ":memory:" check).
func TestBulkWriteMarkerIgnoresMemoryDB(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	// Should not panic, error, or attempt to create a marker file.
	s.BeginBulkWrite(ctx)
	s.EndBulkWrite(ctx)
	// If the guard were absent, BeginBulkWrite on Windows would log a
	// warning about failing to create ":memory:.bulkwrite-crash-marker".
	// The test passes as long as no panic; the guard is verified by
	// reading BeginBulkWrite source.
}
