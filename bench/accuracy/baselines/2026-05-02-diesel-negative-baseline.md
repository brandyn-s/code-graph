# Diesel-negative fixture: initial baseline

**Date:** 2026-05-02
**Fixture:** `bench/accuracy/synthetic/rust-diesel-negative/`
**Harness:** `bench/accuracy/check_negative_fixtures.py`
**Baseline file:** `bench/accuracy/negative_baselines.json`
**Audit:** `bench/accuracy/baselines/2026-05-02-diesel-negative-fixture-audit.md`

## Provenance

Per the 2026-05-02 code-graph roundtable Recommendation #1
(`~/Documents/knowledge-base/research/dispatch-runs/2026-05-02-codegraph-roundtable/results/META_SYNTHESIS.md`):

> **1. Build known-negative fixtures for external-crate/framework calls
> (Diesel `.get_result`, futures_util `ready`, Actix `web::Data`,
> Restate chains) as CI-gate regression tests. [HIGH confidence;
> LOW-MEDIUM cost]** All three agents converged on this by R4-R5.
> Cheapest, sharpest instrument for the co-hallucination class.

This is the seed fixture (Diesel chain). Three more (Actix, futures_util
`ready`, Restate) are planned as Phase D of the implementation plan.

## Baseline measurements

| Metric | Value |
|---|---|
| `phantom_count` | **1** |
| Phantom-edge type | `cross-package-heuristic` (bare-name suffix-match) |
| Positive controls emitted | 6 / 6 |

Single phantom: `entry → AssetRepo.execute` from
`users.execute(conn)` Diesel chain call. See audit doc for the
per-edge verification.

The baseline reflects code-graph at HEAD as of 2026-05-02 (post-PR
#150 trait→impl swap, post-PR #149 receiver-type inference). Pinned
via `python bench/accuracy/check_negative_fixtures.py rust-diesel-negative
--write-baseline`.

## CI gate semantics

The regression gate runs in two modes:

- `make bench-negative` — strict: fails if any phantom edge appears
  beyond the per-fixture baseline. Used by CI on PR merge.
- `python bench/accuracy/check_negative_fixtures.py rust-diesel-negative`
  (default mode) — fails on ANY phantom emission. Used during local
  development to surface phantoms quickly.

**Goal of the gate:** "doesn't get worse," NOT "passes." Today's
phantom_count = 1; the gate passes as long as that doesn't grow. A
fix to `cross-package-heuristic` that drops the phantom would lower
phantom_count to 0, requiring re-pin via `make bench-negative-baseline`
(documented in the corresponding PR).

## Adjacent findings (do not regress)

The audit doc captures three additional findings that are NOT part
of the gate today but worth tracking:

1. **Bare-name conflation across receiver types** — `controls()` calls
   both `repo.get_result()` and `service.get_result()`; only the
   first emits.
2. **Turbofish chain calls absent from emitted edges** —
   `.get_result::<User>(conn)` and similar produce no method-call
   edges. Likely a separate parser/pipeline issue.
3. **Constructor-call edges absent** — `AssetRepo::new()` calls don't
   emit.

Findings 2 and 3 currently keep the visible phantom count low. When
they are fixed (turbofish parsing in particular), expect
`phantom_count` to jump from 1 to ~3. That jump is a SIGNAL of
upstream improvement, not a regression — the baseline should be
re-pinned at the new value with a baseline doc explaining the cause.

## Sequencing per Recommendation #2

This fixture is the FIRST of multiple negative fixtures. Per the
roundtable's ordering:

> **2. Centralize bare-name resolution policy in `registry.Resolve`
> (single emission point); introduce auxiliary release gates
> (negative fixtures + gold slice + task evals) BEFORE the refactor
> lands.**

Do not begin the `registry.Resolve` consolidation until the negative-
fixture corpus reaches at least 3 of 4 planned fixtures (Diesel,
Actix `web::Data`, futures_util `ready`, Restate chains). The refactor
will likely regress syn-oracle F1 temporarily; this gate is the
auxiliary release evidence that the change is correct independent of
that regression.

## Re-baseline protocol

When code-graph emission for this fixture changes intentionally:

1. Run `python bench/accuracy/check_negative_fixtures.py rust-diesel-negative`
   to see the new phantom set.
2. Add per-edge verification to a new audit doc dated YYYY-MM-DD in
   `bench/accuracy/baselines/`.
3. Run `make bench-negative-baseline` to re-pin
   `negative_baselines.json`.
4. Commit baseline JSON + audit doc + new baseline doc together. PR
   description must cite the change in resolver behavior that
   produced the new count.

Never re-pin without the audit doc — silent re-pinning defeats the
gate's purpose.
