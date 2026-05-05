# Store Recovery Taxonomy

This document enumerates every corrupt-state failure mode the `internal/store`
package is responsible for handling, and the expected detection + recovery
behavior for each. It is the load-bearing reference for the recovery test
suite (`recovery_test.go`) and the operator-facing "Recovery procedures"
section in CLAUDE.md.

Empirical baseline: the modes flagged "**probed**" below were verified by a
one-shot Go test on 2026-05-05; the test was not committed. All other modes
are tested by the standing suite in `recovery_test.go`.

## Per-DB-mode coverage

`internal/store` opens SQLite databases at four sites. Recovery semantics differ
per site:

| Site | Function | Journal | Synchronous | Notes |
|---|---|---|---|---|
| Production main DB | `OpenPath` (store.go:97) | WAL | NORMAL | Single conn (`SetMaxOpenConns(1)`); immediate txlock; covered by Modes 1-6 |
| Indexer bulk-write window | `BeginBulkWrite` (store.go:232) | MEMORY | OFF | NO journal recovery — Mode 7 |
| Config DB (`_config.db`) | `OpenConfigInDir` (config.go:35) | default rollback | default | Smaller surface; Mode 7b |
| Migration tool | `migrate.go:37` | WAL | default | One-shot path; not in scope |
| In-memory test fixture | `OpenMemory` (store.go:174) | n/a | OFF | n/a (no on-disk state) |

## Prior art (cite in tests, do not duplicate)

These code paths and tools already address pieces of the recovery problem.
Tests must reuse / validate them rather than build parallel versions.

- `internal/store/store.go:134` — **`recoverStaleSHM`** removes a `-shm` file
  when WAL is empty/missing. Triggers on every `OpenPath` call. Handles the
  "stale SHM after SIGKILL" shape of corruption. Distinct from "missing -shm"
  (Mode 3) which is handled by SQLite directly.
- `~/.claude/scripts/verify-indexes.py` — runs `PRAGMA integrity_check` on
  every code-graph `*.db` and every code-search `metadata.db` / `fts5.db`.
  Detection prior art. Recovery taxonomy slots into this existing pipeline:
  the script's exit non-zero is the operator's signal to consult this doc.
- `~/.claude/hooks/session_start_modules/index_corruption.py` — code-search
  counterpart for shape-of-corruption fingerprints (truncated chunk_ids.pkl,
  missing files). Reference architecture for code-graph's own corruption
  fingerprints if we later want SessionStart-time detection.
- `internal/store/router.go:235` — `DeleteProject` closes the Store connection
  and removes `.db` + WAL/SHM files. The recovery procedure for irrecoverable
  modes (4, 5, 7) is `DeleteProject` followed by `index_repository(force=true)`.

## The 7 failure modes

### Mode 1 — WAL truncation

| | |
|---|---|
| **Trigger** | Power loss / kill mid-fsync; storage hardware reorders writes |
| **Symptom** | First read after re-open returns pre-checkpoint state; uncommitted txn lost |
| **Detection** | None — silent and correct (this is what WAL is for) |
| **Recovery** | SQLite auto-recovers; uncommitted transaction is correctly discarded |
| **Operator action** | None |
| **Prior art** | SQLite WAL spec |
| **Test** | `TestRecoverFromTruncatedWAL` (B2) |

### Mode 2 — Crash before commit (WAL mode)

| | |
|---|---|
| **Trigger** | Indexer killed mid-pass on the production main DB |
| **Symptom** | Re-open succeeds; data from the killed transaction is absent |
| **Detection** | None — the missing data is the signal; integrity_check passes |
| **Recovery** | Transaction is correctly discarded; re-run the indexer |
| **Operator action** | Re-run `index_repository` (incremental will pick up where it left off) |
| **Prior art** | SQLite WAL + `WithTransaction` (store.go:196) explicit Rollback on err |
| **Test** | `TestRecoverFromUnflushedTransaction` (B2) |

### Mode 3 — Missing -shm sidecar

| | |
|---|---|
| **Trigger** | User deletes `.db-shm` (cleanup script, accidental rm, antivirus quarantine) |
| **Symptom** | None visible; SQLite re-creates -shm on next open |
| **Detection** | Transparent to the caller |
| **Recovery** | SQLite re-creates the file automatically |
| **Operator action** | None |
| **Prior art** | SQLite shared-memory protocol |
| **Test** | `TestRecoverFromMissingShm` (B2) — assertion must distinguish "SQLite re-created -shm" (this mode) from "`recoverStaleSHM` cleaned a stale -shm" (different code path; fires when WAL is also empty) |

### Mode 4 — Corrupt header bytes

| | |
|---|---|
| **Trigger** | Disk corruption, bit-flip, ransomware, partial overwrite |
| **Symptom** | `OpenPath` returns wrapped error `"init schema: file is not a database"` |
| **Detection** | Caller checks `errors.Is(err, ErrCorruptDatabase)` (B5) or substring match `"file is not a database"` |
| **Recovery** | Manual: `DeleteProject(name)` + `IndexRepository(name, force=true)` |
| **Operator action** | Same — code-graph cannot reconstruct the corrupted pages |
| **Prior art** | `verify-indexes.py` PRAGMA integrity_check (detection); `DeleteProject` (recovery) |
| **Test** | `TestCorruptHeaderReturnsActionableError` (B3) |
| **Empirical** | **Probed 2026-05-05 ✓**: behavior matches expectation; error type is `*fmt.wrapError` |

### Mode 5 — Missing main DB file (with orphan WAL/SHM)

| | |
|---|---|
| **Trigger** | User deletes `.db` but leaves `.db-wal` / `.db-shm` (partial cleanup, interrupted rm, copy-but-not-rename) |
| **Symptom** | **CURRENTLY: silent re-create as fresh empty DB** — no error, no warning, prior data lost |
| **Detection** | **CURRENTLY: none** — caller has no signal |
| **Expected (post-B3.5)** | `OpenPath` returns structured error `"main DB missing but sidecar files present — likely accidental delete; run delete_project to clean up or restore from backup"` |
| **Recovery** | After fix: `DeleteProject(name)` to clean orphan sidecars + `IndexRepository(name)` to rebuild |
| **Operator action** | Confirm the .db deletion was intentional, then run the recovery; if unintentional, restore from backup before code-graph next opens |
| **Prior art** | None for this specific shape — B3.5 is new |
| **Test** | `TestMissingDBWithOrphanSidecarReturnsError` (B3.5 + B3) |
| **Empirical** | **Probed 2026-05-05 ✗**: silent re-create confirmed. Bug, not a polish item — fix is gating for the A grade on Recoverability |

### Mode 6 — Concurrent writers

| | |
|---|---|
| **Trigger** | Auto-sync watcher + manual `index_repository` + MCP query reader on same project |
| **Symptom** | All operations succeed; one writer waits on `_busy_timeout=10000` (set in DSN) |
| **Detection** | Internal — `SQLITE_BUSY` is retried within the busy_timeout window |
| **Recovery** | None needed — WAL allows multiple readers + 1 writer; second writer serializes |
| **Operator action** | None at N=2 or N=3. At N=10+, expect occasional `SQLITE_BUSY` to escape; treat as transient and retry. |
| **Prior art** | `_busy_timeout=10000` in DSN; SQLite WAL multi-reader / single-writer protocol |
| **Test** | `TestConcurrentWritersSerialize` (B4) parameterized at N={2, 3, 10} |

### Mode 7 — Crash during BulkWrite (MEMORY journal)

| | |
|---|---|
| **Trigger** | Indexer killed during the `BeginBulkWrite` window (between `BeginBulkWrite` and `EndBulkWrite`) |
| **Symptom** | Re-open succeeds (SQLite is permissive); but main DB may have inconsistent pages because the journal is in-memory and was lost |
| **Detection** | `PRAGMA integrity_check` — already run by `verify-indexes.py` |
| **Recovery** | None automatic. Operator: `DeleteProject` + `IndexRepository(force=true)` |
| **Operator action** | If `verify-indexes.py` reports non-"ok", re-index the affected project |
| **Prior art** | `verify-indexes.py` PRAGMA integrity_check |
| **Test** | `TestBulkWriteCrashSurfacesViaIntegrityCheck` (B2.5) |

### Mode 7b — Config DB corruption (default journal)

| | |
|---|---|
| **Trigger** | Power loss while writing `_config.db` (default rollback journal, not WAL) |
| **Symptom** | Rollback journal recovers if present; otherwise `ConfigStore.Get` returns the default value (silent fallback by design — see `ConfigStore.Get`) |
| **Detection** | None — the design treats config as best-effort with hardcoded defaults |
| **Recovery** | Delete `_config.db` to reset to defaults |
| **Operator action** | None unless config drift is suspected |
| **Prior art** | `ConfigStore.Get` returns default on read error (config.go:60) |
| **Test** | Not added — config DB is small enough that "delete and re-create" is the entire recovery story |

## Out of scope (explicitly enumerated)

These failure modes are surfaced for completeness but NOT covered by this
workstream. Listed so the taxonomy doesn't silently exclude them.

| Mode | Why deferred |
|---|---|
| Disk full (ENOSPC) mid-write | Surface area is OS-wide. SQLite returns `SQLITE_FULL` which propagates as a normal error; `verify-indexes.py` catches the leftover state. No store-layer logic needed. |
| Read-only filesystem | Operator-induced; out of normal operating envelope. SQLite will return errors on first write. |
| Schema drift across MCP versions | Migration story is separate (`migrate.go`). Recovery is independent of migration. |
| FK violation from external `sqlite3` CLI mutation | User-induced; `DeleteProject` already covers as a recovery path. The `_foreign_keys=1` PRAGMA is enforced at write time, not read time. |
| Pre-emptive corruption detection | `verify-indexes.py` already runs PRAGMA integrity_check. This workstream tests the recovery side, not the detection side. |
| Stress testing under load | Production workload is single-digit writes/sec from the watcher. No "1M writes/sec" benchmark needed. |
| Cross-platform (Linux/macOS) | Windows is the only operational target. Future ECS/Linux deployment is a separate workstream. |

## How to run the recovery test suite

```bash
# Just the recovery tests
go test ./internal/store/ -run TestRecover -v -count=1

# All store tests (includes recovery + base store tests)
go test ./internal/store/ -v -count=1

# Full suite (covers regression-by-side-effect)
go test ./internal/... -count=1
```

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-05-code-graph-corrupt-state-recovery.md`
- Detection tool: `~/.claude/scripts/verify-indexes.py`
- Existing recovery code: `internal/store/store.go:134` (`recoverStaleSHM`)
- Recovery procedure: `internal/store/router.go:235` (`DeleteProject`)
- Operator runbook: see CLAUDE.md "Recovery procedures" section (added by C2)
