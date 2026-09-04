package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// FormatVersion is the on-disk index format this build reads and writes. It is
// stored in SQLite's user_version pragma. Bump it whenever a schema or
// semantic change makes databases written by this build unreadable by the
// previous release, and document the bump in docs/index-format.md.
const FormatVersion = 1

// MinSupportedFormatVersion is the oldest format this build still opens.
// Databases below it must be rebuilt with index_repository.
const MinSupportedFormatVersion = 1

// legacyUnstampedFormat is what pre-versioning databases report: SQLite's
// default user_version. A database with tables but user_version 0 was written
// by a release before FormatVersion existed and is treated as format 1.
const legacyUnstampedFormat = 0

// ErrIndexFormatTooNew is returned when a database was written by a newer
// code-graph than the one opening it.
var ErrIndexFormatTooNew = errors.New("index format is newer than this code-graph build")

// ErrIndexFormatUnsupported is returned when a database's format is older than
// this build can read; the fix is to rebuild the index.
var ErrIndexFormatUnsupported = errors.New("index format is no longer supported")

// checkFormatVersion reads user_version and decides whether this build may
// open the database. It returns the version the database should be stamped
// with after schema initialization (FormatVersion for fresh and legacy
// databases) or an error that names the remedy.
func checkFormatVersion(ctx context.Context, db *sql.DB, dbPath string) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read index format version: %w", err)
	}
	switch {
	case version == legacyUnstampedFormat:
		// Fresh database or pre-versioning legacy database; both are format 1.
		return FormatVersion, nil
	case version > FormatVersion:
		return 0, fmt.Errorf(
			"%w: %s was built by a newer code-graph (format %d, this build reads %d); upgrade code-graph or delete the project and re-index",
			ErrIndexFormatTooNew, dbPath, version, FormatVersion,
		)
	case version < MinSupportedFormatVersion:
		return 0, fmt.Errorf(
			"%w: %s has format %d, this build supports %d through %d; rebuild the index with index_repository (or delete_project then re-index)",
			ErrIndexFormatUnsupported, dbPath, version, MinSupportedFormatVersion, FormatVersion,
		)
	default:
		return version, nil
	}
}

// stampFormatVersion records the format version in user_version. It is called
// after initSchema so a fresh database is never left unstamped.
func stampFormatVersion(ctx context.Context, db *sql.DB, version int) error {
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("stamp index format version: %w", err)
	}
	return nil
}

// FormatVersionOf reports the format version recorded in this store's database.
func (s *Store) FormatVersionOf() (int, error) {
	var version int
	if err := s.db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read index format version: %w", err)
	}
	return version, nil
}
