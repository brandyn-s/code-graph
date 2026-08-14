# Incremental-versus-clean graph equivalence — 2026-08-13

This bounded metamorphic measurement treats a clean persisted rebuild of the
same final source tree as the oracle for an incremental rebuild.

## Scope

Five mutation classes exercise the dependency-invalidation shapes most likely
to strand graph state:

1. Go file rename with an unchanged caller;
2. deletion with no simultaneous source edit;
3. TypeScript re-export target change through an extensionless `./index`;
4. Go receiver-type change; and
5. TypeScript import-target change.

The comparison canonicalizes SQLite row IDs, then compares every persisted
node identity/location and every persisted edge type/end-point pair. It records
incremental-only state as stale and clean-only state as missing. Physical
storage includes the SQLite database, WAL, and SHM after checkpoint.

## Findings and fixes

The initial oracle exposed two deterministic gaps:

- deletion-only runs selected the no-op path because only currently discovered
  files were classified; and
- the incremental SQLite path lost one consumer `IMPORTS` edge when an
  extensionless TypeScript `./index` re-export used a different canonical
  module identity than the clean in-memory path.

The bounded fix now classifies removed file hashes explicitly, uses deleted
paths as importer/caller invalidation targets before cascade deletion, reports
`files_deleted` in `index_delta`, and canonicalizes extensionless JS/TS index
modules consistently. No summary cache was added.

## Five-run result

Source: `fefbe8a`. Command:

```bash
go test ./internal/pipeline \
  -run TestIncrementalMatchesCleanAcrossChangeClasses \
  -count=5 -v
```

| Mutation | Repetitions | Stale nodes | Missing nodes | Stale edges | Missing edges | Incremental median | Clean median | Incremental / clean bytes |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| File rename | 5 | 0 | 0 | 0 | 0 | 33.286 ms | 33.450 ms | 167,936 / 167,936 |
| File deletion | 5 | 0 | 0 | 0 | 0 | 31.676 ms | 32.539 ms | 159,744 / 159,744 |
| TypeScript re-export | 5 | 0 | 0 | 0 | 0 | 33.898 ms | 33.544 ms | 176,128 / 176,128 |
| Go receiver type | 5 | 0 | 0 | 0 | 0 | 32.441 ms | 32.099 ms | 167,936 / 167,936 |
| TypeScript import target | 5 | 0 | 0 | 0 | 0 | 32.197 ms | 32.288 ms | 167,936 / 167,936 |

All 25 incremental graphs were exactly equivalent to their clean oracle and
showed no physical storage amplification in these fixtures. The latency values
are tiny-fixture regression observations, not large-repository performance
claims. Broader transitive dependency shapes remain bounded by the existing
periodic full-rebuild sentinel.
