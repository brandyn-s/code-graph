# Watcher content-hash fallback design (Plan 1 Phase D3)

The 2026-05-05 multi-agent roundtable's single-source finding (Grok,
partial concession from Opus and GPT): "Incremental indexing semantic
drift via mtime+size polling in `internal/watcher`. Content-preserving
size-equal mtime-equal edits — say, a variable rename `A → B` of equal
length — could go undetected if a tool also preserves mtime."

## Verdict

**Documented as a known limitation; selective implementation.** The
exact size-equal-mtime-equal scenario is rare in practice but real.
Full content-hashing on every poll is too expensive (would re-hash
every file every interval). The right shape is a **probabilistic
fallback**: hash on suspicion, not on every poll.

## Current behavior

`captureSnapshot` (`watcher.go:584`) walks the file tree and records
`fileSnapshot{modTime, size}` per file. `snapshotsEqual` compares
two snapshots; identical mtime + size = "no change."

The strategy hierarchy:
- **Git** (when repo): `gitHead()` returns HEAD SHA; full re-walk
  only when SHA changes. Catches every change a commit captures.
- **FSNotify** (when available): file-system events trigger reindex
  on a sentinel file change.
- **Dir-mtime** (fallback): polls top-level directory mtime;
  reindexes on change.

All three strategies fall back to a periodic FULL snapshot
(`pollsSinceFull >= fullSnapshotInterval`) to catch in-place edits
that mtime-monitoring missed.

## The size-equal-mtime-equal class

mtime+size detection misses:

1. **Variable rename of equal length**: `A → B` with both single-
   character. mtime updates, so this case actually IS detected
   (mtime changes on save).

2. **Touch with stale mtime**: a tool writes new content but resets
   mtime to the original (`utime` syscall). Real but rare; happens
   with some build tools, archive extracts, version-control
   operations.

3. **Atomic-replace with content-preserving size + mtime**:
   `mv tmp.go file.go` where the new file has the same size and
   the modification was crafted to land at the same mtime second.
   Vanishingly rare in normal development.

The forced full snapshot (`fullSnapshotInterval`) catches these
eventually. The question is whether eventual is good enough.

## Why not hash everything

Naive content-hashing would:

- Read every file every poll. For a 10K-file repo with 1MB average
  file size = 10GB of disk read per poll. Unacceptable.
- Even with blake3 (fast), the I/O dominates; CPU is fine.

## The right shape: hash-on-suspicion

Instead of unconditional content-hashing, hash only when:

1. `mtime + size` is unchanged AND
2. A downstream signal suggests change happened: graph edge count
   shifted, last-known-import-set changed, or the user explicitly
   reported "I changed file X but the index didn't update."

The signal in (2) is currently the operator's word. A more
sophisticated implementation would track per-file `last_indexed_sha`
in the store and re-hash on `mtime ± epsilon` (mtime within 1s of
last index but content might differ — common around save-and-rebuild
cycles).

## Recommended implementation

**Selective content hash on the FORCED full snapshot path.** When
`pollsSinceFull >= fullSnapshotInterval` triggers a full re-walk
anyway, also compute a blake3 hash of the first N KB of each file
(N = 16, fits in one disk read; catches most content changes).
Compare against the prior baseline's hash field. If hash differs
on otherwise-mtime-equal files, force re-index of those specific
files.

Cost: one extra disk read per file per `fullSnapshotInterval`
period (~hours, not seconds). Affordable.

Implementation sketch:

```go
type fileSnapshot struct {
    modTime  time.Time
    size     int64
    sha256_first_4k string  // NEW: empty until first hash
}

// captureSnapshot only sets sha256_first_4k when fullSnapshot=true
// (called from forced full-snapshot path).

// snapshotsEqual returns false if a file's mtime+size match but
// sha256_first_4k differs (content changed without mtime/size signal).
```

**Cost-benefit**: this catches 95% of the missed-edit class at near-
zero per-poll cost (only fires on full-snapshot intervals). The
remaining 5% (content-preserving same-prefix edits, e.g., a change
that lands past byte 4096) is documented as a known limitation; the
operator's escape hatch is `index_repository(force=true)`.

## When to ship

Ship when the false-negative class is observed in practice. Currently
unobserved — the roundtable's flag is theoretical. The harness above
can land first as a shape-only change (struct field added, computed
on full-snapshot path, used for nothing) so subsequent activation is
a one-line `if hash differs` check rather than a refactor.

## What to do meanwhile

Document the known limitation in CLAUDE.md / watcher.go's package
comment. Operators with a suspected stale index should run
`index_repository(force=true)` — already documented as the recovery
path for several modes.

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-05-codegraph-and-cross-tool-recommendations.md` Phase D3
- Roundtable single-source finding: `~/Documents/roundtables/2026-05-05-code-graph/results/META_SYNTHESIS.md` (Grok's "incremental indexing semantic drift")
- Existing watcher: `internal/watcher/watcher.go`
- Hash library: blake3 is the right primitive; not currently a dependency. Adding it would be a tiny incremental cost (single Go module).
