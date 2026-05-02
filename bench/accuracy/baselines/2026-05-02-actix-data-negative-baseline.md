# Actix-data-negative fixture: initial baseline

**Date:** 2026-05-02
**Fixture:** `bench/accuracy/synthetic/rust-actix-data-negative/`
**Harness:** `bench/accuracy/check_negative_fixtures.py`
**Audit:** `bench/accuracy/baselines/2026-05-02-actix-data-negative-fixture-audit.md`

## Provenance

Second of four planned negative fixtures per the 2026-05-02 roundtable Recommendation #1. Targets the Actix `web::Data<T>` deref-wrapper class added by PR #149.

## Baseline measurements

| Metric | Value |
|---|---|
| `phantom_count` | **2** |
| Phantom-edge type | `cross-package-heuristic` (same-file shadow dominance) |
| Positive controls emitted | 5 / 5 |

Both phantoms are from `controls() → AppState.{record,flush}`. The `entry()` chain calls correctly peel `web::Data<AppState>` to `AppState`, validating PR #149's deref-wrapper logic.

## What this fixture catches

- **Active phantom (today):** same-file Method node shadows cross-file receiver-type-correct method. `metrics.record(42)` (where `metrics: MetricsCollector`) binds to `AppState.record` (same file as caller) instead of `MetricsCollector.record` (correct receiver-type method, different module).
- **Regression-prevention (today no-op):** if PR #149's deref-wrapper peeling regresses, `entry → MetricsCollector/Cache.{record,flush}` phantoms will fire. Today they don't.

## Class summary

Distinct from the Diesel fixture's bare-name conflation across receiver types. Both are bare-name resolution failures, both fire via `cross-package-heuristic`, but the Diesel case picks the FIRST cross-file candidate while the Actix case picks the same-file shadow over the correct cross-file candidate.

Recommendation #2's `registry.Resolve` consolidation needs to address both:
1. Cross-file receiver-type discrimination (Diesel fixture)
2. Same-file shadow override when a cross-file binding has the correct receiver type (this fixture)

## CI gate semantics

Same as Diesel baseline. `make bench-negative` runs the regression gate; baseline pinned in `negative_baselines.json` at 2 phantoms.

When the same-file shadow class is fixed, expect `phantom_count` to drop from 2 to 0 and 4 new positive-control edges to emit (`controls → MetricsCollector.{record,flush}`, `controls → Cache.{record,flush}`). Re-baseline with audit doc.
