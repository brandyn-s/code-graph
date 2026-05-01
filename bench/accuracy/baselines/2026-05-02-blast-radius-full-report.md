# Blast-radius FULL-mode report

**Date**: 2026-05-02
**Source**: `2026-05-01-code-graph-go-report.json` (Step 6 instrumented baseline)
**Mode**: FULL (auto-detected via `fp_scoped_full` / `fn_scoped_full` fields)
**Replaces**: `2026-05-02-blast-radius-report.md` (PARTIAL slice from 2026-05-01)

## Aggregate

| Metric | Value |
|---|---:|
| Distinct call sites with errors | 185 |
| Total error edges (FP+FN) | 434 |
| p50 / p95 / max blast | 2 / 6 / 34 |
| Top 1% sites share of total errors | **7.8%** |

## Top-20 sites by blast_radius

| blast | site |
|---:|---|
| **34** | internal-pipeline.pipeline.Pipeline.runIncrementalPasses |
| **20** | internal-pipeline.pipeline.Pipeline.runFullPasses |
| 10 | internal-pipeline.pipeline.Pipeline.passDefinitions |
| 7 | internal-pipeline.nix_services.Pipeline.passNixServices |
| 7 | internal-tools.architecture.Server.handleManageADR |
| 7 | internal-tools.snippet.Server.handleGetCodeSnippet |
| 6 | internal-cypher.executor.Executor.executeStepsForProject |
| 6 | internal-cypher.lexer.Lexer.lexNextToken |
| 6 | internal-store.migrate.StoreRouter.migrate |
| 6 | internal-tools.dataflow.Server.handleDataFlow |
| 6 | internal-tools.trace.Server.handleTraceCallPath |
| 5 | internal-pipeline.adaptive.adaptivePool.onTick |
| 5 | internal-pipeline.pipeline.Pipeline.passHTTPLinks |
| 5 | internal-tools.detect_changes.Server.handleDetectChanges |
| 5 | internal-tools.index.Server.handleIndexRepository |
| 4 | internal-pipeline.configlink_strategies.Pipeline.passConfigLinker |
| 4 | internal-pipeline.dataflow.Pipeline.passDataflow |
| 4 | internal-pipeline.pipeline.Pipeline.flushResolvedEdges |
| 4 | internal-store.architecture.Store.archLayers |
| 4 | internal-store.migrate.StoreRouter.migrateProject |

## Per-project (FULL)

| Project | sites | total_err | p50/p95/max | top1%share | F1 |
|---|---:|---:|---:|---:|---:|
| internal/store | 54 | 109 | 2/4/6 | 5.5% | 0.5377 |
| internal/cypher | 12 | 30 | 2/6/6 | 20.0% | 0.793 |
| internal/pipeline | 51 | 156 | 1/20/34 | **21.8%** | 0.8351 |
| internal/tools | 66 | 134 | 1/6/7 | 5.2% | 0.8448 |
| internal/cbm | 2 | 5 | 2/3/3 | 60.0% | 0.9942 |

## Headline check (FN concentration)

- **Top-1 site by FN count**: `internal-tools.index.Server.handleIndexRepository`
- **FN count at top site**: 5 of 6 total FNs (**83%**)
- Confirms cohort hypothesis: a single call site dominates FN distribution.
- The plateau is partly a single-site explosion, not uniform error.

## What changed vs PARTIAL

PARTIAL (2026-05-02-blast-radius-report.md) sampled 10+6 edges per project. This run uses the full 428 FPs + 6 FNs. Distribution stats (p50/p95/max), top-1% share, and top-20 site list are now meaningful.

## Implication for plateau-2 fixes

- **internal/pipeline**'s `runIncrementalPasses` + `runFullPasses` together = **54 errors (12.4% of all errors)**. Both are method-body callers in the cross-package-heuristic cell — Phase Y A1 (threshold P≥0.85) should clean up most of this concentration.
- **internal/cbm** is essentially solved (F1=0.99, 5 errors total at 2 sites).
- **internal/store**'s 109 errors are spread thin (54 sites, max blast 6) — a wider population that won't be solved by any single-site fix; needs the resolver-rule shift (Phase Y A2/A3).
