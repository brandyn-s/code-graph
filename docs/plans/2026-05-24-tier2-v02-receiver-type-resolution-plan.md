# Tier-2 v0.2+ — Receiver-type resolution for residual fuzzy-Janusian gap

**Date**: 2026-05-24
**Status**: SCOPE (not approved; cost ceiling not committed)
**Prereqs landed**: Tier-2 v0.1 (PR #343 code-graph + PR #346 oracle), 2026-05-23 finding (PR #341), 2026-05-10 finding
**Substrate baseline**: `bench/accuracy/baselines/2026-05-24-psm-rust-report.{md,json}` (post-#346)

## Goal

Lift assetman scope-aligned F1 (high-conf) from 0.869 (post-#346) toward the 2026-05-10 finding's recoverable ceiling of ~0.92 by extending the Tier-2 resolver with receiver-type tracking for call sites the v0.1 cargo-metadata external-chain drop does not address.

## Why this is NOT a 1-hour goal-discipline lever

The 2026-05-10 finding scoped the full Tier-2 effort as "2-3 weeks (substantial resolver state work). Mirrors the original PR #149 PerFuncTypeMap effort." This plan inherits that scope. The cost ceiling is multi-week, governed by sub-phase falsifiers rather than a single binary {SHIP, REVERT}.

## Substrate (post-Tier-2-v0.1 state)

Per the post-#346 2026-05-24 baseline on assetman:

| Metric | Value |
|---|---:|
| Oracle expected | 374 |
| Measured | 531 |
| TP | 332 |
| FP | 58 |
| FN | 42 |
| Precision | 0.851 |
| Recall | 0.888 |
| F1 | 0.869 |
| Ambiguous sites | 24 |
| Ambiguous precision | 0.529 |
| Janusian gap | +0.406 |

Gap to 0.92 ceiling: ~0.051 F1. Closing this requires eliminating ~half of the remaining 58 FPs OR converting ~half of the 42 FNs to TPs.

## Bucket characterization (hypothesized from prior findings)

Without direct DB inspection of the residual edges, hypothesized from the 2026-05-10 finding's bucket framework + the 2026-05-23 substrate analysis:

| Bucket | Est. size | Pattern | v0.2+ mechanism |
|---|---:|---|---|
| Same-crate same-named-method (in-graph trait+impl pair) | ~12-18 FPs | `runnable.run_effect()` where `R: Runnable` resolves to in-graph trait dispatch correctly; `commenter.delete()` where `CommentRepository::delete` vs `WorkOrderRepository::delete` both exist — wrong pick | Receiver-type inference via PerFuncTypeMap chain-walker improvement (drop the heuristic-pick when receiver type is known) |
| Cross-crate trait dispatch (workspace-internal) | ~8-12 FPs | Trait declared in one crate, impls in multiple; e.g., a local `Service` trait | External-chain awareness (v0.1) doesn't apply because chain root IS in workspace; need trait-registry expansion |
| Trait method dispatch with smart pointers / wrappers | ~3-5 FPs | `Box<dyn T>::method`, `Arc<T>::method`, `Pin<Box<T>>::method` — chain walker doesn't unwrap | Extend chain walker to unwrap common wrapper types |
| Legitimate ambiguity (AMBIGUOUS verdict, not FP) | ~5-10 | Two real impls, either could be called depending on runtime state | No mechanism — emit at `confidence_band=ambiguous-runtime` |
| Recall-side: methods we don't emit at all | 42 FNs | Trait dispatches the chain walker can't bottom out at | Diagnostic substrate first; not a single mechanism |

Total addressable target: ~25-35 FP eliminations + ~10-20 FN recoveries → F1 lift ~0.04-0.07.

## Sub-phase structure (proposed)

### Phase v0.2a: Same-crate same-named receiver-type inference

**Substrate**: Identify all assetman FPs where the picked candidate's class is a same-crate sibling impl (e.g., `CommentRepository.delete` vs `WorkOrderRepository.delete`).

**Mechanism**: Extend PerFuncTypeMap to track receiver type per call-site, drop the bare-name fallback when receiver type uniquely identifies the impl.

**Falsifier (per `verify-effectiveness.md`)**: If FP reduction on assetman is < 4 edges with 0 TP regression, retire — the receiver-type signal isn't carrying enough information. Re-run on adversarial-2 + restate-chain-negative + diesel-negative fixtures.

**Ship gate**: Binary {SHIP, REVERT}. SHIP requires assetman F1 ≥ 0.88 (current 0.869 + 0.011 conservative lift), no sibling-fixture CI excluding zero unfavorable. DECIDE if assetman lift < 0.005 OR any sibling fixture regresses with CI excluding zero unfavorable.

**Cost ceiling**: 1 week wall + $0 API (LSP-bound).

### Phase v0.2b: Workspace-internal trait registry

**Substrate**: Workspace traits with multiple impls. Diagnostic count via direct DB query (`MATCH (t:Trait)-[:HAS_IMPL]->(i) WHERE ...`).

**Mechanism**: Build workspace-internal trait registry (separate from the external-chain set v0.1 maintains). When a fuzzy call has multiple candidates AND they're all impls of the same workspace trait, emit at `confidence_band=trait-dispatch-unresolved` instead of picking one.

**Falsifier**: If workspace-trait substrate is < 5 edges on PSM-rust, this phase is not load-bearing — defer or retire.

**Ship gate**: Binary. SHIP requires assetman F1 ≥ 0.89, no regressions. DECIDE otherwise.

**Cost ceiling**: 1 week wall + $0 API.

### Phase v0.2c: Smart-pointer chain unwrap

**Substrate**: Call sites where chain walker bottoms out at `Box<dyn T>`, `Arc<T>`, `Rc<T>`, `Pin<Box<T>>`, `Mutex<T>`-guard methods.

**Mechanism**: Extend chain walker in `oracle-rust-syn` (PR #346 binary) and `internal/pipeline/cargo_metadata.go` chain walker to unwrap common smart-pointer types.

**Falsifier**: If smart-pointer substrate is < 3 edges, defer.

**Ship gate**: Binary. SHIP requires assetman F1 ≥ 0.875, no regressions.

**Cost ceiling**: 3-5 days wall + $0 API.

### Phase v0.2d: Adversarial Rust fixture coverage

**Substrate**: Currently no adversarial Rust fixture in `bench/fixtures/` (per the goal-discipline finding's note). The closest are `actix-data-negative`, `diesel-negative`, `restate-chain-negative` which test specific NEGATIVE patterns but aren't adversarial in the Flask/requests sense (Python-side has flask-adversarial, requests-adversarial).

**Mechanism**: Either (a) construct a hand-curated Rust adversarial fixture covering the v0.1+v0.2 mechanism failure modes (over-aggressive drops, missing trait registrations, smart-pointer edge cases), OR (b) commit to running v0.2 ship-gates against only PSM-rust sub-projects with the explicit acknowledgement that there's no adversarial counterweight.

Decision: option (a) is the higher-quality choice but adds ~1 week of fixture-authoring work. Defer this decision until v0.2a's measurements are in.

**Cost ceiling**: 1 week wall + $0 API.

## Total budget (proposed)

3 weeks wall + $0 API if all 4 sub-phases execute, gated by per-phase falsifiers. Realistically expect 1-2 phases to retire mid-execution (lever-class exhaustion is more common than not at this stage); planning for full budget is conservative.

## Falsifier for the whole effort

If 3 consecutive sub-phases retire (no F1 lift, lever-class saturated per `three_consecutive_regressions_in_same_lever_class_is_a_stop_signal`), STOP the Tier-2 v0.2 effort. The 0.92 recoverable ceiling estimate from the 2026-05-10 finding was hypothesized; if the substrate doesn't support it, the ceiling was over-stated and the engine has reached the receiver-type-resolution lever-class ceiling.

In that case the next plan should pursue a structurally different lever:
- LSP-based resolution (rust-analyzer integration probe was rejected in PR #325; revisit if lever-class saturation here)
- Symbolic execution / interpreter-style for receiver types
- Per-call-site disambiguation via a smaller LLM judge call

## Measurements that would validate v0.2 success

```bash
python3 bench/accuracy/compare.py psm-rust
python3 bench/research/paired_bootstrap_per_subproject.py --baseline post-v0.2 --vs post-#346
```

Per-fixture deltas + paired bootstrap CIs (n_bootstraps=10000) on per-query data. Expected best-case aggregate F1 lift: 0.909 → ~0.94 on PSM-rust (combining ~+0.05 on assetman with no sibling regressions).

## NOT in scope of this plan

- IMPORTS resolver (the `0 / 29` measurement in the baseline; separate effort)
- Python-side work (Tier-2 is Rust-only; Python has its own Janusian gate per PR #320)
- Adversarial fixtures for non-Rust languages (Go fixtures show 1.000 across sub-projects already)
- LSP integration / rust-analyzer (rejected lever per PR #325)

## Decision required to proceed

This plan is SCOPE-only. To proceed, the owner must:

1. Commit a multi-week cost ceiling (vs the goal-discipline 1-hour budget)
2. Choose Phase v0.2a as the entry point (recommended over v0.2b/c) since the same-crate same-named bucket is hypothesized to be the largest residual cluster
3. Set up the per-phase falsifier check infrastructure (per-fixture eval + bootstrap CI runs)
4. Schedule against other parked work (Tier-2 v0.1 had been parked in #343 awaiting #346; check no other parked work is competing)

Until those four are committed, this plan stays in `docs/plans/` as a candidate, not an active workstream.
