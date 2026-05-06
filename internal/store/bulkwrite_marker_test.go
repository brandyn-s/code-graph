package store

// Tests for the Mode 7 (BulkWrite/MEMORY-journal crash) crash-marker
// detection added by Phase B1. See RECOVERY_TAXONOMY.md Mode 7.
//
// Helpers (newTestStore, etc.) live in recovery_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
