package store

// Recovery test suite — see RECOVERY_TAXONOMY.md for the 7 modes covered.
//
// Helpers in this file are reused by mode-specific tests:
//   - newTestStore(t)           — opens a fresh on-disk store at t.TempDir()
//   - corruptHeader(t, dbPath)  — zeroes the first 100 bytes (SQLite header)
//   - truncateWAL(t, dbPath)    — zeroes the .db-wal file
//   - deleteShm(t, dbPath)      — removes the .db-shm file
//   - deleteMainDB(t, dbPath)   — removes the .db file (Mode 5; orphan WAL/SHM)
//
// Connection pool: OpenPath calls SetMaxOpenConns(1) (store.go:107). Helpers
// reuse OpenPath; do NOT rebuild the pool here. Closing the *Store releases
// the file lock cleanly on Windows.

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestStore opens a fresh on-disk store in t.TempDir() and returns the
// store along with the path to the underlying .db file. Caller is responsible
// for closing the store.
//
// The .db path is returned (not just the directory) so corruption helpers can
// target the file directly.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := OpenPath(dbPath)
	if err != nil {
		t.Fatalf("newTestStore: OpenPath: %v", err)
	}
	return s, dbPath
}

// corruptHeader zeroes the first 100 bytes of the .db file (the entire SQLite
// header). The store must be closed BEFORE calling this — otherwise Windows
// holds the file lock and the open will fail.
//
// SQLite header reference: https://www.sqlite.org/fileformat.html#the_database_header
func corruptHeader(t *testing.T, dbPath string) {
	t.Helper()
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("corruptHeader: open %s: %v", dbPath, err)
	}
	defer f.Close()
	zeros := make([]byte, 100)
	if _, err := f.WriteAt(zeros, 0); err != nil {
		t.Fatalf("corruptHeader: WriteAt: %v", err)
	}
}

// truncateWAL zeroes the .db-wal file (sets size to 0). Simulates a power-loss
// scenario where the WAL was lost mid-fsync. Store must be closed first.
//
// If the WAL file doesn't exist (e.g. it was checkpointed before close),
// truncateWAL is a no-op — that case still exercises Mode 1 from the perspective
// of "WAL is empty/missing" because the next open path is the same.
func truncateWAL(t *testing.T, dbPath string) {
	t.Helper()
	walPath := dbPath + "-wal"
	if _, err := os.Stat(walPath); os.IsNotExist(err) {
		return
	} else if err != nil {
		t.Fatalf("truncateWAL: stat %s: %v", walPath, err)
	}
	if err := os.Truncate(walPath, 0); err != nil {
		t.Fatalf("truncateWAL: truncate %s: %v", walPath, err)
	}
}

// deleteShm removes the .db-shm file. Simulates Mode 3 (user/cleanup deletes
// the shared-memory sidecar). Store must be closed first.
//
// If the SHM file doesn't exist, deleteShm is a no-op.
func deleteShm(t *testing.T, dbPath string) {
	t.Helper()
	shmPath := dbPath + "-shm"
	err := os.Remove(shmPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("deleteShm: remove %s: %v", shmPath, err)
	}
}

// deleteMainDB removes the .db file but leaves -wal/-shm in place if they
// exist. Simulates Mode 5 (user deletes main DB; sidecars orphaned).
func deleteMainDB(t *testing.T, dbPath string) {
	t.Helper()
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("deleteMainDB: remove %s: %v", dbPath, err)
	}
}

// seedNode inserts a single Function node into the given store under project
// "recoverytest" so post-fault re-opens have something to read. Returns the
// inserted node's ID.
//
// Used by mode-1/2/3 tests to verify that "data persists across re-open" or
// "data is correctly absent" depending on the mode.
func seedNode(t *testing.T, s *Store, qn string) int64 {
	t.Helper()
	if err := s.UpsertProject("recoverytest", "/tmp/recoverytest"); err != nil {
		t.Fatalf("seedNode: UpsertProject: %v", err)
	}
	id, err := s.UpsertNode(&Node{
		Project:       "recoverytest",
		Label:         "Function",
		Name:          "Foo",
		QualifiedName: qn,
		FilePath:      "main.go",
		StartLine:     1,
		EndLine:       10,
	})
	if err != nil {
		t.Fatalf("seedNode: UpsertNode: %v", err)
	}
	return id
}

// TestRecoveryHelpersWork is a smoke test: the helpers themselves work
// correctly on a fresh store. If this fails, all downstream recovery tests
// are unreliable.
func TestRecoveryHelpersWork(t *testing.T) {
	s, dbPath := newTestStore(t)
	id := seedNode(t, s, "test.helpers.Foo")
	if id == 0 {
		t.Fatal("seedNode returned 0")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// .db file should exist after close
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected .db file after close, got: %v", err)
	}

	// re-open should work
	s2, err := OpenPath(dbPath)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer s2.Close()

	// node should still be there
	found, err := s2.FindNodeByQN("recoverytest", "test.helpers.Foo")
	if err != nil {
		t.Fatalf("FindNodeByQN: %v", err)
	}
	if found == nil {
		t.Fatal("expected node after re-open, got nil")
	}
}
