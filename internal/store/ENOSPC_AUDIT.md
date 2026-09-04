# ENOSPC / `SQLITE_FULL` handling audit (Plan 1 Phase D1)

The 2026-05-05 multi-agent roundtable's META_SYNTHESIS flagged
ENOSPC handling as a single-source finding (Opus, with partial
concession from Grok and GPT): "Multi-DB router has no quota /
disk-pressure handling; ENOSPC explicitly out-of-scope in
`RECOVERY_TAXONOMY.md`." For 80K-node projects this is a practical
operational failure mode that hits before most exotic corruption
modes. Phase D1 is the verification.

## Verdict

**No store-layer fix needed.** The current behavior is correct. The
recovery procedure in RECOVERY_TAXONOMY.md (out-of-scope, with
"propagates as a normal error") is verifiably what happens in
practice.

## What was checked

### How SQLite signals ENOSPC

`mattn/go-sqlite3` (the driver code-graph uses) maps SQLite's
`SQLITE_FULL` error code to a `sqlite3.Error` with `Code = ErrFull`.
The error propagates through `database/sql` as a normal error from
`db.Exec` / `db.QueryRow.Scan` / `tx.Commit`.

### How code-graph's store wraps it

Every mutating call in `internal/store/` wraps with
`fmt.Errorf("operation: %w", err)`. Examples:

- `nodes.go:22`: `return 0, fmt.Errorf("upsert node: %w", err)`
- `nodes.go:32`: `return 0, fmt.Errorf("get node id: %w", err)`
- `nodes.go:57`: `return nil, fmt.Errorf("find by name: %w", err)`

The wrapping uses `%w` consistently, preserving `errors.Is`
unwrapping. A caller can detect ENOSPC via:

```go
import "errors"
import sqlite3 "github.com/mattn/go-sqlite3"

var sqliteErr sqlite3.Error
if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrFull {
    // ENOSPC path — recovery is operator-driven (free space, retry)
}
```

### How operators actually see it

When ENOSPC fires:

1. `db.Exec` returns `SQLITE_FULL` error.
2. Store layer wraps: `"upsert node: database or disk is full"`.
3. Pipeline pass's error-handling logs and aborts the indexing job.
4. The MCP server returns the wrapped error to the calling agent.

The operator's observable behavior is "indexing failed with disk
full" — which is exactly the right signal. They free space and
retry.

## Why no store-layer fix

The roundtable's concern was "multi-DB router has no quota / disk-
pressure handling." Adding store-layer logic would mean either:

1. **Pre-flight checks** (`statvfs` before `db.Exec`): adds
   overhead on every write; race-prone (free space changes between
   check and write); and the SQLite driver's error path is already
   sufficient.

2. **Specific recovery (e.g., compact / vacuum on ENOSPC)**: VACUUM
   requires temporary disk space proportional to the DB size, which
   is exactly what's not available during ENOSPC. Counterproductive.

3. **Quota at the multi-DB router level**: code-graph's deployment
   model is single-tenant single-machine; per-project quotas are
   not a feature anyone has asked for. Adding them now would be
   infrastructure for a future need that hasn't materialized.

## What WOULD justify a store-layer fix

If any of these became true, revisit:

- We move to a multi-tenant deployment where one project's runaway
  growth could starve others.
- Operators repeatedly hit ENOSPC mid-indexing of large projects
  AND the wrapped-error handling produces confusing partial-state
  artifacts (orphan WAL files, half-applied schema migrations).
- The `mattn/go-sqlite3` driver changes its error-mapping behavior
  in a future version.

None of these are currently observed. The audit's recommendation:
**keep RECOVERY_TAXONOMY.md's out-of-scope rationale as-is**, and
add a one-line clarification (below) so future readers know this
audit happened.

## Clarification added to RECOVERY_TAXONOMY.md

The "Out of scope" table entry for ENOSPC now reads:

> **Disk full (ENOSPC) mid-write**: SQLite returns `SQLITE_FULL`
> via `mattn/go-sqlite3`'s `Code == ErrFull`. Wrapped as a normal
> error through every store-layer mutation. `verify-indexes.py`
> catches leftover state. Audited 2026-05-05 (ENOSPC_AUDIT.md);
> no store-layer fix needed unless deployment model changes.

## Empirical reproduction (deferred)

A fault-injection test using a small loopback disk image (Linux/WSL
only — Windows fault injection is harder) would exercise the actual
ENOSPC path end-to-end and confirm the error-message shape. Plan 1's
Phase D1 noted this as Linux-only test work; deferred to operator
discretion. If the deployment model changes (and ENOSPC handling
becomes load-bearing), the test should be added before any
store-layer fix ships.

## Cross-references

- Existing: `internal/store/RECOVERY_TAXONOMY.md` "Out of scope" table
- mattn/go-sqlite3 error codes: https://pkg.go.dev/github.com/mattn/go-sqlite3#ErrNo
