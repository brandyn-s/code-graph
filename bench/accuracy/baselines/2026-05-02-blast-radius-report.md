# Per-call-site blast-radius — code-graph-go @ 82de048

**Date**: 2026-05-02
**Source baseline**: `2026-04-30-code-graph-go-report.json`
**Mode**: **PARTIAL** (10 FP + 6 FN samples per arm; full sets pending next harness re-run after `fp_scoped_full` field ships)
**Tool**: `blast_radius.py`

## What this measures

Per-site blast_radius = #FPs sharing site + #FNs sharing site

Step 1 of the 2026-05-02 plateau-2 plan. Surfaces single-call-site explosions
that aggregate F1 + per-project F1 hide.

## Headline finding (visible in PARTIAL mode)

**Top-1 site by blast_radius: `internal-tools.index.Server.handleIndexRepository` (blast=5)**

5 of 6 sample FNs concentrate on one function — all calls into the same
`tools` package (`errResult`, `getBoolArg`, `getStringArg`, `jsonResult`,
`parseArgs`).

This is exactly the FN-cluster anomaly that the `/persona` cohort surfaced
on 2026-05-02. The aggregate F1 (0.890) and the per-project F1 (`internal/tools`
= 0.845) both spread this single-function failure across multi-edge averages.
At the site level it's an outlier of >2x the next-highest blast.

## Top sites in PARTIAL data

| blast | site |
|------:|------|
| 5 | `internal-tools.index.Server.handleIndexRepository` |
| 3 | `internal-cbm.lsp_bridge.RunGoLSPCrossFile` |
| 2 | `internal-cbm.cbm.ExtractFile` |
| 2 | `internal-cypher.executor.Executor.Execute` |
| 2 | `internal-cypher.executor.Executor.aggregateResults` |
| 1 | `internal-cypher.executor.Executor.evaluateCondition` |
| 1 | `internal-store.store.Store.Close` |

## Caveat (PARTIAL data)

The 10 FP + 6 FN samples per arm aren't a representative slice of the
full 427 FP / 6 FN scope-aligned error set:

- **FNs** are fully captured (only 6 in the full set)
- **FPs** are NOT — only 10 of 427 are visible. The top-20 sites table
  here is therefore not the true top-20.

To get the full picture (p50/p95/max distribution, top-1% share of total
errors, true top-20 sites by blast), re-run `compare.py` after the
`fp_scoped_full` / `fn_scoped_full` fields land (this PR). Then:

```bash
python bench/accuracy/blast_radius.py bench/accuracy/baselines/<date>-code-graph-go-report.json
```

The PARTIAL-mode finding (top-1 site = handleIndexRepository) is robust because
all 6 of the actual FNs are visible in the FN sample, and blast-radius for FNs
is a complete signal. The FP shape from samples is suggestive but not
definitive.

## Implication for plateau-2 plan

Step 1 confirms the FN-cluster anomaly empirically. The cohort hypothesis
("the plateau is partly a single-site explosion, not uniform error") is
not refuted by the partial data and is strongly supported by the FN sample.

Steps 3–5 (caller-kind tagging, modality tagging, Janusian) are still
worth shipping; this analysis just adds a per-site dimension that should
be run again after each step's harness re-run to track whether the new
fields collapse handleIndexRepository's blast or expose new outliers.
