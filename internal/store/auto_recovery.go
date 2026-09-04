package store

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/brandyn-s/code-graph/internal/config"
)

// RecoveryEvent classifies what auto-recovery did, if anything.
//
// Callers use this to decide whether to schedule a fresh re-index after
// the recovery cleared corrupt artifacts. See `OpenPathWithAutoRecovery`.
type RecoveryEvent int

const (
	// RecoveryNone means OpenPath succeeded without recovery; no action needed.
	RecoveryNone RecoveryEvent = iota
	// RecoveryCorruptHeader means Mode 4 (corrupt header) was detected and
	// the on-disk artifacts were removed. The returned Store is a fresh
	// empty DB; caller must re-index from source.
	RecoveryCorruptHeader
	// RecoveryOrphanSidecar means Mode 5 (main DB missing + orphan WAL/SHM)
	// was detected and the orphan sidecars were removed. The returned Store
	// is a fresh empty DB; caller must re-index from source.
	RecoveryOrphanSidecar
	// RecoveryBulkWriteCrash means Mode 7 (BulkWrite/MEMORY-journal crash)
	// was detected and the inconsistent DB + sidecars were removed. The
	// returned Store is a fresh empty DB; caller must re-index from source.
	RecoveryBulkWriteCrash
)

// String returns the human-readable name for the event, used in logs and
// the operator-facing slog records.
func (r RecoveryEvent) String() string {
	switch r {
	case RecoveryNone:
		return "none"
	case RecoveryCorruptHeader:
		return "corrupt_header"
	case RecoveryOrphanSidecar:
		return "orphan_sidecar"
	case RecoveryBulkWriteCrash:
		return "bulkwrite_crash"
	default:
		return "unknown"
	}
}

// AutoRecoveryEnvVar is the env var name that opt-in for auto-recovery.
// Default behavior (env var unset) preserves the existing
// "structured error → operator decides" flow.
const AutoRecoveryEnvVar = "CODE_GRAPH_AUTO_RECOVERY"

// OpenPathWithAutoRecovery wraps OpenPath with optional auto-recovery for
// the three manual-recovery modes (Mode 4 corrupt header, Mode 5 orphan
// sidecar, Mode 7 BulkWrite crash). See bench/research/2026-05-10-corruption-recovery-classification.md
// for the safety analysis underlying this behavior.
//
// Behavior:
//   - Default (CODE_GRAPH_AUTO_RECOVERY unset): identical to OpenPath. The
//     structured error propagates; the operator decides recovery.
//   - Opt-in (CODE_GRAPH_AUTO_RECOVERY=1): when OpenPath returns an
//     auto-feasible error shape, the function (a) removes the corrupt
//     on-disk artifacts (.db, .db-wal, .db-shm), (b) re-runs OpenPath on
//     the now-clean path, and (c) returns the fresh Store + RecoveryEvent
//     identifying which mode was recovered. Caller must re-index from
//     source — the returned Store is empty.
//
// Auto-recovery is logged via slog.Warn so the operator sees what
// happened. Pass the project's `name` if known (used purely for logging
// — pass "" to skip).
//
// Errors that do NOT match the three auto-feasible shapes propagate as-is
// regardless of the env var. Auto-recovery never fires for errors outside
// the documented feasibility set.
func OpenPathWithAutoRecovery(dbPath, name string) (*Store, RecoveryEvent, error) {
	s, err := OpenPath(dbPath)
	if err == nil {
		return s, RecoveryNone, nil
	}
	if config.Get(config.AutoRecovery) == "" {
		return nil, RecoveryNone, err
	}
	event := classifyAutoRecoverable(err)
	if event == RecoveryNone {
		return nil, RecoveryNone, err
	}
	slog.Warn("store.auto_recovery_triggered",
		"path", dbPath,
		"project", name,
		"event", event.String(),
		"original_error", err.Error(),
	)
	if rmErr := removeStoreArtifacts(dbPath); rmErr != nil {
		// Recovery itself failed — propagate both errors so the operator
		// sees the original cause AND the cleanup failure.
		return nil, RecoveryNone, fmt.Errorf(
			"auto-recovery cleanup failed for %s recovery: %w (original: %v)",
			event.String(), rmErr, err,
		)
	}
	// Re-open against the clean path. A fresh empty DB is the expected
	// shape; the caller will re-index from source.
	s2, openErr := OpenPath(dbPath)
	if openErr != nil {
		return nil, RecoveryNone, fmt.Errorf(
			"auto-recovery reopened path returned error: %w (after %s recovery)",
			openErr, event.String(),
		)
	}
	return s2, event, nil
}

// classifyAutoRecoverable returns the RecoveryEvent matching err, or
// RecoveryNone if err is not auto-feasible.
//
// Detection is by error-message substring match because the error shapes
// in store.go are constructed via fmt.Errorf with explicit phrasing
// rather than wrapped sentinels. Substring match keeps the classifier
// resilient to minor formatting tweaks while staying tied to the exact
// phrases the taxonomy documents.
func classifyAutoRecoverable(err error) RecoveryEvent {
	if err == nil {
		return RecoveryNone
	}
	msg := err.Error()
	switch {
	// Mode 4: corrupt header. Both ErrCorruptDatabase wrapping (if/when
	// adopted) and the existing string match are checked.
	case errors.Is(err, ErrCorruptDatabase):
		return RecoveryCorruptHeader
	case strings.Contains(msg, "file is not a database"):
		return RecoveryCorruptHeader
	// Mode 5: orphan sidecar. The exact phrase from checkOrphanSidecars.
	case strings.Contains(msg, "main DB missing but sidecar files present"):
		return RecoveryOrphanSidecar
	// Mode 7: BulkWrite crash. Two phrasings from checkAndClearBulkWriteMarker.
	case strings.Contains(msg, "Mode 7 corruption"):
		return RecoveryBulkWriteCrash
	case strings.Contains(msg, "bulkwrite-crash check"):
		return RecoveryBulkWriteCrash
	}
	return RecoveryNone
}

// removeStoreArtifacts deletes the .db + .db-wal + .db-shm + crash-marker
// files for the given DB path. Best-effort: missing files are not errors.
// Returns the first non-IsNotExist error encountered.
func removeStoreArtifacts(dbPath string) error {
	suffixes := []string{"", "-wal", "-shm", ".bulkwrite-crash-marker"}
	for _, suffix := range suffixes {
		p := dbPath + suffix
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}

// ErrCorruptDatabase is a sentinel error type for Mode 4 (corrupt header).
// Provided for callers that want errors.Is matching instead of substring.
// Currently unused upstream; reserved for future migration.
var ErrCorruptDatabase = errors.New("corrupt database")
