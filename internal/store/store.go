package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Querier abstracts *sql.DB and *sql.Tx so store methods work in both contexts.
// Both variants support the Context-accepting counterparts; we expose them so
// callers inside a WithTransaction block can honor caller cancellation without
// reaching past the tx (which would ask the single-connection pool for a
// second connection and deadlock on the write lock).
type Querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	Prepare(query string) (*sql.Stmt, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// Store wraps a SQLite connection for graph storage.
type Store struct {
	db     *sql.DB
	q      Querier // active querier: db or tx
	dbPath string
}

// Node represents a graph node stored in SQLite.
type Node struct {
	ID            int64
	Project       string
	Label         string
	Name          string
	QualifiedName string
	FilePath      string
	StartLine     int
	EndLine       int
	Properties    map[string]any
}

// Edge represents a graph edge stored in SQLite.
type Edge struct {
	ID         int64
	Project    string
	SourceID   int64
	TargetID   int64
	Type       string
	Properties map[string]any
}

// cacheDir returns the default cache directory for databases.
func cacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".cache", "codebase-memory-mcp")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir cache: %w", err)
	}
	return dir, nil
}

// Open opens or creates a SQLite database for the given project in the default cache dir.
func Open(project string) (*Store, error) {
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, project+".db")
	return OpenPath(dbPath)
}

// OpenInDir opens or creates a SQLite database for the given project in a specific directory.
func OpenInDir(dir, project string) (*Store, error) {
	dbPath := filepath.Join(dir, project+".db")
	return OpenPath(dbPath)
}

// OpenPath opens a SQLite database at the given path.
//
// If the main .db file is missing but sidecar files (-wal or -shm) exist, this
// returns a structured error instead of silently re-creating an empty DB.
// The orphan-sidecar condition usually means the operator deleted the .db
// accidentally; silent re-create would be data loss with no signal.
// Recovery: call DeleteProject (or remove the sidecars manually) and re-index.
//
// If neither main DB nor sidecars exist, this is a normal fresh-create.
func OpenPath(dbPath string) (*Store, error) {
	if err := checkOrphanSidecars(dbPath); err != nil {
		return nil, err
	}

	// Recover from stale SHM left by SIGKILL: if WAL is empty/missing but SHM exists,
	// the SHM has stale lock state that can deadlock new connections. Safe to remove.
	recoverStaleSHM(dbPath)

	// Mode 7 detection (BulkWrite/MEMORY-journal crash, see RECOVERY_TAXONOMY.md):
	// if a stale crash-marker file is present, the previous BeginBulkWrite was
	// not paired with a clean EndBulkWrite. The DB may have inconsistent pages
	// because MEMORY journal mode means there's no on-disk journal to replay.
	// Run PRAGMA quick_check before serving; clear marker on success, surface
	// structured error pointing to delete_project on failure.
	if err := checkAndClearBulkWriteMarker(dbPath); err != nil {
		return nil, err
	}

	dsn := dbPath + "?_journal_mode=WAL" +
		"&_busy_timeout=10000" +
		"&_foreign_keys=1" +
		"&_synchronous=NORMAL" +
		"&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// Single connection: SQLite is single-writer, pool adds lock contention.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// PRAGMAs not supported in mattn DSN — set via Exec after Open.
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, "PRAGMA temp_store = MEMORY")
	_, _ = db.ExecContext(ctx, "PRAGMA mmap_size = 67108864") // 64 MB

	// Adaptive cache: 10% of DB file size, clamped to 2-64 MB.
	cacheMB := adaptiveCacheMB(dbPath)
	cacheKB := cacheMB * 1024
	_, _ = db.ExecContext(ctx, fmt.Sprintf("PRAGMA cache_size = -%d", cacheKB))

	s := &Store{db: db, dbPath: dbPath}
	s.q = s.db
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	slog.Debug("store.open", "path", dbPath, "cache_mb", cacheMB)
	return s, nil
}

// checkOrphanSidecars returns a structured error if the main .db file is
// missing but sidecar files (.db-wal or .db-shm) exist.
//
// This guards against the silent-data-loss scenario where an operator deletes
// the main DB file (intentionally or not) and leaves sidecars behind. Without
// this check, the next OpenPath call would silently create a fresh empty DB
// with no signal that prior data was lost.
//
// If neither main DB nor sidecars exist, returns nil — that's a normal
// fresh-create case and OpenPath should proceed.
//
// See RECOVERY_TAXONOMY.md Mode 5.
func checkOrphanSidecars(dbPath string) error {
	if _, err := os.Stat(dbPath); err == nil {
		return nil // main DB exists, no orphan check needed
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat db: %w", err)
	}

	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"
	walExists := false
	shmExists := false
	if _, err := os.Stat(walPath); err == nil {
		walExists = true
	}
	if _, err := os.Stat(shmPath); err == nil {
		shmExists = true
	}
	if walExists || shmExists {
		return fmt.Errorf("main DB missing but sidecar files present (wal=%t shm=%t) — likely accidental delete; run delete_project to clean up or restore from backup", walExists, shmExists)
	}
	return nil
}

// bulkWriteMarkerPath returns the marker filename for the given DB path.
// The marker exists only during a BeginBulkWrite ... EndBulkWrite window.
// Its persistence past the window indicates an unclean shutdown during
// MEMORY-journal-mode writes (Mode 7 in RECOVERY_TAXONOMY.md).
func bulkWriteMarkerPath(dbPath string) string {
	return dbPath + ".bulkwrite-crash-marker"
}

// writeBulkWriteMarker creates the marker file. Called by BeginBulkWrite.
//
// Best-effort: marker creation failure is logged but does not block the
// bulk-write operation. The cost of a missed marker is "operator runs
// PRAGMA integrity_check manually if anything looks off"; the cost of
// blocking the write is "indexer fails entirely on a transient FS hiccup."
// Trade-off favors continuing.
func writeBulkWriteMarker(dbPath string) {
	markerPath := bulkWriteMarkerPath(dbPath)
	f, err := os.OpenFile(markerPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("store.bulkwrite_marker_create_failed", "path", markerPath, "err", err)
		return
	}
	_ = f.Close()
}

// removeBulkWriteMarker deletes the marker. Called by EndBulkWrite.
// Best-effort: removal failure means a stale marker will fire on next
// open and force a quick_check. That's safe — false-positive cost is
// one extra PRAGMA quick_check vs the false-negative cost of missing
// real corruption.
func removeBulkWriteMarker(dbPath string) {
	_ = os.Remove(bulkWriteMarkerPath(dbPath))
}

// checkAndClearBulkWriteMarker is called from OpenPath. If the marker is
// present, run PRAGMA quick_check; if quick_check passes, clear the
// marker and proceed. If quick_check fails, return a structured error
// directing the operator to delete_project + re-index.
//
// Conservative-by-design: any error from quick_check OR any unexpected
// quick_check output other than "ok" is treated as failure. False positives
// (quick_check fails on a clean DB) cost one re-index; false negatives
// (real corruption goes undetected) cost silent-wrong-state — much worse.
func checkAndClearBulkWriteMarker(dbPath string) error {
	markerPath := bulkWriteMarkerPath(dbPath)
	if _, err := os.Stat(markerPath); err != nil {
		return nil // no marker = no crash window in flight
	}

	slog.Warn("store.bulkwrite_marker_present", "path", markerPath,
		"action", "running PRAGMA quick_check")

	// Open the DB without our usual setup just to run quick_check.
	dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return fmt.Errorf("bulkwrite-crash check: open db: %w", err)
	}
	defer db.Close()

	row := db.QueryRowContext(context.Background(), "PRAGMA quick_check")
	var result string
	if err := row.Scan(&result); err != nil {
		return fmt.Errorf("bulkwrite-crash check: quick_check failed (%w) — likely Mode 7 corruption; run delete_project + index_repository(force=true) to recover", err)
	}
	if result != "ok" {
		return fmt.Errorf("bulkwrite-crash check: quick_check returned %q (expected \"ok\") — Mode 7 corruption confirmed; run delete_project + index_repository(force=true) to recover", result)
	}

	// quick_check passed — DB is consistent despite the crash. Clear marker.
	removeBulkWriteMarker(dbPath)
	slog.Info("store.bulkwrite_marker_cleared", "path", markerPath,
		"reason", "quick_check passed — DB consistent")
	return nil
}

// recoverStaleSHM removes stale SHM files left by unclean shutdowns (SIGKILL).
// If the WAL file is empty or missing but the SHM file exists, the SHM contains
// stale lock state that can deadlock new connections. Removing it lets SQLite
// rebuild clean shared memory on next open.
func recoverStaleSHM(dbPath string) {
	shmPath := dbPath + "-shm"
	walPath := dbPath + "-wal"

	shmInfo, shmErr := os.Stat(shmPath)
	if shmErr != nil {
		return // no SHM file — nothing to recover
	}

	walInfo, walErr := os.Stat(walPath)
	walEmpty := walErr != nil || walInfo.Size() == 0

	if walEmpty && shmInfo.Size() > 0 {
		slog.Info("store.recover_shm", "path", dbPath, "shm_bytes", shmInfo.Size())
		_ = os.Remove(shmPath)
		// Also remove empty WAL to let SQLite start fresh
		if walErr == nil {
			_ = os.Remove(walPath)
		}
	}
}

// adaptiveCacheMB returns a cache size in MB proportional to the DB file size.
// 10% of DB size, clamped to [2, 64] MB. Returns 2 for missing/small files.
func adaptiveCacheMB(dbPath string) int {
	fi, err := os.Stat(dbPath)
	if err != nil {
		return 2
	}
	dbSizeMB := int(fi.Size() / (1 << 20))
	cacheMB := dbSizeMB / 10
	if cacheMB < 2 {
		return 2
	}
	if cacheMB > 64 {
		return 64
	}
	return cacheMB
}

// OpenMemory opens an in-memory SQLite database (for testing).
func OpenMemory() (*Store, error) {
	dsn := ":memory:?_foreign_keys=1" +
		"&_synchronous=OFF"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open memory db: %w", err)
	}
	_, _ = db.ExecContext(context.Background(), "PRAGMA temp_store = MEMORY")
	s := &Store{db: db, dbPath: ":memory:"}
	s.q = s.db
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return s, nil
}

// WithTransaction executes fn within a single SQLite transaction.
// The callback receives a transaction-scoped Store — all store methods called on
// txStore use the transaction. The receiver's q field is never mutated, so
// concurrent read-only handlers (using s.q == s.db) are unaffected.
func (s *Store) WithTransaction(ctx context.Context, fn func(txStore *Store) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	txStore := &Store{db: s.db, q: tx, dbPath: s.dbPath}
	if err := fn(txStore); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Checkpoint forces a WAL checkpoint, moving pages from WAL to the main DB,
// then runs PRAGMA optimize so the query planner has up-to-date statistics.
// PRAGMA optimize (SQLite 3.46+) auto-limits sampling per index, only re-analyzing
// stale stats. Cost is absorbed during indexing rather than the first read query.
func (s *Store) Checkpoint(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	_, _ = s.db.ExecContext(ctx, "PRAGMA optimize")
}

// WALSize returns the current WAL file size in bytes, or -1 if unavailable.
// Useful for diagnosing memory bloat from un-checkpointed WAL files.
func (s *Store) WALSize() int64 {
	walPath := s.dbPath + "-wal"
	fi, err := os.Stat(walPath)
	if err != nil {
		return -1
	}
	return fi.Size()
}

// BeginBulkWrite switches to MEMORY journal mode for faster bulk writes.
// Also boosts cache to 64 MB for write throughput.
// Call EndBulkWrite when done to restore WAL mode and adaptive cache.
//
// Writes a Mode 7 crash-marker (RECOVERY_TAXONOMY.md) before switching
// journal mode. EndBulkWrite removes the marker. If the process is killed
// inside this window, the marker survives and the next OpenPath will
// run PRAGMA quick_check to detect MEMORY-journal-corruption that the
// missing on-disk journal can't recover.
func (s *Store) BeginBulkWrite(ctx context.Context) {
	if s.dbPath != "" && s.dbPath != ":memory:" {
		writeBulkWriteMarker(s.dbPath)
	}
	_, _ = s.db.ExecContext(ctx, "PRAGMA journal_mode = MEMORY")
	_, _ = s.db.ExecContext(ctx, "PRAGMA synchronous = OFF")
	_, _ = s.db.ExecContext(ctx, "PRAGMA cache_size = -65536") // 64 MB
}

// EndBulkWrite restores WAL journal mode, NORMAL synchronous, and adaptive cache.
//
// Removes the Mode 7 crash-marker. Order matters: switch back to WAL
// (which forces a checkpoint) BEFORE removing the marker, so a crash
// between mode-switch and marker-removal still leaves a valid signal.
func (s *Store) EndBulkWrite(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, "PRAGMA synchronous = NORMAL")
	_, _ = s.db.ExecContext(ctx, "PRAGMA journal_mode = WAL")
	s.restoreDefaultCache(ctx)
	if s.dbPath != "" && s.dbPath != ":memory:" {
		removeBulkWriteMarker(s.dbPath)
	}
}

// WithLargeCache temporarily boosts the page cache to 64 MB for heavy read operations
// (e.g. GetSchema, Louvain clustering), then restores the adaptive default.
func (s *Store) WithLargeCache(ctx context.Context, fn func() error) error {
	_, _ = s.db.ExecContext(ctx, "PRAGMA cache_size = -65536") // 64 MB
	defer s.restoreDefaultCache(ctx)
	return fn()
}

// restoreDefaultCache recalculates and applies the adaptive cache size from DB file size.
func (s *Store) restoreDefaultCache(ctx context.Context) {
	cacheMB := adaptiveCacheMB(s.dbPath)
	cacheKB := cacheMB * 1024
	_, _ = s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA cache_size = -%d", cacheKB))
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying sql.DB (for advanced queries).
//
// Prefer Q() for query execution inside pipeline passes: Q() returns the
// active querier (either *sql.DB or *sql.Tx), which honors the surrounding
// transaction. Using DB() from code running under WithTransaction bypasses
// the tx and asks the single-connection pool for another connection, which
// blocks indefinitely on the write lock held by the outer tx.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Q returns the active Querier. Inside WithTransaction, this is the tx; at
// other times, it is the raw *sql.DB. Passes should use Q for ad-hoc queries
// so they participate in whatever transaction their caller set up.
func (s *Store) Q() Querier {
	return s.q
}

// DBPath returns the filesystem path to the SQLite database.
func (s *Store) DBPath() string {
	return s.dbPath
}

func (s *Store) initSchema() error {
	ctx := context.Background()
	schema := `
	CREATE TABLE IF NOT EXISTS projects (
		name TEXT PRIMARY KEY,
		indexed_at TEXT NOT NULL,
		root_path TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS file_hashes (
		project TEXT NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
		rel_path TEXT NOT NULL,
		sha256 TEXT NOT NULL,
		mtime_ns INTEGER NOT NULL DEFAULT 0,
		size INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (project, rel_path)
	);

	CREATE TABLE IF NOT EXISTS nodes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project TEXT NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
		label TEXT NOT NULL,
		name TEXT NOT NULL,
		qualified_name TEXT NOT NULL,
		file_path TEXT DEFAULT '',
		start_line INTEGER DEFAULT 0,
		end_line INTEGER DEFAULT 0,
		properties TEXT DEFAULT '{}',
		UNIQUE(project, qualified_name)
	);

	CREATE INDEX IF NOT EXISTS idx_nodes_label ON nodes(project, label);
	CREATE INDEX IF NOT EXISTS idx_nodes_name ON nodes(project, name);
	CREATE INDEX IF NOT EXISTS idx_nodes_file ON nodes(project, file_path);

	CREATE TABLE IF NOT EXISTS edges (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project TEXT NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
		source_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
		target_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
		type TEXT NOT NULL,
		properties TEXT DEFAULT '{}',
		UNIQUE(source_id, target_id, type)
	);

	CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source_id, type);
	CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target_id, type);
	CREATE INDEX IF NOT EXISTS idx_edges_type ON edges(project, type);

	CREATE INDEX IF NOT EXISTS idx_edges_target_type ON edges(project, target_id, type);
	CREATE INDEX IF NOT EXISTS idx_edges_source_type ON edges(project, source_id, type);
	`
	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		return err
	}

	// Migration: project_summaries table for ADR storage.
	_, _ = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS project_summaries (
			project TEXT PRIMARY KEY,
			summary TEXT NOT NULL,
			source_hash TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`)

	// Migration: add url_path generated column to edges table.
	// Generated columns require SQLite 3.31.0+ (mattn/go-sqlite3 supports this).
	// We check if the column already exists to make this idempotent.
	var colCount int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('edges') WHERE name='url_path_gen'`).Scan(&colCount)
	if colCount == 0 {
		_, err = s.db.ExecContext(ctx, `ALTER TABLE edges ADD COLUMN url_path_gen TEXT GENERATED ALWAYS AS (json_extract(properties, '$.url_path'))`)
		if err != nil {
			// If generated columns aren't supported, skip gracefully
			slog.Warn("schema.url_path_gen.skip", "err", err)
		}
	}

	// Index on generated column (safe to CREATE IF NOT EXISTS)
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_edges_url_path ON edges(project, url_path_gen)`)

	// Migration: add confidence_tier_gen generated column to edges. Reads
	// the "confidence_tier" key out of properties JSON and falls back to
	// EXTRACTED when absent, so existing rows (and passes that haven't
	// been updated to set confidence_tier) are treated as source-proven
	// by default.
	//
	// Lets Cypher queries filter via `WHERE r.confidence_tier = 'INFERRED'`
	// without scanning the JSON blob on every row.
	//
	// The key is `confidence_tier` (not `confidence`) to avoid colliding
	// with the pre-existing numeric `confidence` score used by
	// configlink_strategies.go.
	var confCol int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('edges') WHERE name='confidence_tier_gen'`).Scan(&confCol)
	if confCol == 0 {
		_, err = s.db.ExecContext(ctx, `ALTER TABLE edges ADD COLUMN confidence_tier_gen TEXT GENERATED ALWAYS AS (COALESCE(json_extract(properties, '$.confidence_tier'), 'EXTRACTED'))`)
		if err != nil {
			slog.Warn("schema.confidence_tier_gen.skip", "err", err)
		}
	}
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_edges_confidence_tier ON edges(project, confidence_tier_gen)`)

	// Migration: add caller_node_kind generated column to edges. Reads
	// the "caller_node_kind" key out of properties JSON (populated by
	// the resolver — see internal/pipeline/caller_kind.go for the
	// decision rules). NULL on pre-migration rows; new rows always
	// populate. Lets bench/accuracy/compare.py stratify precision by
	// caller scope (function-body, method-body, file-block, etc.)
	// without scanning the JSON blob.
	//
	// Index on (project, caller_node_kind_gen) keeps per-kind precision
	// queries cheap — typical access pattern is "how many CALLS edges
	// in project X have caller_node_kind = 'file-block'?".
	var ckCol int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('edges') WHERE name='caller_node_kind_gen'`).Scan(&ckCol)
	if ckCol == 0 {
		_, err = s.db.ExecContext(ctx, `ALTER TABLE edges ADD COLUMN caller_node_kind_gen TEXT GENERATED ALWAYS AS (json_extract(properties, '$.caller_node_kind'))`)
		if err != nil {
			slog.Warn("schema.caller_node_kind_gen.skip", "err", err)
		}
	}
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_edges_caller_node_kind ON edges(project, caller_node_kind_gen)`)

	// Migration: add resolver_rule generated column to edges. Reads the
	// "resolver_rule" key out of properties JSON (populated by the
	// resolver — see internal/pipeline/resolver_rule.go for the
	// taxonomy and emit-site mapping). NULL on pre-migration rows; new
	// rows always populate when the edge is a CALLS-family emission.
	// Lets bench/accuracy/compare.py stratify precision by resolver
	// pathway (cross-package-import-map, cross-package-suffix,
	// fuzzy-resolve, etc.) without scanning the JSON blob.
	//
	// Index on (project, resolver_rule_gen) keeps per-rule precision
	// queries cheap — typical access pattern is "how many CALLS edges
	// in project X have resolver_rule = 'cross-package-suffix'?".
	// Step 4 of the 2026-05-02 plateau-2 plan; sub-bucket split landed
	// 2026-05-06 (precision varied 10x across fixtures within the
	// originally lumped cross-package-heuristic bucket).
	var rrCol int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('edges') WHERE name='resolver_rule_gen'`).Scan(&rrCol)
	if rrCol == 0 {
		_, err = s.db.ExecContext(ctx, `ALTER TABLE edges ADD COLUMN resolver_rule_gen TEXT GENERATED ALWAYS AS (json_extract(properties, '$.resolver_rule'))`)
		if err != nil {
			slog.Warn("schema.resolver_rule_gen.skip", "err", err)
		}
	}
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_edges_resolver_rule ON edges(project, resolver_rule_gen)`)

	// Migration: add candidate_set_size_gen generated column to edges. Reads
	// the "candidate_set_size" key out of properties JSON (populated by the
	// resolver — see internal/pipeline/candidate_set.go for the rationale
	// and emit-site mapping). NULL on pre-migration rows; new rows always
	// populate when the edge is a CALLS-family emission.
	//
	// Lets bench/accuracy/compare.py stratify precision by call-site
	// ambiguity (Janusian ambiguous vs unambiguous) without scanning the
	// JSON blob. Index on (project, candidate_set_size_gen) keeps the
	// per-bucket count queries cheap — typical access pattern is
	// "how many CALLS edges in project X have candidate_set_size >= 2?".
	// Step 5 of the 2026-05-02 plateau-2 plan.
	var csCol int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('edges') WHERE name='candidate_set_size_gen'`).Scan(&csCol)
	if csCol == 0 {
		_, err = s.db.ExecContext(ctx, `ALTER TABLE edges ADD COLUMN candidate_set_size_gen INTEGER GENERATED ALWAYS AS (json_extract(properties, '$.candidate_set_size'))`)
		if err != nil {
			slog.Warn("schema.candidate_set_size_gen.skip", "err", err)
		}
	}
	_, _ = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_edges_candidate_set_size ON edges(project, candidate_set_size_gen)`)

	// Migration: add mtime_ns and size columns to file_hashes for stat pre-filtering.
	var mtimeCol int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('file_hashes') WHERE name='mtime_ns'`).Scan(&mtimeCol)
	if mtimeCol == 0 {
		_, _ = s.db.ExecContext(ctx, `ALTER TABLE file_hashes ADD COLUMN mtime_ns INTEGER NOT NULL DEFAULT 0`)
		_, _ = s.db.ExecContext(ctx, `ALTER TABLE file_hashes ADD COLUMN size INTEGER NOT NULL DEFAULT 0`)
	}

	// Migration: add enrichment_version column to projects for tracking enrichment pass versions.
	var evCol int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('projects') WHERE name='enrichment_version'`).Scan(&evCol)
	if evCol == 0 {
		_, _ = s.db.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN enrichment_version TEXT NOT NULL DEFAULT ''`)
	}

	// Migration: add index progress columns for live progress reporting.
	var ipCol int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('projects') WHERE name='index_phase'`).Scan(&ipCol)
	if ipCol == 0 {
		_, _ = s.db.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN index_phase TEXT NOT NULL DEFAULT ''`)
		_, _ = s.db.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN index_pct INTEGER NOT NULL DEFAULT 0`)
		_, _ = s.db.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN index_detail TEXT NOT NULL DEFAULT ''`)
	}

	// Migration: incrementals_since_full counter for the
	// periodic-full-reindex sentinel. Bounds the lifetime of
	// silently-stale edges that the incremental dependency-discovery
	// heuristic (findDependentFiles) can miss. Reset to 0 on every full
	// reindex, bumped by 1 on every successful incremental. The pipeline
	// forces a full reindex when the counter exceeds
	// CODE_GRAPH_FULL_REINDEX_EVERY (default 50, env-configurable).
	var isfCol int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('projects') WHERE name='incrementals_since_full'`).Scan(&isfCol)
	if isfCol == 0 {
		_, _ = s.db.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN incrementals_since_full INTEGER NOT NULL DEFAULT 0`)
	}

	// Migration: node_embeddings table for Voyage AI semantic search.
	_, _ = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS node_embeddings (
			node_id INTEGER PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
			model TEXT NOT NULL,
			embedding BLOB NOT NULL
		)`)

	return nil
}

// marshalProps serializes properties to JSON.
func marshalProps(props map[string]any) string {
	if props == nil {
		return "{}"
	}
	b, err := json.Marshal(props)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// UnmarshalProps deserializes JSON properties. Exported for use by cypher executor.
func UnmarshalProps(data string) map[string]any {
	return unmarshalProps(data)
}

// unmarshalProps deserializes JSON properties.
func unmarshalProps(data string) map[string]any {
	if data == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return map[string]any{}
	}
	return m
}

// Now returns the current time in ISO 8601 format.
func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
