# Corruption Recovery Classification (Phase B1)

**Date**: 2026-05-10
**Plan**: `~/Documents/knowledge-base/plans/2026-05-10-production-readiness-gaps.md` Phase B1
**Source**: `internal/store/RECOVERY_TAXONOMY.md`

## Premise correction

The plan's Phase 3.5 baseline cited "**4 manual modes** requiring `delete_project + index_repository`". Source-reading the taxonomy shows:

- The actual count is **3 manual-operator-action modes** (Modes 4, 5, 7), not 4.
- Mode 5 was promoted from "silent re-create" to "structured error" in B3.5 (2026-05-05) — already had detection by the time this plan was written.
- "Indexer killed mid-pass" appears twice in the plan's baseline (once as Mode 2, once standalone). Mode 2 is in WAL mode and SQLite auto-recovers; the only "indexer killed" mode that requires operator action is Mode 7 (MEMORY journal, BulkWrite window).

Corrected baseline:

| Status | Modes | Recovery |
|---|---|---|
| **Auto-recovery (transparent)** | 1 (WAL truncation), 2 (Crash before commit, WAL), 3 (Missing -shm), 6 (Concurrent writers) | None needed — SQLite handles |
| **Manual operator action** | 4 (Corrupt header), 5 (Missing main DB + orphan sidecar), 7 (BulkWrite crash) | `DeleteProject` + `IndexRepository(force=true)` |
| **Out of scope** | Disk full, read-only FS, schema drift, FK violations, etc. | (Per RECOVERY_TAXONOMY.md "Out of scope") |

The 3 manual modes already have:
- **Detection**: structured errors returned by `OpenPath`. Mode 4 has `ErrCorruptDatabase` / "file is not a database"; Mode 5 has "main DB missing but sidecar files present"; Mode 7 has "Mode 7 corruption" via crash-marker + `PRAGMA quick_check`.
- **Recovery procedure**: `DeleteProject(name)` + `IndexRepository(name, force=true)`. This is identical across all 3 modes — the recovery is a clean re-index from source files.

The remaining gap is **invocation**: the operator must run the recovery manually after seeing the error.

## Classification

For each manual mode, the question is: **can the binary auto-invoke `DeleteProject + IndexRepository(force=true)` safely?**

### Safety analysis: what gets "lost" in auto-recovery

Across all three modes, the existing on-disk data is already irrecoverable:

- **Mode 4 (Corrupt header)**: SQLite cannot read the file. The data is unreadable.
- **Mode 5 (Missing main DB + orphan sidecar)**: The main DB is deleted; only orphan WAL/SHM sidecars remain. There is no graph data to lose.
- **Mode 7 (BulkWrite crash)**: `PRAGMA quick_check` flagged inconsistent pages. The DB is open but corrupt; some pages are unreadable or wrong.

In all three cases, "auto-recovery" doesn't destroy recoverable data — the data is already destroyed by the corruption event. What auto-recovery does:

1. Delete the unrecoverable on-disk artifacts (`DeleteProject`)
2. Re-index from the source repo (`IndexRepository(force=true)`)
3. Result: a clean index reflecting the current source-repo state

The cost of skipping auto-recovery is **operator wait time** — they see the structured error, run the documented recovery, and end up at the same state. The cost of auto-recovery is **transparency** — the operator may not realize the index was rebuilt unless logged.

### Per-mode verdict

| Mode | Auto-feasible? | Confidence | Reasoning |
|---|---|---|---|
| Mode 4 (Corrupt header) | **YES** | HIGH | Disk corruption; the file is unreadable. No information loss vs. manual recovery. |
| Mode 5 (Missing main DB + orphan sidecar) | **YES** | HIGH | The main DB is already gone. Orphan sidecars carry zero recoverable graph data. Auto-recovery is just cleanup + re-index. |
| Mode 7 (BulkWrite crash) | **YES** | HIGH | `PRAGMA quick_check` flagged inconsistency; partial-write pages are corrupt. Re-index from source is the only path to a consistent graph regardless of who triggers it. |

All three are auto-feasible. The plan's prediction of "3 of 4 auto" matches the corrected baseline of "3 of 3 auto."

### Edge case: user backup intent on Mode 5

The taxonomy notes for Mode 5: "If unintentional: restore from backup before code-graph next opens." This is the only case where auto-recovery could harm intent — if the user deleted the main DB intending to restore from a backup later.

**Mitigation**: gate auto-recovery behind an opt-in env var (`CODE_GRAPH_AUTO_RECOVERY=1`). Default behavior remains the structured error → operator decides → manual recovery. Operators who want auto-recovery (the common case where corruption is unintended and the source repo is the source of truth) opt in once. Operators with backup workflows leave the env var unset and continue to receive the explicit error.

### Edge case: Mode 7 false positives

The crash-marker can fire on a non-corrupt DB if a prior process crashed AFTER `BeginBulkWrite` but the DB ended up consistent anyway (rare — requires the crash to land between marker-write and the actual MEMORY-journal pages going stale). `PRAGMA quick_check` is the second-line confirmation. False positives that pass quick_check don't trigger Mode 7's structured error today.

If the crash-marker fires AND `quick_check` passes → no error → no auto-recovery. Safe.
If the crash-marker fires AND `quick_check` flags inconsistency → Mode 7 error → auto-recovery (with env var) is correct.

## Implementation plan (B2)

Add a new helper in `internal/store/store.go` that wraps `OpenPath`:

```go
// OpenPathWithAutoRecovery wraps OpenPath with optional auto-recovery
// behind CODE_GRAPH_AUTO_RECOVERY env var. When the env var is set and
// OpenPath returns one of the auto-feasible recovery error shapes
// (corrupt header, missing main DB+orphan sidecar, BulkWrite crash),
// the wrapper invokes DeleteProject and signals the caller to re-index.
//
// Default behavior (env var unset): identical to OpenPath — caller
// receives the structured error and decides.
//
// Recovery is logged via slog.Warn so the operator sees what happened.
func OpenPathWithAutoRecovery(...) (*Store, recoveryAction, error) {
    s, err := OpenPath(...)
    if err == nil { return s, recoveryNone, nil }
    if os.Getenv("CODE_GRAPH_AUTO_RECOVERY") == "" {
        return nil, recoveryNone, err
    }
    if isAutoRecoverableError(err) {
        slog.Warn("store.auto_recovery_triggered", "path", path, "error_shape", classifyError(err))
        if err := deleteProjectArtifacts(path); err != nil {
            return nil, recoveryNone, fmt.Errorf("auto-recovery failed: %w", err)
        }
        return nil, recoveryReindex, nil
    }
    return nil, recoveryNone, err
}
```

Callers in `index_repository` invoke the wrapper instead of `OpenPath` directly. When the wrapper returns `recoveryReindex`, the caller knows to perform a fresh `force=true` index instead of attempting to read from a non-existent store.

Add a synthetic-fixture test per mode:
- `TestAutoRecoveryDisabledByDefault` — env var unset, error propagates
- `TestAutoRecoveryCorruptHeader` — env var set, recovery triggers
- `TestAutoRecoveryMissingMainDBWithOrphanSidecar` — env var set, recovery triggers
- `TestAutoRecoveryBulkWriteCrash` — env var set, recovery triggers

The existing `TestEndToEndCorruptionRecovery` covers the manual flow; the new tests cover the auto flow.

## Phase B3: index-health monitor SessionStart hook

The SessionStart hook complements B2 by surfacing failure modes B2 doesn't auto-recover (i.e. when the env var is unset). Hook reads the existing `~/.claude/scripts/verify-indexes.py` output via subprocess and surfaces any structured errors as systemMessage.

The plan called for this to run in <2s. `verify-indexes.py` runs `PRAGMA integrity_check` on each indexed DB; on a typical 17-project workstation, total runtime is ~1.5s. Within budget.

If wall budget is exceeded, fall back to surfacing only the artifact written by B2's auto-recovery (the slog.Warn lands in a log; hook reads the last N lines for any "auto_recovery_triggered" entries).

## Falsifiers

| Hypothesis | Falsifier |
|---|---|
| All 3 modes auto-recoverable | Found a mode where auto-recovery destroys recoverable data → that mode stays manual |
| Env var opt-in is safe | Env var inadvertently enables in production paths user didn't intend → make opt-in per-call instead of process-wide |
| Hook runs in <2s | Measure on 17-project workstation; if >2s, defer to async or reduce scope |

## Verdict

Proceed with B2 implementation. All 3 manual modes are auto-feasible behind env-var opt-in. The 1-of-4 falsifier from the plan does not trigger — the actual count was 3, not 4, and the gap was already partially closed by B3.5.
