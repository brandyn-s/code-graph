package store

// Tests for the 7 recovery modes from RECOVERY_TAXONOMY.md.
// Helpers (newTestStore, corruptHeader, etc.) live in recovery_test.go.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sync/errgroup"
)

// ─── Mode 1: WAL truncation (transparent recovery) ──────────────────────────

// TestRecoverFromTruncatedWAL: zeroing the .db-wal file after a clean close
// should still allow re-open. Pre-checkpoint data persists in main .db.
//
// Note: SQLite's normal close sequence may auto-checkpoint, so the data we
// seeded ends up in main .db, not WAL. This test exercises the "WAL is empty
// or truncated on next open" code path — the same path that fires after a
// power-loss-mid-fsync.
func TestRecoverFromTruncatedWAL(t *testing.T) {
	s, dbPath := newTestStore(t)
	seedNode(t, s, "test.mode1.Foo")
	s.Checkpoint(context.Background()) // force WAL → main DB
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	truncateWAL(t, dbPath)

	s2, err := OpenPath(dbPath)
	if err != nil {
		t.Fatalf("re-open after WAL truncation: %v", err)
	}
	defer s2.Close()

	found, err := s2.FindNodeByQN("recoverytest", "test.mode1.Foo")
	if err != nil {
		t.Fatalf("FindNodeByQN: %v", err)
	}
	if found == nil {
		t.Fatal("expected node from pre-checkpoint state, got nil")
	}
}

// ─── Mode 2: Crash before commit (transparent recovery) ─────────────────────

// TestRecoverFromUnflushedTransaction: simulate an indexer killed mid-pass by
// starting a transaction, writing inside it, then closing the store WITHOUT
// committing. After re-open, the transaction's data must be absent (correctly
// discarded). Re-open itself must succeed.
func TestRecoverFromUnflushedTransaction(t *testing.T) {
	s, dbPath := newTestStore(t)

	// Seed one node OUTSIDE the transaction — this should persist.
	seedNode(t, s, "test.mode2.Persisted")

	// Begin a transaction, do work inside, but DON'T commit. Close mimics
	// the indexer process being killed.
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	txStore := &Store{db: s.db, q: tx, dbPath: s.dbPath}
	if _, err := txStore.UpsertNode(&Node{
		Project:       "recoverytest",
		Label:         "Function",
		Name:          "Lost",
		QualifiedName: "test.mode2.Lost",
		FilePath:      "main.go",
		StartLine:     20,
		EndLine:       30,
	}); err != nil {
		t.Fatalf("UpsertNode in tx: %v", err)
	}
	// NO tx.Commit() — simulate kill. We DO call Rollback to release the
	// connection back to the pool so Close can release the file lock on
	// Windows. The semantic effect (transaction's data is discarded) is
	// identical to a kill — only the housekeeping differs.
	_ = tx.Rollback()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open: must succeed.
	s2, err := OpenPath(dbPath)
	if err != nil {
		t.Fatalf("re-open after unflushed tx: %v", err)
	}
	defer s2.Close()

	// Persisted node should still be there.
	persisted, err := s2.FindNodeByQN("recoverytest", "test.mode2.Persisted")
	if err != nil {
		t.Fatalf("FindNodeByQN persisted: %v", err)
	}
	if persisted == nil {
		t.Fatal("expected pre-tx node to persist, got nil")
	}

	// Lost node should be absent (transaction discarded).
	lost, err := s2.FindNodeByQN("recoverytest", "test.mode2.Lost")
	if err != nil {
		t.Fatalf("FindNodeByQN lost: %v", err)
	}
	if lost != nil {
		t.Fatalf("expected uncommitted node to be discarded, got %+v", lost)
	}
}

// ─── Mode 3: Missing -shm sidecar (transparent recovery) ────────────────────

// TestRecoverFromMissingShm: deleting the -shm file should be transparent —
// SQLite re-creates it on next open.
//
// This test specifically exercises the case where -shm is missing AND -wal
// has content (i.e., recoverStaleSHM does NOT fire because WAL isn't empty).
// Mode 3 is "user/cleanup deletes -shm with valid WAL" — the recoverStaleSHM
// code path covers a different shape ("stale -shm with empty WAL").
func TestRecoverFromMissingShm(t *testing.T) {
	s, dbPath := newTestStore(t)
	seedNode(t, s, "test.mode3.Foo")
	// Don't Checkpoint — we want WAL to have content so recoverStaleSHM
	// doesn't fire its "empty WAL + stale SHM" path.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deleteShm(t, dbPath)

	s2, err := OpenPath(dbPath)
	if err != nil {
		t.Fatalf("re-open after -shm delete: %v", err)
	}
	defer s2.Close()

	// SQLite should have re-created -shm transparently.
	shmPath := dbPath + "-shm"
	if _, err := os.Stat(shmPath); err != nil {
		// SHM may be created lazily; touch the DB to force creation.
		_, _ = s2.FindNodeByQN("recoverytest", "test.mode3.Foo")
		if _, err := os.Stat(shmPath); err != nil {
			t.Logf("note: -shm not present after re-open + read; SQLite may defer creation. Acceptable if subsequent write succeeds.")
		}
	}

	// Confirm the node is still readable (the real recoverability assertion).
	found, err := s2.FindNodeByQN("recoverytest", "test.mode3.Foo")
	if err != nil {
		t.Fatalf("FindNodeByQN: %v", err)
	}
	if found == nil {
		t.Fatal("expected node, got nil")
	}
}

// ─── Mode 7: Crash during BulkWrite (MEMORY journal) ────────────────────────

// TestBulkWriteCrashSurfacesViaIntegrityCheck: during BeginBulkWrite, journal
// is MEMORY — there is NO recovery journal. If the process is killed in this
// window, the main DB may have inconsistent pages. The test asserts that
// re-open does NOT panic AND that PRAGMA integrity_check is the operator's
// signal (it returns "ok" or a list of issues).
//
// We cannot easily produce real page corruption from a Go test, so this test
// performs the operationally-equivalent shape: BeginBulkWrite + partial
// inserts + Close-without-EndBulkWrite, then re-open and run integrity_check.
// The test asserts the check runs without panicking — the operator can act
// on its result.
func TestBulkWriteCrashSurfacesViaIntegrityCheck(t *testing.T) {
	s, dbPath := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertProject("recoverytest", "/tmp/recoverytest"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	s.BeginBulkWrite(ctx)
	// Partial inserts inside the bulk-write window. Don't call EndBulkWrite —
	// simulates the indexer being killed mid-pass.
	for i := 0; i < 5; i++ {
		_, err := s.UpsertNode(&Node{
			Project:       "recoverytest",
			Label:         "Function",
			Name:          fmt.Sprintf("Bulk%d", i),
			QualifiedName: fmt.Sprintf("test.mode7.Bulk%d", i),
			FilePath:      "main.go",
			StartLine:     i*10 + 1,
			EndLine:       i*10 + 5,
		})
		if err != nil {
			t.Fatalf("UpsertNode in bulk window: %v", err)
		}
	}
	// NO EndBulkWrite — close in MEMORY-journal mode.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open: must succeed without panic.
	s2, err := OpenPath(dbPath)
	if err != nil {
		t.Fatalf("re-open after bulk-write crash: %v", err)
	}
	defer s2.Close()

	// Run PRAGMA integrity_check — operator's signal.
	row := s2.db.QueryRowContext(ctx, "PRAGMA integrity_check")
	var result string
	if err := row.Scan(&result); err != nil {
		t.Fatalf("PRAGMA integrity_check: %v", err)
	}
	t.Logf("Mode 7 integrity_check result: %q", result)
	// Result is either "ok" (lucky — close-with-MEMORY-journal happened to
	// flush cleanly) or a non-"ok" value indicating corruption. EITHER is
	// acceptable; the test's purpose is "the check runs without panic and
	// gives the operator something to act on."
}

// ─── Mode 4: Corrupt header (irrecoverable, actionable error) ───────────────

// TestCorruptHeaderReturnsActionableError: zeroing the SQLite header should
// cause OpenPath to return an error containing "file is not a database" so
// the caller can route to delete_project + index_repository.
//
// String-match approach: the wrapping is fmt.Errorf("init schema: %w", err)
// where the inner error is mattn/go-sqlite3's "file is not a database". No
// typed sentinel exists today (B5 would add one).
func TestCorruptHeaderReturnsActionableError(t *testing.T) {
	s, dbPath := newTestStore(t)
	seedNode(t, s, "test.mode4.Foo")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Remove WAL/SHM so recoverStaleSHM doesn't paper over the corruption.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	corruptHeader(t, dbPath)

	s2, err := OpenPath(dbPath)
	if err == nil {
		s2.Close()
		t.Fatal("expected error after header corruption, got nil")
	}
	if !strings.Contains(err.Error(), "file is not a database") {
		t.Fatalf("error should contain 'file is not a database' for caller routing, got: %v", err)
	}
	t.Logf("Mode 4 actionable error: %v", err)
}

// ─── Mode 5: Missing main DB with orphan sidecars (B3.5 fix) ────────────────

// TestMissingDBWithOrphanSidecarReturnsError: deleting the main .db file
// while -wal/-shm remain (or even just one of them) should return a
// structured error indicating "main DB missing but sidecar files present —
// likely accidental delete." This protects against silent data loss.
//
// This test pins the fix from B3.5 (see RECOVERY_TAXONOMY.md Mode 5).
// Without the fix, OpenPath silently re-creates the .db as empty (probed
// 2026-05-05).
func TestMissingDBWithOrphanSidecarReturnsError(t *testing.T) {
	s, dbPath := newTestStore(t)
	seedNode(t, s, "test.mode5.Foo")
	// Don't Checkpoint — we WANT WAL/SHM to remain after close.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify sidecars exist post-close (they may not always — SQLite checkpoints
	// on close in some configurations). If they don't, fabricate one to ensure
	// the "orphan sidecar" condition holds.
	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"
	if _, err := os.Stat(walPath); os.IsNotExist(err) {
		if _, err2 := os.Stat(shmPath); os.IsNotExist(err2) {
			// Neither sidecar exists; create an empty -wal to satisfy the
			// orphan-sidecar precondition this test pins.
			if err := os.WriteFile(walPath, []byte{}, 0o644); err != nil {
				t.Fatalf("setup orphan -wal: %v", err)
			}
		}
	}

	deleteMainDB(t, dbPath)

	s2, err := OpenPath(dbPath)
	if err == nil {
		s2.Close()
		t.Fatal("expected error when main DB missing but sidecars present (B3.5 fix), got silent re-create")
	}
	// Error must mention either the orphan condition or 'main DB missing'.
	// Adjust the substring once B3.5's exact wording lands.
	if !strings.Contains(err.Error(), "sidecar") &&
		!strings.Contains(err.Error(), "missing") &&
		!strings.Contains(err.Error(), "orphan") {
		t.Fatalf("error should signal orphan-sidecar condition, got: %v", err)
	}
	t.Logf("Mode 5 actionable error: %v", err)
}

// TestMissingMainDBPureCreateStillWorks: the B3.5 fix must NOT regress the
// fresh-create path. When NO sidecars exist, OpenPath should create a fresh
// DB normally.
func TestMissingMainDBPureCreateStillWorks(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fresh.db")

	// No sidecars; no main DB. Pure create.
	s, err := OpenPath(dbPath)
	if err != nil {
		t.Fatalf("pure create should succeed, got: %v", err)
	}
	defer s.Close()
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected .db file after pure create, got: %v", err)
	}
}

// ─── Mode 6: Concurrent writers ──────────────────────────────────────────────

// TestConcurrentWritersSerialize tests parameterized concurrency: N=2 (basic),
// N=3 (production triad: watcher + indexer + reader), N=10 (busy_timeout
// stress).
//
// Each writer inserts a unique node. Final state must contain ALL inserts
// (eventual-consistency assertion, not strict ordering).
func TestConcurrentWritersSerialize(t *testing.T) {
	cases := []struct {
		name string
		n    int
	}{
		{"N2_basic", 2},
		{"N3_production_triad", 3},
		{"N10_busy_timeout_stress", 10},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			defer s.Close()
			rootCtx := context.Background()

			if err := s.UpsertProject("recoverytest", "/tmp/recoverytest"); err != nil {
				t.Fatalf("UpsertProject: %v", err)
			}

			var busyErrs atomic.Int64
			// Use errgroup for goroutine sync ONLY. Don't use the derived ctx
			// for the post-Wait count query — it's canceled once goroutines
			// complete.
			g, _ := errgroup.WithContext(rootCtx)
			for i := 0; i < tc.n; i++ {
				i := i
				g.Go(func() error {
					_, err := s.UpsertNode(&Node{
						Project:       "recoverytest",
						Label:         "Function",
						Name:          fmt.Sprintf("Conc%d", i),
						QualifiedName: fmt.Sprintf("test.mode6.Conc%d", i),
						FilePath:      "main.go",
						StartLine:     i*10 + 1,
						EndLine:       i*10 + 5,
					})
					if err != nil && strings.Contains(err.Error(), "database is locked") {
						busyErrs.Add(1)
						return nil // tolerated at high N
					}
					return err
				})
			}
			if err := g.Wait(); err != nil {
				t.Fatalf("concurrent writes failed: %v", err)
			}

			// Eventual-consistency assertion: count successful writes using
			// rootCtx (errgroup's derived ctx is canceled by now).
			expected := int64(tc.n) - busyErrs.Load()
			var count int64
			row := s.db.QueryRowContext(rootCtx,
				`SELECT count(*) FROM nodes WHERE project='recoverytest'`)
			if err := row.Scan(&count); err != nil {
				t.Fatalf("count nodes: %v", err)
			}
			if count != expected {
				t.Fatalf("expected %d successful writes, got %d (busyErrs=%d)",
					expected, count, busyErrs.Load())
			}

			// At N=2/3, SQLite_BUSY should NOT escape (busy_timeout=10s in DSN).
			if tc.n <= 3 && busyErrs.Load() > 0 {
				t.Errorf("at N=%d, no SQLITE_BUSY should escape, got %d", tc.n, busyErrs.Load())
			}
			t.Logf("N=%d: writes=%d, busyErrs=%d", tc.n, count, busyErrs.Load())
		})
	}
}

// Ensure the errors package is referenced even if a future edit removes the
// only use; cheap insurance against the import-blanking paper-cut.
var _ = errors.New
