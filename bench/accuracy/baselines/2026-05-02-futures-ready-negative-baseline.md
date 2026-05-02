# Futures-ready-negative fixture: initial baseline

**Date:** 2026-05-02
**Fixture:** `bench/accuracy/synthetic/rust-futures-ready-negative/`
**Audit:** `bench/accuracy/baselines/2026-05-02-futures-ready-negative-fixture-audit.md`

## Provenance

Third of four planned negative fixtures per the 2026-05-02 roundtable Recommendation #1. Catches the bare-name free-function shadow class.

## Baseline

| Metric | Value |
|---|---|
| `phantom_count` | **1** |
| Phantom-edge type | `cross-package-heuristic` (free-function bare-name shadow) |
| Positive controls emitted | 3 / 3 |

Phantom: `entry → signal.ready` despite `use futures_util::future::ready` in scope.

## What this fixture catches

- **Active phantom (today):** bare-name free-function call (`ready(42)`) binds to internal `signal::ready` instead of the externally-imported `futures_util::future::ready`. The resolver does not consult `use` imports when picking among bare-name candidates.

## Class distinction (full corpus, 3 fixtures so far)

| Fixture | Phantom class | Receiver | Resolver path |
|---|---|---|---|
| `rust-diesel-negative` | Cross-file bare-name conflation across receiver types | method | cross-package-heuristic |
| `rust-actix-data-negative` | Same-file shadow vs cross-file receiver-type method | method | cross-package-heuristic |
| `rust-futures-ready-negative` | Bare-name free-fn shadow ignoring `use` import | free fn | cross-package-heuristic |

Three fixtures, three phantom shapes, ONE resolver path. This validates the roundtable's Recommendation #2 framing: the load-bearing architectural debt is the duplication of bare-name resolution across paths without per-path receiver-type / import-binding discrimination.

## CI gate semantics

Same as Diesel/Actix baselines. `make bench-negative` runs the regression gate.

When the bare-name shadow class is fixed (likely via Recommendation #2's `registry.Resolve` consolidation), expect:
- `rust-futures-ready-negative.phantom_count` → 0
- New positive-control edges emit (cross-file qualified-path free-function calls become trackable)

Re-baseline with audit doc when that happens.
