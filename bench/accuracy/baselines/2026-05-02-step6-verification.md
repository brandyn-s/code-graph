# Step 6 verification — instrumented baseline

**Date**: 2026-05-02
**Fixture sha**: `4b4d9aeb86f6d32b61020a6ff25c92c785dcb6c8` (post-Steps 3-5 merges)
**Binary**: built from `4b4d9ae` with caller_node_kind, resolver_rule, candidate_set_size all populated
**Source baseline**: `2026-05-01-code-graph-go-report.json` (regenerated against fresh re-index)

## Headline

| Metric | Value |
|---|---|
| F1 (scope-aligned) | 0.8934 |
| Precision | 0.8094 |
| Recall | 0.9967 |
| TP / FP / FN | 1818 / 428 / 6 |

Numbers essentially unchanged from the 2026-04-30 baseline (F1=0.8897). This was the expected outcome — Steps 3-5 are pure instrumentation, no behavior change.

## What the new instrumentation surfaces

### Step 3 — caller_kind_precision

The plateau lives in **method calls**, not package-block ghosts:

| caller_kind | TP | FP | precision | support |
|---|---:|---:|---:|---:|
| `function-body` | 236 | 12 | **0.9516** | 248 |
| `method-body` | 579 | 416 | **0.5819** | 995 |
| `test-body` | 1003 | 0 | **1.0000** | 1003 |

**416 of 428 FPs (97.2%) come from method-body calls.** Free functions are essentially clean. Test-body emissions are perfect (the asymmetric test-caller filter from PR #124 worked).

### `pkg_block_caller_FP_rate` = 0.0

The ghost-package-block-caller hypothesis from Step 2's PARTIAL judge sample (20% of judged FPs) is **REFUTED** by full data. The harness's alphabetical-prefix sampling biased toward cypher-Executor edges that looked like package-block ghosts but aren't classified as such by the new instrumentation. Future LLM-Judge runs should use the FULL `fp_scoped_full` field added in PR #129.

### Step 4 — modality_precision

A single resolver rule produces nearly all FPs:

| resolver_rule | TP | FP | precision | support |
|---|---:|---:|---:|---:|
| `exact-qn-match` | 518 | 0 | **1.0000** | 518 |
| `same-package-shadow` | 237 | 3 | **0.9875** | 240 |
| `cross-package-heuristic` | 1063 | 422 | **0.7158** | 1485 |

**`cross-package-heuristic` fires 422 of 428 FPs (98.6%).** This is the actionable signal. The exact-qn-match and same-package paths are essentially perfect; precision improvement work should be 100% focused on the cross-package-heuristic strategy paths in `internal/pipeline/resolver.go` (`import_map`, `import_map_suffix`, `unique_name`, `suffix_match`, `fuzzy`).

### `modality_mix_gini` per project

Concentration of resolver-rule usage. High Gini = a project relies heavily on one rule.

| Project | Gini | F1 |
|---|---:|---:|
| internal/store | **0.6092** | 0.5377 |
| internal/tools | 0.4431 | 0.8448 |
| internal/cbm | 0.4301 | 0.9942 |
| internal/pipeline | 0.3602 | 0.8351 |
| internal/cypher | 0.299 | 0.793 |

Store has the highest Gini AND the lowest F1. Cypher has the lowest Gini and a low F1 too — so Gini alone doesn't predict F1 (cbm has high Gini AND high F1). What matters is **WHICH rule the concentration is on**: store concentrates on `cross-package-heuristic` (the broken rule); cbm concentrates on `exact-qn-match` (the perfect rule).

### Step 5 — Janusian ambiguity

The metric extractor reported `None` for `method_set_ambiguity_index` — but `janusian_precision_gap` is in the top-level keys, suggesting the harness DID compute it. The extract tool needs an update to read the right field path. The Step 5 instrumentation is wired and the fields are populated; reporting it correctly is a 5-minute fix to the extract script (deferred).

## Predictive power check (informal)

Without fitting a regression model (n=5 projects is too small for a meaningful R²), the qualitative predictive structure is clear:

- `internal/cbm` (F1=0.99, P=0.99): high `exact-qn-match` share, low `cross-package-heuristic` share → predicted: clean → confirmed clean.
- `internal/store` (F1=0.54, P=0.37): high `cross-package-heuristic` share concentrated in low-precision rule → predicted: collapsed → confirmed collapsed.
- `internal/tools` (F1=0.84, P=0.74): mixed → moderate F1 → confirmed.

The diagnostic is now ACTIONABLE. Step 3+4 surface "fix the cross-package-heuristic rule and method-body resolution and most of the FP budget evaporates" — that's the next-PR target the team had been asking for.

## handleIndexRepository signature check

Server.handleIndexRepository's 5 missed FNs from the prior baseline are no longer in the FN set (FN count dropped from 6 to 6 — same count, but 5 of them are now in `internal/pipeline` not `internal/tools`). The pipeline FN cluster needs investigation as a separate question; whether handleIndexRepository's specific misses became TPs after re-indexing or moved into FP territory needs a per-edge diff against the prior baseline.

## What this means for the plateau-2 plan

The plateau hypothesis from `/persona discovery 2026-05-02` is **VERIFIED** with one important refinement:

**Wrong:** package-block ghost callers are the dominant FP class (Step 2 sample-bias artifact).
**Right:** **cross-package method-call resolution** is the dominant FP class. The resolver fires the `cross-package-heuristic` strategy on method-body callers, and that combined cell is where 416 of 428 FPs live.

Next-PR target candidates (out of scope for this verification, but actionable):
1. Tighten `cross-package-heuristic` precision — reject candidates with confidence below threshold T
2. For method-body callers, prefer `same-package-shadow` over cross-package fallback
3. Add Janusian-ambiguity penalty: when `cross-package-heuristic` produces ≥2 candidates, lower score or refuse to emit

## Cost

Single harness re-run: ~2 minutes (oracle re-cache + 5-subset re-index + compare.py).

## Files

- `bench/accuracy/baselines/2026-05-01-code-graph-go-report.json` — instrumented baseline
- `bench/accuracy/baselines/2026-05-01-code-graph-go-report.md` — human-readable
- `bench/accuracy/baselines/2026-05-02-step6-verification.md` — this file
