# Tier-2 receiver-type resolution — design

> Status: **design**, post PR #341 finding. No implementation in this
> doc. The v0.1 implementation slice ships per
> `TIER2_V0_1_PLAN.md`; v0.2+ are forward-looking sections.

## Why now

PR #341's diagnostic finding (`bench/research/2026-05-23-fuzzy-janusian-tp-loss-analysis.md`)
identified the lever for assetman's -2.2pp F1 weakness as **upstream
receiver-type resolution**, not Janusian-gate tuning. Direct source
inspection of the dominant `get_result` bucket (25 of 61 gate-eligible
edges) showed the actual call sites are external-crate dispatches
(diesel `RunQueryDsl::get_result`) that the resolver fuzzy-matched
into the only in-graph candidate by bare name.

The 2026-05-10 finding (`bench/research/2026-05-10-assetman-janusian-finding.md`)
already named the layer: "resolver Tier-2 (`PerFuncTypeMap` +
receiver-type lookup). The existing `resolveCallWithTypes` chain
walker handles many cases; the residual is where the chain doesn't
bottom out at a known type." Recoverable ceiling on assetman:
F1 0.878 → ~0.92.

This doc decomposes that scope into shippable slices.

## Existing infrastructure (what NOT to re-build)

| Component | File | Role |
|---|---|---|
| `TypeMap` | `internal/pipeline/typeinfer.go:6` | Local var name → resolved class QN |
| `PerFuncTypeMap` | `internal/pipeline/typeinfer.go:13` | Keyed by enclosing func QN; values are the local TypeMap |
| `FieldTypeMap` | `internal/pipeline/typeinfer.go:21` | `"<structQN>.<fieldName>"` → field-type class QN |
| `ReturnTypeMap` | `internal/pipeline/typeinfer.go:24` | Function QN → return-type class QN |
| `inferTypesCBM` | `internal/pipeline/pipeline_cbm.go:241` | Builds PerFuncTypeMap from CBM-extracted `TypeAssign` records |
| `resolveCallWithTypes` chain walker | `internal/pipeline/pipeline.go:2145-2308` | Walks `obj.field.method(args).method` chains, threads receiver type into `CallContext.ReceiverType` |
| `CallContext.ReceiverType` field | `internal/pipeline/resolver.go:133` | Receiver QN passed to registry for Tier-2 discrimination |
| `resolveViaTypeStaticDispatch` | `internal/pipeline/resolver.go:429-…` | Static `Type::method` dispatch |
| `resolveViaTypeDispatch` (Tier-2 inside `ResolveCtx`) | `internal/pipeline/resolver.go` (Tier-2 discriminator block) | Filters bare-name candidates by `ReceiverType` match |
| `closure_typemap.go::augmentRustClosureTypeMap` | `internal/pipeline/closure_typemap.go` | Rust closure-binding extension |

PR #149 introduced `PerFuncTypeMap`. ACC-003 / ACC-006 / ACC-008
extended its coverage to multi-line chains, own-crate-prefix
stripping, and constructor-call rooting. CG-2 added the "currentType
is class-like" gate at chain landing. The chain walker is mature; the
residual gap is **what the walker can't follow** and **how the
resolver behaves when the walker fails**.

## Gap analysis (post PR #341)

The 71 assetman edges where `strategy=fuzzy` AND `candidate_set_size >= 2`
are exactly the cases where Tier-2 didn't fire. Classifying the gap by
**why the chain walker didn't resolve**:

### Gap class 1: external-crate chain root (dominant — ~25-35 of 71)

Pattern:
```rust
diesel::insert_into(asset_update_stages::table)
    .values(&new_stage)
    .returning(asset_update_stages::all_columns)
    .get_result(conn)            // <-- bare callee `get_result`
```

The chain root is a function call into an external crate (diesel).
The chain walker walks via `returnTypes["diesel.insert_into"]` and
finds nothing (diesel isn't indexed). Walker sets `resolved=false`,
chain receiver type stays empty, registry fuzzy-resolves the bare
name `get_result` into the only in-graph candidate
(`AssetIntrospectImpl.get_result`).

**Root cause**: code-graph has no signal distinguishing "the chain
genuinely terminates in an external crate" from "the chain's
intermediate types are temporarily unknown to us." Both look like
`returnTypes` miss.

**Fix shape**: detect external-crate chain heads via cargo-metadata
ingestion (project's `[dependencies]` list). When the chain root is
known-external AND the walker can't resolve, propagate a sentinel
receiver type (e.g., `_external.<crate>.<type>`) and use it in the
resolver gate (drop, don't fuzzy).

This is the **lever that unifies the abandoned 2026-05-23 cargo-
metadata task** with the Tier-2 work — cargo metadata isn't useful
for replacing `synthetic_structs.go` (different abstraction level),
but it IS useful here for marking chain roots as external.

### Gap class 2: receiver from generic trait bound (real-TP shape — ~10-15 of 71)

Pattern:
```rust
async fn emit_stage_event_effect<R: Runnable>(runnable: &R, ...) {
    runnable.run_effect(async move || { ... }).await   // <-- target Runnable.run_effect
}
```

`runnable: &R` where `R: Runnable`. The receiver type is the **trait
bound**, not a concrete type. The chain walker today binds `runnable`
to type `R` (a generic param), which doesn't match any class-like QN
in the registry; the chain falls through.

The fuzzy resolver eventually picks `Runnable.run_effect` (the trait
declaration), which IS the correct target per syn-oracle convention
for trait-bound dispatch. So these are real TPs that survive
**despite** Tier-2 missing.

**Root cause**: PerFuncTypeMap doesn't capture trait bounds on
generic parameters.

**Fix shape**: when the chain root binds to a generic parameter,
look up the parameter's trait bound(s) in the function signature.
Use the trait QN as the receiver type. This lets Tier-2 match the
trait-method declaration directly (no fuzzy fallback needed).

### Gap class 3: receiver from chained method call landing on internal type (~5-10 of 71)

Pattern:
```rust
self.stage_repo.get_job_progress()    // returns Option<JobProgress>
    .ok_or_else(|| ...)?              // returns Result<JobProgress, _>
    .as_str()                         // bare callee
```

The chain walker today walks `self.stage_repo` (via PerFuncTypeMap +
fieldTypes), then `get_job_progress()` (via returnTypes), then
`ok_or_else()` — but `Option::ok_or_else` is external (`std`), so the
walker's returnTypes lookup misses and falls through to fuzzy on
`as_str`.

**Root cause**: same external-crate gap as Class 1, but the chain
PASSES THROUGH external types on internal-typed intermediates. We
lose the type binding mid-chain.

**Fix shape**: same external-crate awareness as Class 1, with an
additional rule for std collection / Option / Result generic
unwrapping — when an intermediate is known to be `Option<T>` or
`Result<T, E>`, propagate `T` as the next chain segment's type.

### Gap class 4: legitimately ambiguous (~5-10 of 71)

Pattern: the source code itself has multiple correct answers
(`CommentRepository.create` vs `WorkOrderRepository.create` are both
plausible). The 2026-05-10 finding classified these as AMBIGUOUS
verdicts, not pure FPs.

**Root cause**: not a Tier-2 bug. The fuzzy resolver picks one; the
oracle has no preference.

**Fix shape**: out of scope. Improving Tier-2 doesn't help.

## Architecture (cross-class)

The unifying design is a **per-chain final-type-classification**
that distinguishes four outcomes the chain walker can produce, and a
**resolver gate keyed on the classification**:

```
chainReceiverType outcome                Resolver gate behavior
────────────────────────────────────────  ─────────────────────────────
"<InternalClassQN>"                       Tier-2 discriminate as today
"_external.<crate>.<type>" (Gap 1)        DROP — don't fuzzy-resolve
"_generic.<traitQN>" (Gap 2)              Match against trait QN directly
"_unknown" (chain walker truly couldn't   Tier-2 OFF, fuzzy survives
  follow — e.g. chain through fn-pointer
  or closure invocation)                  (current default behavior)
""  (no chain attempted)                  Tier-2 OFF (current default)
```

The four-state outcome replaces today's binary "resolved/not
resolved" return from the chain walker.

The resolver gate uses this state to decide:

- `Internal` → existing Tier-2 candidate filtering (no change)
- `External` → drop the edge (new — closes Class 1, the dominant FP bucket)
- `Generic` → match candidates whose parent QN matches the trait (new — closes Class 2 cleanly without fuzzy)
- `Unknown` → fall through to fuzzy (current behavior — preserves recall on hard cases)

`Internal` and `Unknown` preserve current behavior; `External` and
`Generic` are the new outcomes that move the F1 number.

## Scope decomposition

### v0.1 — External-chain awareness (~1 week)

**Scope**: introduce the `_external.<crate>.<type>` sentinel and the
resolver's `External` gate. Cargo-metadata ingestion to populate the
external-crate set. No PerFuncTypeMap shape changes.

**Closes**: Gap class 1 (the dominant FP bucket — ~25 edges on
assetman).

**Mechanism**:
1. At index time, parse the project's `Cargo.toml` workspace (or
   shell out to `cargo metadata --no-deps`) to build the set of
   external dependency names.
2. In `resolveCallWithTypes`, when the chain root is a static call
   path (`crate::path::fn`) AND the root crate is in the external
   set AND the chain walker would otherwise return `resolved=false`,
   return `chainReceiverType="_external.<crate>"`.
3. In the registry's `ResolveCtx`, when `ReceiverType` starts with
   `_external.`, return empty (drop the edge) before fuzzy fallback.

**Expected impact** (assetman):
- TP recovery: 0 (the 25 `get_result` edges aren't TPs per PR #341)
- FP elimination: ~25 edges (the dominant external-crate FP bucket)
- Net F1: 0.855 → ~0.89 (precision lift from FP elimination)

This is the smallest slice that ships measurable improvement. v0.2
and v0.3 extend the gate's input coverage.

### v0.2 — Generic-bound receiver types (~2 weeks)

**Scope**: capture trait bounds on generic parameters in
PerFuncTypeMap. Extend the chain walker to recognize generic-bound
receivers and produce the `_generic.<traitQN>` sentinel.

**Closes**: Gap class 2 (real-TP shape that today survives via
fuzzy luck).

**Mechanism**:
1. CBM extractor change: when a function signature has
   `fn name<R: Trait>(arg: &R)`, emit a `TypeAssign` for `arg` with
   `TypeName="<traitQN>"` and a new field `BindingKind="generic_bound"`.
2. Resolver: when `ReceiverType` starts with `_generic.`, match
   candidates whose parent QN matches the trait name. If exactly
   one candidate matches, emit that as the type-dispatch target
   (Strategy=`type_dispatch_generic`, confidence high).

**Expected impact** (assetman):
- TP recovery: ~10-15 edges shift from fuzzy (low confidence) to
  type_dispatch_generic (high confidence) — quality lift, not
  recall lift
- FP elimination: low (these edges were already TPs via fuzzy luck)
- Net F1: marginal — the win is in confidence_band, not raw F1

### v0.3 — Std-type generic unwrapping (~1-2 weeks)

**Scope**: when chain walker passes through known std generic types
(`Option<T>`, `Result<T, E>`, `Vec<T>`, `Arc<T>`), unwrap to `T` and
continue walking.

**Closes**: Gap class 3 (chains that pass through std intermediates).

**Mechanism**:
1. ReturnTypeMap pre-population for std generics: when extractor
   sees a `Option<MyType>` return, record `return_type_unwrap` →
   `MyType` so the chain walker can use it.
2. Chain walker: when the next segment's lookup misses in
   `returnTypes` but the current type is a known generic
   (`Option`/`Result`/`Vec`/`Arc`), unwrap and retry.

**Expected impact**: ~5-10 additional assetman edges resolve
internally. Smaller than v0.1/v0.2 because the std-passthrough chains
are less common than external-root chains.

## Confidence framework

Each new resolution path gets a confidence value tied to band:

| Source | Strategy label | Confidence | Band |
|---|---|---:|---|
| External-drop (v0.1) | (no edge emitted) | n/a | n/a |
| Generic-bound dispatch (v0.2) | `type_dispatch_generic` | 0.85 | high |
| Std-unwrap chain landing (v0.3) | `type_dispatch_unwrap` | 0.80 | high |

The new strategies count as resolved for the trace_call_path
confidence-band calculation; their dispatch_kind isn't set (these
are direct resolutions, not synthesized indirect calls — distinct
from the `tagIndirectDispatch` path).

## Validation gates per slice

| Slice | Gate fixture | Pass criteria |
|---|---|---|
| v0.1 | `bench/accuracy/compare.py psm-rust` | assetman F1 ≥ 0.88 (up from 0.855). No regression elsewhere. |
| v0.2 | Same + confidence_band shift | ≥ 10 assetman edges shift from `fuzzy` to `type_dispatch_generic`. F1 ≥ 0.88. |
| v0.3 | Same | assetman F1 ≥ 0.89. |
| any | Loc-Bench iter=2 | 86.0 / 84.5 / 73.5 ±2pp tolerance |
| any | `go test ./internal/pipeline/ -count=1` | All pass |

The cumulative ceiling (v0.1 + v0.2 + v0.3) targets the 2026-05-10
finding's ~0.92 recoverable estimate on assetman. v0.1 alone moves
~70% of the way there because external-crate FPs are the dominant
bucket.

## Risks

1. **Cargo-metadata parse cost.** Shelling out to `cargo metadata`
   adds 1-5 seconds to indexing on workspace repos. Mitigation: cache
   the result, invalidate only on Cargo.lock change. Indexing-time
   regression budget: 5 seconds per repo (per the prior cargo-metadata
   task's STOP criterion).
2. **External-crate set false negatives.** Repos with vendored deps
   or path-dep-only setups may have empty `cargo metadata --no-deps`
   output. Mitigation: also seed the external set from `use` import
   parsing (every `use external_crate::*` adds `external_crate` to the
   set).
3. **External-drop false positives.** A workspace member CAN be
   classified as external if the chain root names a sibling crate.
   Mitigation: exclude workspace-internal crate names from the
   external set — cargo metadata's `workspace_members` field gives
   this directly.
4. **Generic-bound resolution complexity** (v0.2). Trait bounds can
   be multi-trait (`R: Trait1 + Trait2`) or where-clause-style.
   Mitigation: v0.2 handles only the simple `fn name<R: Trait>` form;
   defer multi-bound and where-clause to v0.4.
5. **Confidence band desync.** Adding new strategy labels to
   `dispatchKindConfidence` requires the round-trip pinned by the
   `TestTagIndirectDispatchBandMatchesConfidence` test shipped in PR
   #340. The new strategies aren't dispatch_kinds (they're direct
   resolutions), so the pin doesn't apply, but the resolver's
   `confidenceBand` thresholds must stay consistent.

## Non-goals

1. **Not addressing Janusian-gate tuning** further. PR #341 showed
   the gate is correctly shaped given its information budget; Tier-2
   improvements are the right lever.
2. **Not retiring the existing fuzzy fallback**. Fuzzy stays as the
   `Unknown`-state fallback. Removing it would crater recall on hard
   cases (closures, fn-pointers, dyn dispatch).
3. **Not introducing new edge types**. Tier-2's outputs flow into
   the existing `CALLS` resolution; only `confidence` /
   `confidence_band` / `resolution_strategy` properties change.
4. **Not addressing non-Rust receiver types**. The chain walker
   already handles Python/TS/Go via `language`-aware paths. Tier-2
   improvements are Rust-scoped because that's where the failure
   mode is measured. Cross-language extension is v0.4+.
5. **Not extending PerFuncTypeMap to track ownership/borrowing**.
   The receiver-type discrimination cares about the bound type, not
   whether the receiver is `&T` vs `&mut T` vs `T`. Borrow tracking
   would be a separate primitive.

## Cross-references

- PR #341 finding: `bench/research/2026-05-23-fuzzy-janusian-tp-loss-analysis.md`
- 2026-05-10 finding: `bench/research/2026-05-10-assetman-janusian-finding.md`
- v0.1 implementation plan: `TIER2_V0_1_PLAN.md` (this directory)
- Chain walker source: `internal/pipeline/pipeline.go:2145-2308`
- `ResolveCtx` / `CallContext.ReceiverType`: `internal/pipeline/resolver.go:125-145`
- PerFuncTypeMap origin: PR #149 (the design precedent)
- Cargo-metadata STOP finding: superseded by v0.1 below — cargo
  metadata IS the right primitive here, just at the crate-name
  abstraction level not the type-name level the prior task framed.
