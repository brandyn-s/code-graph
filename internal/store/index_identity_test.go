package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brandyn-s/code-graph/internal/indexidentity"
)

func createPreIdentityDatabase(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE projects (
			name TEXT PRIMARY KEY,
			indexed_at TEXT NOT NULL,
			root_path TEXT NOT NULL
		);
		INSERT INTO projects (name, indexed_at, root_path)
		VALUES ('legacy', '2026-01-02T03:04:05Z', '/work/legacy');
	`); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	return dbPath
}

func TestOpenPathMigratesLegacyDatabaseWithoutSynthesizingIdentity(t *testing.T) {
	dbPath := createPreIdentityDatabase(t)
	st, err := OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath legacy database: %v", err)
	}
	defer st.Close()

	project, err := st.GetProject("legacy")
	if err != nil {
		t.Fatalf("GetProject after migration: %v", err)
	}
	if project.RootPath != "/work/legacy" || project.IndexedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("legacy project metadata changed during migration: %+v", project)
	}
	record, err := st.GetIndexIdentity("legacy")
	if err != nil {
		t.Fatalf("GetIndexIdentity after migration: %v", err)
	}
	if record.Status != indexidentity.StatusMissing || record.Identity != nil {
		t.Errorf("legacy identity = %+v, want explicit missing with no envelope", record)
	}

	var tableCount int
	if err := st.DB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='index_identity'`).Scan(&tableCount); err != nil {
		t.Fatalf("query migrated schema: %v", err)
	}
	if tableCount != 1 {
		t.Errorf("index_identity table count = %d, want 1", tableCount)
	}
}

func TestGetIndexIdentityRejectsIncompleteCapturedRow(t *testing.T) {
	st, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer st.Close()
	if err := st.UpsertProject("test", "/work/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if _, err := st.DB().ExecContext(t.Context(), `
		INSERT INTO index_identity (
			project, schema_version, repository_id, checkout_id, source_revision,
			dirty_fingerprint, index_generation, captured_at, identity_status, identity_reason
		) VALUES ('test', 1, '', '', '', '', '', '', 'captured', '')`); err != nil {
		t.Fatalf("insert incomplete captured row: %v", err)
	}

	record, err := st.GetIndexIdentity("test")
	if err != nil {
		t.Fatalf("GetIndexIdentity: %v", err)
	}
	if record.Status != indexidentity.StatusError {
		t.Errorf("incomplete row status = %q, want %q", record.Status, indexidentity.StatusError)
	}
	if record.Identity != nil {
		t.Errorf("incomplete row exposed identity: %+v", record.Identity)
	}
	if !strings.Contains(record.Reason, "incomplete") {
		t.Errorf("incomplete row reason = %q, want actionable incomplete-state reason", record.Reason)
	}
}

func TestGetIndexIdentityRejectsInconsistentGeneration(t *testing.T) {
	st, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer st.Close()
	if err := st.UpsertProject("test", "/work/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	identity := &indexidentity.Envelope{
		SchemaVersion:    indexidentity.SchemaVersion,
		RepositoryID:     strings.Repeat("a", 64),
		CheckoutID:       strings.Repeat("b", 64),
		SourceRevision:   strings.Repeat("c", 40),
		DirtyFingerprint: "clean",
		IndexGeneration:  strings.Repeat("d", 64),
		CapturedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if err := st.SetIndexIdentity("test", identity); err != nil {
		t.Fatalf("SetIndexIdentity: %v", err)
	}

	record, err := st.GetIndexIdentity("test")
	if err != nil {
		t.Fatalf("GetIndexIdentity: %v", err)
	}
	if record.Status != indexidentity.StatusError {
		t.Errorf("inconsistent generation status = %q, want %q", record.Status, indexidentity.StatusError)
	}
	if record.Identity != nil {
		t.Errorf("inconsistent generation exposed identity: %+v", record.Identity)
	}
	if !strings.Contains(record.Reason, "index_generation") {
		t.Errorf("inconsistent generation reason = %q, want index_generation remediation", record.Reason)
	}
}
