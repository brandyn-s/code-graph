# Restate-chain-negative fixture: initial baseline

**Date:** 2026-05-02
**Fixture:** `bench/accuracy/synthetic/rust-restate-chain-negative/`
**Audit:** `bench/accuracy/baselines/2026-05-02-restate-chain-negative-fixture-audit.md`

## Provenance

Fourth and final fixture of the initial corpus per the 2026-05-02 roundtable Recommendation #1. Catches the multi-segment chain bare-name shadow class.

## Baseline

| Metric | Value |
|---|---|
| `phantom_count` | **3** |
| Phantom-edge type | `cross-package-heuristic` (multi-segment chain shadow) |
| Positive controls emitted | 4 / 4 |

Most prolific fixture in the corpus — each chain segment is an independent binding opportunity, so the multi-segment chain `ctx.invocation().target().send()` produces phantom edges at multiple segments.

## What this fixture catches

- **Active phantoms (3):**
  - `entry → Workflow.run` (single-segment: `ctx.run(...)`)
  - `entry → Invocation.target` (multi-segment middle: `ctx.invocation().target()`)
  - `entry → Invocation.send` (multi-segment terminal: `...target().send()`)

All three bind to internal methods via `cross-package-heuristic` despite the external `Context` receiver type and the external builder types in the chain.

- **Adjacent finding (not gated):** `controls()` calls `w.target()` (Workflow) AND `i.target()` (Invocation); the emitted edge set conflates both into `controls → Invocation.target`. Same pattern as Diesel fixture's bare-name receiver-type conflation. Both finding instances persist until `registry.Resolve` consolidation.

## Class summary — full initial corpus

| Fixture | Class | Phantoms |
|---|---|---|
| `rust-diesel-negative` | Cross-file bare-name conflation (receiver type) | 1 |
| `rust-actix-data-negative` | Same-file shadow vs cross-file receiver type | 2 |
| `rust-futures-ready-negative` | Free-fn shadow ignoring `use` import | 1 |
| `rust-restate-chain-negative` | Multi-segment chain shadow | 3 |
| **TOTAL** | | **7** |

All 7 phantoms emit via `cross-package-heuristic`. Four distinct surface manifestations, ONE shared resolver path.

## Sequencing per Recommendation #2

The initial corpus is now complete. With 4 fixtures live, the auxiliary release gate prescribed by Recommendation #2 has sufficient coverage to begin the `registry.Resolve` consolidation:

> Centralize bare-name resolution policy in `registry.Resolve` (single emission point); introduce auxiliary release gates (negative fixtures + gold slice + task evals) BEFORE the refactor lands.

When the refactor regresses syn-oracle F1 temporarily (expected per the roundtable), this corpus provides the alternative correctness signal. The expected outcome: `phantom_count` drops to 0 across all 4 fixtures, with new positive-control edges emerging for the cross-file qualified-path calls that don't track today.

## CI gate semantics

`make bench-negative` runs the regression gate against all 4 fixtures simultaneously. Any per-fixture phantom_count increase fails the gate. The gate is wired into `.github/workflows/accuracy-regression.yml`.

## Future fixtures (NOT part of this initial corpus)

The roundtable's Recommendation #1 specifically named Diesel, Actix, futures_util, Restate — all delivered. Additional patterns worth considering later:

- `tracing` macros (`info!`, `debug!`, `error!` — bare-name macro calls; tree-sitter sees them as macro_invocation, not function_call)
- `serde` derive interactions (proc-macro generated methods)
- Async trait impls (`async fn` in trait, `BoxFuture`, etc.)

Add these only if real-world code surfaces phantoms not covered by the initial four. The 7-phantom baseline already exercises every surface manifestation of the `cross-package-heuristic` class.
