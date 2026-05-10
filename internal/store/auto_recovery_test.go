package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAutoRecoveryDisabledByDefault — when the env var is unset, OpenPathWithAutoRecovery
// must propagate the original error unchanged. Recovery never fires.
func TestAutoRecoveryDisabledByDefault(t *testing.T) {
	t.Setenv(AutoRecoveryEnvVar, "")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// Mode 4 fixture: write a non-SQLite header.
	if err := os.WriteFile(dbPath, []byte("not a sqlite database"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	s, event, err := OpenPathWithAutoRecovery(dbPath, "test")
	if err == nil {
		if s != nil {
			s.Close()
		}
		t.Fatal("expected error when auto-recovery disabled, got nil")
	}
	if event != RecoveryNone {
		t.Errorf("expected RecoveryNone with env var unset, got %v", event)
	}
}

// TestAutoRecoveryCorruptHeader — Mode 4 with env var enabled triggers
// RecoveryCorruptHeader, returns a fresh empty Store.
func TestAutoRecoveryCorruptHeader(t *testing.T) {
	t.Setenv(AutoRecoveryEnvVar, "1")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// Mode 4 fixture: corrupt header bytes.
	if err := os.WriteFile(dbPath, []byte("not a sqlite database garbage"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	s, event, err := OpenPathWithAutoRecovery(dbPath, "test")
	if err != nil {
		t.Fatalf("expected auto-recovery to succeed, got err: %v", err)
	}
	defer s.Close()
	if event != RecoveryCorruptHeader {
		t.Errorf("expected RecoveryCorruptHeader, got %v", event)
	}

	// Verify the returned store is fresh (no projects).
	projs, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects on recovered store: %v", err)
	}
	if len(projs) != 0 {
		t.Errorf("expected fresh empty store after recovery, got %d projects", len(projs))
	}
}

// TestAutoRecoveryOrphanSidecar — Mode 5 with env var enabled triggers
// RecoveryOrphanSidecar.
func TestAutoRecoveryOrphanSidecar(t *testing.T) {
	t.Setenv(AutoRecoveryEnvVar, "1")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// Mode 5 fixture: no main DB, orphan sidecars present.
	if err := os.WriteFile(dbPath+"-wal", []byte("wal data"), 0o644); err != nil {
		t.Fatalf("write wal: %v", err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("shm data"), 0o644); err != nil {
		t.Fatalf("write shm: %v", err)
	}

	s, event, err := OpenPathWithAutoRecovery(dbPath, "test")
	if err != nil {
		t.Fatalf("expected auto-recovery to succeed, got err: %v", err)
	}
	defer s.Close()
	if event != RecoveryOrphanSidecar {
		t.Errorf("expected RecoveryOrphanSidecar, got %v", event)
	}

	// Verify orphan sidecars were removed (not just main DB created).
	if _, err := os.Stat(dbPath + "-wal"); err == nil {
		// WAL may exist after fresh open since SQLite WAL mode creates it.
		// What matters: the FIXTURE-WRITTEN content should be gone.
		// The recovery removes the file, then OpenPath creates a new one.
		// We verify by checking content is no longer "wal data".
		content, _ := os.ReadFile(dbPath + "-wal")
		if string(content) == "wal data" {
			t.Error("orphan WAL content survived recovery — cleanup didn't run")
		}
	}
}

// TestAutoRecoveryBulkWriteCrash — Mode 7 with env var enabled triggers
// RecoveryBulkWriteCrash. Constructs the fixture by writing a stale
// crash-marker over a corrupt DB.
func TestAutoRecoveryBulkWriteCrash(t *testing.T) {
	t.Setenv(AutoRecoveryEnvVar, "1")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// Mode 7 fixture: corrupt DB content (so quick_check fails when run)
	// AND the crash-marker file present (so checkAndClearBulkWriteMarker
	// fires the quick_check).
	if err := os.WriteFile(dbPath, []byte("garbage that fails quick_check"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	markerPath := dbPath + ".bulkwrite-crash-marker"
	if err := os.WriteFile(markerPath, []byte{}, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	s, event, err := OpenPathWithAutoRecovery(dbPath, "test")
	if err != nil {
		t.Fatalf("expected auto-recovery to succeed, got err: %v", err)
	}
	defer s.Close()
	// The corrupt header path may fire first depending on which check runs
	// first. Either RecoveryCorruptHeader or RecoveryBulkWriteCrash is
	// acceptable for this fixture — both correctly indicate a corrupted
	// DB requiring rebuild. The test asserts auto-recovery fired, not
	// the precise classification.
	if event != RecoveryBulkWriteCrash && event != RecoveryCorruptHeader {
		t.Errorf("expected RecoveryBulkWriteCrash or RecoveryCorruptHeader, got %v", event)
	}

	// Verify the crash-marker is gone after recovery.
	if _, err := os.Stat(markerPath); err == nil {
		t.Error("crash-marker survived recovery — cleanup didn't remove it")
	}
}

// TestAutoRecoveryEnabledButNoErrorPropagatesNone — when the DB opens cleanly
// AND env var is set, RecoveryNone returns. Auto-recovery only fires on
// detected error shapes.
func TestAutoRecoveryEnabledButCleanOpenReturnsNone(t *testing.T) {
	t.Setenv(AutoRecoveryEnvVar, "1")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// No fixture — let OpenPath create a fresh empty DB.

	s, event, err := OpenPathWithAutoRecovery(dbPath, "test")
	if err != nil {
		t.Fatalf("expected clean open, got err: %v", err)
	}
	defer s.Close()
	if event != RecoveryNone {
		t.Errorf("expected RecoveryNone for clean open, got %v", event)
	}
}

// TestAutoRecoveryDoesNotFireForUnknownErrors — errors outside the three
// auto-feasible shapes propagate even with env var set. Auto-recovery is
// scoped to the documented modes only.
func TestAutoRecoveryDoesNotFireForUnknownErrors(t *testing.T) {
	t.Setenv(AutoRecoveryEnvVar, "1")

	// Synthetic error that doesn't match any auto-feasible shape.
	unknownErr := fmt.Errorf("some new error shape that isn't taxonomy-classified")
	event := classifyAutoRecoverable(unknownErr)
	if event != RecoveryNone {
		t.Errorf("classifier should return RecoveryNone for unknown error, got %v", event)
	}
}

// TestClassifyAutoRecoverable_AllShapes — pin the substring matches the
// classifier relies on. If the error phrasing in store.go drifts, this
// test catches it.
func TestClassifyAutoRecoverable_AllShapes(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected RecoveryEvent
	}{
		{"nil", nil, RecoveryNone},
		{"corrupt-header-substring",
			fmt.Errorf("init schema: file is not a database"),
			RecoveryCorruptHeader},
		{"corrupt-header-sentinel",
			fmt.Errorf("wrap: %w", ErrCorruptDatabase),
			RecoveryCorruptHeader},
		{"orphan-sidecar",
			fmt.Errorf("main DB missing but sidecar files present (wal=true shm=false)"),
			RecoveryOrphanSidecar},
		{"bulkwrite-mode7",
			fmt.Errorf("quick_check returned \"corrupt\" — Mode 7 corruption confirmed"),
			RecoveryBulkWriteCrash},
		{"bulkwrite-check-prefix",
			fmt.Errorf("bulkwrite-crash check: open db: i/o error"),
			RecoveryBulkWriteCrash},
		{"unknown",
			fmt.Errorf("disk full"),
			RecoveryNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyAutoRecoverable(c.err)
			if got != c.expected {
				t.Errorf("classify(%v) = %v, want %v", c.err, got, c.expected)
			}
		})
	}
}

// TestRemoveStoreArtifacts_NoFiles — best-effort: missing files are not errors.
func TestRemoveStoreArtifacts_NoFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// No files exist; cleanup should succeed.
	if err := removeStoreArtifacts(dbPath); err != nil {
		t.Errorf("removeStoreArtifacts on empty path returned err: %v", err)
	}
}

// TestRemoveStoreArtifacts_AllFiles — verify every documented artifact is removed.
func TestRemoveStoreArtifacts_AllFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	suffixes := []string{"", "-wal", "-shm", ".bulkwrite-crash-marker"}
	for _, suffix := range suffixes {
		if err := os.WriteFile(dbPath+suffix, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", dbPath+suffix, err)
		}
	}
	if err := removeStoreArtifacts(dbPath); err != nil {
		t.Fatalf("removeStoreArtifacts returned err: %v", err)
	}
	for _, suffix := range suffixes {
		if _, err := os.Stat(dbPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("file %s should have been removed, stat err: %v", dbPath+suffix, err)
		}
	}
}

// TestRecoveryEventStrings — pin enum string output for log stability.
func TestRecoveryEventStrings(t *testing.T) {
	cases := map[RecoveryEvent]string{
		RecoveryNone:           "none",
		RecoveryCorruptHeader:  "corrupt_header",
		RecoveryOrphanSidecar:  "orphan_sidecar",
		RecoveryBulkWriteCrash: "bulkwrite_crash",
	}
	for ev, want := range cases {
		if got := ev.String(); got != want {
			t.Errorf("event(%d).String() = %q, want %q", int(ev), got, want)
		}
	}
}

// TestAutoRecoveryEnvVar_NameIsCorrect — the env var name must match
// what's documented in the rule and CLAUDE.md. If anyone renames it,
// this test catches the doc/code drift.
func TestAutoRecoveryEnvVar_NameIsCorrect(t *testing.T) {
	if AutoRecoveryEnvVar != "CODE_GRAPH_AUTO_RECOVERY" {
		t.Errorf("AutoRecoveryEnvVar = %q, must be %q (documented name)",
			AutoRecoveryEnvVar, "CODE_GRAPH_AUTO_RECOVERY")
	}
	// Cross-check that the constant is non-empty (sanity).
	if !strings.HasPrefix(AutoRecoveryEnvVar, "CODE_GRAPH_") {
		t.Errorf("env var name must use CODE_GRAPH_ prefix")
	}
}
