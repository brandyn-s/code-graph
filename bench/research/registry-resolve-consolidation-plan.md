# `registry.Resolve` consolidation plan

**Date:** 2026-05-02
**Status:** Draft — pending implementation
**Provenance:** 2026-05-02 code-graph roundtable Recommendation #2 (`~/Documents/knowledge-base/research/dispatch-runs/2026-05-02-codegraph-roundtable/results/META_SYNTHESIS.md`)

## Problem

`FunctionRegistry.Resolve` at `internal/pipeline/resolver.go:61` exposes one entry point but **dispatches across multiple sibling strategies (`resolveViaImportMap`, `resolveViaSameModule`, `resolveViaNameLookup`, `resolveSuffixMatch`, `pickBestCandidate`, `FuzzyResolve`) that each pick a candidate independently**. None of them consult:

- the call site's **inferred receiver type** (added by PR #149's `PerFuncTypeMap`)
- the **`use` import bindings** in scope at the caller
- whether multiple internal candidates share the bare name and which one is receiver-type-correct

The negative-fixture corpus (PRs #151–#154) demonstrates that this absence produces **7 phantom CALLS edges across 4 distinct surface manifestations**, all emitting via `cross-package-heuristic` (`resolver_rule.go`):

| Manifestation | Class | Resolver path |
|---|---|---|
| `rust-diesel-negative` | Cross-file bare-name conflation across receiver types (method) | suffix_match / unique_name |
| `rust-actix-data-negative` | Same-file shadow vs cross-file receiver-type method (method) | suffix_match |
| `rust-futures-ready-negative` | Free-fn shadow ignoring `use` import (free function) | suffix_match / unique_name |
| `rust-restate-chain-negative` | Multi-segment chain shadow (method, repeated per segment) | suffix_match |

Same architectural debt, four surfaces, one resolver path. The fix is **not a series of independent strategy patches** (PR #150's symmetric `resolveCallEdge` filter caught only 12 of 369 phantoms because the duplicated logic was invisible to a single-site fix). The fix is **a unified call-context structure that every strategy consumes**, plus discrimination criteria that fire when `CandidateCount > 1`.

## Goal

Centralize the candidate-discrimination policy in `FunctionRegistry.Resolve` so:

1. **One call-context surface** — every strategy receives the same `CallContext` struct (callee name, caller QN, module QN, import map, receiver type, import bindings, in-scope aliases).
2. **One discrimination ladder** — when a strategy returns `CandidateCount > 1`, a shared tiebreaker chooses the correct target using receiver type, import binding, and package proximity in a fixed precedence order.
3. **One emission point** — `resolveCallEdge` (current site of the symmetric filter from PR #150) becomes a thin wrapper around `Resolve(CallContext) → ResolutionResult` with no per-path logic of its own.

## Non-goals

The Group B findings in `bench/accuracy/FOLLOWUPS.md` (ACC-001 through ACC-004) are **out of scope** for this consolidation. They are about MAKING candidates available in the first place (parser, path resolution, import tracking), not about CHOOSING among candidates. Mixing them in:

- inflates blast radius across 4 unrelated code paths
- conflates two metrics (`phantom_count` regression vs missing-edge expansion)
- makes the negative-fixture corpus less useful as a disambiguation tool — if both phantom_count drops AND new edges appear, you can't tell which change caused which effect

Address Group B as separate, sequential PRs after this consolidation lands.

## Current state (verified by reading source)

`internal/pipeline/resolver.go`:

```go
// 467 LOC, 6 strategies feeding one entry point
func (r *FunctionRegistry) Resolve(calleeName, moduleQN string, importMap map[string]string) ResolutionResult {
    // ... dispatches to strategies in fall-through order:
    //   resolveViaImportMap         → "import_map" / "import_map_suffix"
    //   resolveViaSameModule        → "same_module"
    //   resolveViaNameLookup        → "unique_name"
    //   resolveSuffixMatch          → "suffix_match"
    //   pickBestCandidate           → "suffix_match" (fallback)
    //   FuzzyResolve                → "fuzzy"
}
```

Strategy → `resolver_rule.go` mapping:
- `import_map`, `import_map_suffix`, `unique_name`, `suffix_match` → `cross-package-heuristic`
- `same_module` → `same-package-shadow`
- `type_dispatch` (TypeMap-driven, set by `pipeline_cbm.go`) → `interface-dispatch`
- `fuzzy` → `fuzzy-resolve`

`ResolutionResult` already carries `CandidateCount` (per `candidate_set.go`), so the multi-candidate signal exists but isn't consumed for discrimination.

Receiver-type info exists too: `pipeline_cbm.go::inferTypesCBM` produces `PerFuncTypeMap` keyed by `(funcQN, varName) → typeQN`. Today this feeds `resolveCallWithTypes` directly (PR #149's path) but **does not flow into the registry strategies**. That's the consolidation gap.

## Target state

### New types

```go
// internal/pipeline/resolver.go
type CallContext struct {
    CalleeName     string
    CallerQN       string                  // empty for module-level callers
    ModuleQN       string
    ImportMap      map[string]string       // existing — module-prefix → resolved module path

    // NEW — populated by pipeline_cbm.go from PerFuncTypeMap + extraction
    ReceiverType   string                  // empty if no inferred receiver
    ImportBindings map[string]string       // bare-name → qualified-target from `use` imports
    Aliases        map[string]string       // `use X as alias` mappings (ACC-004; field exists but populated empty until ACC-004 ships)
}

// Existing — extended with discrimination context
type ResolutionResult struct {
    QualifiedName  string
    Strategy       string
    Confidence     float64
    CandidateCount int
    DiscriminationApplied string  // NEW — which tiebreaker fired (empty = candidate set was unique)
}
```

### New entry point

```go
func (r *FunctionRegistry) Resolve(ctx CallContext) ResolutionResult {
    // 1. Strategy ladder unchanged — each strategy receives ctx
    if res := r.resolveViaImportMap(ctx); res.QualifiedName != "" { return res }
    if res := r.resolveViaSameModule(ctx); res.QualifiedName != "" { return res }
    if res := r.resolveViaNameLookup(ctx); res.QualifiedName != "" { return res }
    if res := r.resolveSuffixMatch(ctx); res.QualifiedName != "" { return res }
    if res := r.pickBestCandidate(ctx); res.QualifiedName != "" { return res }
    if res, ok := r.FuzzyResolve(ctx); ok { return res }
    return ResolutionResult{}
}
```

Each strategy keeps its existing logic but, **whenever it would return a result with `CandidateCount > 1`**, runs the candidates through a shared discrimination ladder before picking.

### Discrimination ladder

Order is the entire policy. State it explicitly so it's reviewable:

| Tier | Test | Wins when |
|---|---|---|
| 1 | **Exact QN match** — does any candidate's QN exactly equal a fully-qualified target the caller could plausibly mean? | Always (already covered by `import_map` strategy) |
| 2 | **Receiver-type match** — `ctx.ReceiverType != ""` AND a candidate's parent class is `ReceiverType` (or peeled deref-wrapper of it per PR #149) | Method calls with inferred receiver type |
| 3 | **Import binding** — `ctx.ImportBindings[ctx.CalleeName]` exists AND a candidate matches that binding | Free-function calls with `use` imports |
| 4 | **Same-package proximity** — candidate's module is the caller's module OR a child of it | Package-local conventions |
| 5 | **No discrimination possible** — fall back to current heuristic (alphabetical / first-encountered) BUT downgrade confidence by `0.5×` and set `DiscriminationApplied = "fallback-no-tiebreaker"` | Last resort; signals to harness that the edge is speculative |

Tier 1 and 2 are firm. Tier 3 is firm. Tier 4 is heuristic — a candidate's "package proximity" preference can produce false confidence on legitimately-out-of-scope same-named methods. Tier 5 is the no-discrimination escape hatch; its confidence downgrade ensures the harness's high-confidence band excludes these edges.

### Call-site changes

`pipeline_cbm.go::resolveFileCallsCBM`:

```go
// BEFORE
res := r.Resolve(callee, moduleQN, importMap)

// AFTER
ctx := CallContext{
    CalleeName:     callee,
    CallerQN:       call.EnclosingFuncQN,
    ModuleQN:       moduleQN,
    ImportMap:      importMap,
    ReceiverType:   typeMap.LookupReceiver(call.EnclosingFuncQN, call.ReceiverVar),
    ImportBindings: extractionCache.ImportBindings[fileQN],
    Aliases:        extractionCache.UseAliases[fileQN],  // empty until ACC-004
}
res := r.Resolve(ctx)
```

`pipeline.go::resolveCallEdge`:

The post-PR-150 symmetric filter at this site (the one that caught 12/369) becomes redundant — the discrimination is now upstream in `Resolve`. Delete the filter; rely on the registry's `DiscriminationApplied` field for stratification.

## Migration plan (phased)

### Phase 1: Add `CallContext`, keep behavior unchanged

- Add `CallContext` struct alongside the existing signature
- Add `Resolve(ctx CallContext)` as a new method that constructs the legacy args and forwards to the existing implementation
- Update one call site (`pipeline_cbm.go::resolveFileCallsCBM`) to use the new entry point
- All strategies still take legacy args internally
- **Validation:** all existing tests pass, syn-oracle F1 unchanged, negative-fixture phantom counts unchanged

### Phase 2: Thread `CallContext` through every strategy

- Change each `resolveVia*` signature to take `CallContext`
- No discrimination logic yet — every strategy just unpacks the context into its existing locals
- **Validation:** unchanged from Phase 1

### Phase 3: Add discrimination ladder, tier-by-tier

Sub-PRs, one per tier:

- **Phase 3a:** Tier 2 — receiver-type discrimination. Lights up the Diesel + Actix + Restate phantoms.
- **Phase 3b:** Tier 3 — import binding. Lights up the futures-ready phantom.
- **Phase 3c:** Tier 4 — same-package proximity. May regress some edges; measure carefully.
- **Phase 3d:** Tier 5 — confidence downgrade for no-discrimination case. Stratification only; no edge loss.

Each Phase 3 sub-PR:
- Updates the relevant negative-fixture `ground_truth.json` to add new positive-control edges that should now emit (e.g., `controls → IntrospectService.get_result` becomes expected once Tier 2 lands)
- Re-baselines `negative_baselines.json` (each `phantom_count` drops as discrimination fires)
- Reports per-fixture syn-oracle F1 delta in the baseline doc

### Phase 4: Remove the symmetric filter from `resolveCallEdge`

After Phase 3 lands, the post-PR-150 filter is dead code (its job is now upstream). Delete it. Run the full Rust + Go baselines. **Expected:** no F1 change vs Phase 3d — the filter was already a subset of what the registry now does.

## Validation strategy

### Auxiliary release gates (pre-existing)

- **Negative-fixture corpus** (4 fixtures, 7 phantom baseline) — `make bench-negative`. Watch `phantom_count` per fixture across phases.
- **Positive-control expansion** — each Phase 3 sub-PR adds positive controls for the edges that should newly emit. Failure to add them means the consolidation is silently absorbing edges instead of routing them correctly.
- **Synthetic prove-the-instrument fixtures** — `rust-minimal`, `go-minimal`. Must stay at FP=0 FN=0.
- **Real-fixture baselines** — `psm-rust` (assetman dominant), `code-graph-go`, `cobra-go`, `mcp-servers`. Track per-project F1.

### Expected outcomes per phase

| Phase | phantom_count (sum) | Syn-oracle F1 delta | Notes |
|---|---|---|---|
| Phase 1 | 7 (unchanged) | 0 | No-op refactor |
| Phase 2 | 7 (unchanged) | 0 | Plumbing only |
| Phase 3a (receiver-type) | ~3 | likely –2 to –4pp temporarily | Diesel + Actix + Restate phantom drop; some real-fixture edges may regress while receiver-type inference accuracy catches up |
| Phase 3b (import binding) | ~2 | minimal | futures-ready phantom drops |
| Phase 3c (proximity) | 0 | variable | May produce small F1 regression on real fixtures depending on package-proximity bias |
| Phase 3d (confidence downgrade) | 0 | 0 | Stratification only |
| Phase 4 | 0 | 0 | Filter removal validates Phase 3 was complete |

If syn-oracle F1 drops more than 5pp at any phase: **stop, investigate, do not proceed**. The negative-fixture corpus disambiguates correctness from oracle-agreement, but the syn-oracle still represents real recall — a 5pp drop indicates the consolidation broke a legitimate emission path.

### Rollback criteria

- Phase 3 sub-PRs are independently revertable. If Phase 3b regresses, revert Phase 3b and continue with 3c (which may not depend on it).
- If multi-phase rollback needed: revert Phase 4 first, then Phase 3 sub-PRs in reverse order. Phase 1+2 can stay (they're a pure refactor).

## Risk register

| Risk | Likelihood | Mitigation |
|---|---|---|
| Receiver-type info incomplete (PR #149's coverage gaps) → Tier 2 misfires on real code | Medium | Phase 3a measures syn-oracle F1 against real fixtures BEFORE shipping. Cap impact at -5pp (rollback threshold). |
| Same-package proximity (Tier 4) over-prefers same-module candidates | High | Already a known issue per resolver-rule audit. Phase 3c's primary purpose is to TIGHTEN this, not loosen. |
| The discrimination ladder is the wrong abstraction (turns out we need a probabilistic resolver, not a fixed-tier one) | Low | Tier 5's confidence downgrade is the escape hatch — if many sites land there, that's the signal to revisit. |
| Phase 3a lands but doesn't help because PerFuncTypeMap doesn't cover enough sites | Medium | Phase 3a's success metric is per-fixture phantom_count, not aggregate F1. If phantoms don't drop, the type map is the bottleneck — fix that first. |

## Open questions

- **Tier 4 vs Tier 5 ordering:** is "prefer same-package" stronger than "no discrimination + downgrade"? Either ordering can be right depending on how strict the package-proximity bias is. Default: Tier 4 first; switch if real-fixture F1 regresses during Phase 3c.
- **LSP-resolved edges:** today these set `CandidateCount = 1` by definition (per `candidate_set.go`'s LSP comment). If LSP starts exposing alternates (would require C-side refactor), the discrimination ladder applies to them too — no design changes needed.
- **Cross-language consistency:** Go has its own resolver via LSP; Python uses bare-name registry resolution. The consolidation is Rust-targeted. Whether Python should adopt the same `CallContext` is open — Python's coverage gaps look different (FQN-format issues, decorator handling) and may warrant a separate plan.

## Success criteria

- All 4 negative fixtures' `phantom_count` reaches 0
- New positive controls (currently silently absorbed edges) start emitting; ground truth files updated to require them
- Real-fixture syn-oracle F1 within ±2pp of pre-consolidation baseline
- Symmetric filter at `resolveCallEdge` removed without regressing any baseline
- `resolver_rule` distribution shifts: less `cross-package-heuristic`, more `receiver-qualified` and `same-package-shadow`

## Out-of-scope companions

These follow-ups exist independently of this consolidation; track in `bench/accuracy/FOLLOWUPS.md`:

- **ACC-001** Constructor calls don't emit
- **ACC-002** Turbofish chain calls don't emit method-call edges
- **ACC-003** Cross-file qualified-path free-function calls don't emit
- **ACC-004** `use X as alias` rename imports don't resolve cross-file
- **ACC-005** Pre-existing CI infrastructure: `dtolnay/rust-toolchain` SHA unresolvable

ACC-002 in particular is worth scheduling **after** Phase 3a — once turbofish chains start emitting CALLS edges, expect a one-time jump in negative-fixture `phantom_count` as previously-uninspected chain calls become subject to bare-name resolution. That jump is signal of upstream improvement, not regression; re-baseline with audit doc.

## References

- Roundtable META_SYNTHESIS: `~/Documents/knowledge-base/research/dispatch-runs/2026-05-02-codegraph-roundtable/results/META_SYNTHESIS.md`
- Negative-fixture corpus: `bench/accuracy/synthetic/rust-{diesel,actix-data,futures-ready,restate-chain}-negative/`
- Audit docs: `bench/accuracy/baselines/2026-05-02-{diesel,actix-data,futures-ready,restate-chain}-negative-fixture-audit.md`
- Resolver source: `internal/pipeline/resolver.go` (467 LOC, 6 strategies)
- Resolver rule taxonomy: `internal/pipeline/resolver_rule.go`
- Candidate-set instrumentation: `internal/pipeline/candidate_set.go`
- Per-function type map: `internal/pipeline/typeinfer.go` (PR #149)
- Symmetric filter (to be removed in Phase 4): `internal/pipeline/pipeline.go::resolveCallEdge` (PR #150)
