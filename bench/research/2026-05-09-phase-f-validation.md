# Phase F validation — RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE

**Date**: 2026-05-09
**Plan**: `~/Documents/knowledge-base/plans/2026-05-09-code-search-code-graph-multi-month-arc.md` Phase F

## Question

Does `RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE=1` improve adversarial-fixture CALLS F1 (target: Python ≥0.75, Go within ±0.02 of baseline)?

## Method

Reindex flask + cobra fixtures with and without the env var, run `bench/accuracy/compare.py` for each, compare CALLS F1.

## Results

| Fixture | Mode | Total edges | CALLS measured | CALLS F1 |
|---------|------|-------------|---------------|----------|
| flask | gate UNSET | 7783 | 302 | 0.223 |
| flask | gate SET | 5589 (-28%) | 302 | 0.223 |
| cobra | gate UNSET | 6223 | 1585 | 0.719 |
| cobra | gate SET | 4935 (-21%) | 1585 | 0.718 |

**The gate measurably reduces total edge count (~25% in both fixtures) but does NOT move CALLS F1.**

## Why F1 didn't move

CALLS measured count is identical pre/post gate on both fixtures. The dropped edges are NOT CALLS — they're USAGE / IMPLEMENTS / OVERRIDE / other edge types.

Reading the resolver code path:

- **Python (flask)**: `shouldDropCrossPackageSuffix(lang.Python)` already drops the entire suffix bucket BEFORE the new gate's branch. The Phase F gate has nothing to gate for Python CALLS — CG-1 fired first.
- **Go (cobra)**: most CALLS resolve via `resolveViaImportMap` (Strategy 1) which runs upstream of `unique_name` / `suffix_match`. The few CALLS that fall through to those strategies happen to have import-reachable candidates that pass the gate.

In both cases, the Phase F gate fires on a different code path or a different set of edges than the CALLS metric measures.

## Verdict

**The gate exists and works correctly** (7 unit tests pass). It drops emissions where intended (per-candidate import-reachability check). But the available adversarial fixtures (flask, cobra) don't surface its effect on CALLS F1 because:
1. Python's existing CG-1 language-drop is already aggressive enough to dominate
2. Go's CALLS resolution bypasses the gated strategies for the bulk of edges

The 25% edge-count reduction (USAGE/etc.) is a side-effect — those edges aren't captured by the F1 metric, so we can't validate whether dropping them is precision-improving without an oracle for those edge types.

**No default flip.** Phase F's premise (that the looser cross-package buckets emit precision-leaking CALLS edges) doesn't reproduce on the available fixtures with the F1 oracle the harness uses. The existing CG-1 + the new gate both work, but only CG-1 affects the measured CALLS bucket.

## What to do next

If a future plan wants to actually move CALLS precision on Go adversarial fixtures, the right lever is upstream of unique_name/suffix_match — likely tightening `resolveViaImportMap` or adding a cross-package-imported-bucket-precision gate. The Phase F gate stays as opt-in infrastructure.

## Cost

- 2 fixture reindexes (flask + cobra): ~30 sec wall, no API spend (no Voyage embedding for these fixtures)
- 4 compare.py runs: ~10 sec wall
- **Total: <1 min**

## Files

- `bench/accuracy/baselines/2026-05-09-flask-adversarial-report.{md,json}` — gate-set run
- `bench/accuracy/baselines/2026-05-09-cobra-go-report.{md,json}` — gate-set run
- This findings doc
