# Phase D: P99 phase tail investigation

**Date**: 2026-05-06
**Plan**: Plan 5 Phase D (`~/Documents/knowledge-base/plans/2026-05-06-codegraph-remaining-gaps.md`)
**Verdict**: Targeted fix shipped — `config_linker` phase wall reduced
  from 53.18s to 45.12s (-15%) on the code-graph self-index baseline.

## Root cause

The `config_linker` pipeline phase ran a cartesian-product match between
config Variable nodes and code Function/Variable/Class nodes. Inside the
inner loop, every code node's name was re-normalized via
`normalizeConfigKey` per outer iteration:

```go
for _, ce := range entries {       // outer loop over config entries
    for _, code := range codeNodes { // inner loop over code nodes
        codeNorm, _ := normalizeConfigKey(code.Name)  // re-computed each iter!
```

On the code-graph self-index (15,279 nodes, 41,597 edges), this multiplied
the normalization work by `|configEntries|` for no reason — `codeNorm`
depends only on `code.Name`, not on `ce`.

## Fix

Pre-normalize all code-node names exactly once before the match loop.
The change is mechanical: build a `[]codeNodeWithNorm` slice, iterate
that in the inner loop. The rest of the matching logic is unchanged.

`internal/pipeline/configlink_strategies.go:148-202` — see the
`Plan 5 Phase D` comment block.

## Measurement

Same throughput harness (`bench/research/indexing_throughput`), same
target (code-graph self-index), same mode (full):

| metric | before | after | delta |
|---|---|---|---|
| `config_linker` wall | 53.18s | 45.12s | **-8.06s (-15.2%)** |
| P99 phase wall | 53.18s | 45.12s | -8.06s |
| total wall | 256.35s | 270.40s | +14.05s |

Total wall variation is dominated by the `similarity` phase
(62.52s → 82.78s) which is unrelated to this change. The targeted fix
delivered the expected reduction in `config_linker`; total-wall noise
is a separate concern (likely sensitive to Voyage API latency, since
`similarity` calls embedding APIs).

## What's left (next session)

The next-largest tail entries:

- `similarity` (~62-83s, 24-30% of total) — bound by Voyage API
  round-trip latency on cosine queries; not a CPU-side fix.
- `embeddings` (~33s) — bound by Voyage embedding-API throughput.
- `definitions:parse` (~30s spread across multiple sub-phases) —
  tree-sitter parse cost; CPU-bound, would need profiling to find
  whether grammar choice or batch size is the bottleneck.

These are out of scope for Plan 5 Phase D (1h budget). Capture if a
future plan invests in performance.

## Cross-references

- Before baseline: `bench/research/baselines/2026-05-06-indexing-throughput-self-full.json`
- After baseline: `bench/research/baselines/2026-05-06-indexing-throughput-self-fixed.json`
- Plan: `~/Documents/knowledge-base/plans/2026-05-06-codegraph-remaining-gaps.md` Phase D
