package store

// DeleteProject cleanup contract — added 2026-06-12 after the SweRank
// pilot session hit Mode 7 retry poisoning: a SIGTERM'd indexing run
// left <db>.bulkwrite-crash-marker, delete_project removed the .db but
// NOT the marker, and the orphan marker meant the documented recovery
// procedure (delete_project + index_repository(force=true)) could still
// trip the Mode 7 check on the recreated DB. The marker had to be
// removed by hand. DeleteProject must remove every sidecar OpenPath
// inspects — the same set auto_recovery's quarantine path uses.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteProjectRemovesAllSidecarsIncludingCrashMarker(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}

	const name = "marker-cleanup-proj"
	if _, err := r.ForProject(name); err != nil {
		t.Fatalf("ForProject: %v", err)
	}

	dbPath := filepath.Join(dir, name+".db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected db at %s after ForProject: %v", dbPath, err)
	}

	// Simulate the Mode 7 shape: a crash marker left by a killed
	// BulkWrite. Create ONLY the marker — the store connection is still
	// open and SQLite mmaps the live -shm, so creating/truncating the
	// WAL/SHM sidecars from outside SIGBUSes the process (learned the
	// hard way writing this test). SQLite manages those two itself;
	// the marker is the file DeleteProject historically missed.
	if err := os.WriteFile(bulkWriteMarkerPath(dbPath), []byte{}, 0o600); err != nil {
		t.Fatalf("create crash marker: %v", err)
	}

	if err := r.DeleteProject(name); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	for _, p := range []string{
		dbPath,
		dbPath + "-wal",
		dbPath + "-shm",
		bulkWriteMarkerPath(dbPath),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s removed after DeleteProject, stat err=%v", p, err)
		}
	}
}
