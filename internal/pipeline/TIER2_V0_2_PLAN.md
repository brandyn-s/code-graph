# Tier-2 v0.2 — Generic-bound receiver types

> **Status:** plan. v0.1 (`TIER2_V0_1_PLAN.md`) shipped via PR #341 +
> #346 + the syn-oracle external-chain awareness PRs #353/#354/#355.
> v0.2 implementation lands on a follow-up branch after this plan PR
> merges. Forward-looking sections live in `TIER2_RECEIVER_TYPE_DESIGN.md`
> "Gap class 2" and "v0.2".

## Goal

Capture trait bounds on generic parameters in `PerFuncTypeMap`. Extend
the chain walker to recognize generic-bound receivers and produce a new
`_generic.<traitQN>` sentinel that lets the resolver bind candidates
to a trait QN directly — eliminating the fuzzy fallback's confidence
hit on this class.

## Why now

The 2026-05-24 PSM Rust bucket-E source-inspection (16 FNs) showed:

- 8 REAL gaps (47% of remaining FNs after v0.1 oracle improvements)
- 2 of those are generic-bound receivers:
  - `JobRepository.add_job_events → T.to_json_value` — generic T in a
    trait-bounded fn body
  - `AssetUpdaterImpl.run_job → ImageBuildScheduler.submit_job` —
    restate trait dispatch on a Service trait-bounded receiver

These are real TPs that today survive via fuzzy resolution at low
confidence (`speculative-janusian` band). v0.2 promotes them to high-
confidence `type_dispatch_generic` emission. The win is in confidence
band (precision-friendly consumers can rely on these edges) rather
than raw F1, but it removes a documented class of "fuzzy luck" that
masks bugs when the lucky guess turns out to be wrong.

## Architecture

Three additive changes, no edge-type changes, no schema changes —
same shape as v0.1.

1. **Extractor change** (CBM tree-sitter): when a function signature
   has a generic parameter with a trait bound (`fn f<R: MyTrait>(...)`
   or `fn f<R>(arg: R) where R: MyTrait`), emit a `TypeAssign`
   record for the bound parameter with `TypeName="<traitQN>"` and a
   new field `BindingKind="generic_bound"`.

2. **Chain walker extension** (`resolveCallWithTypes`): when the
   chain root resolves to a generic-bound parameter, produce
   `chainReceiverType="_generic.<traitQN>"` instead of falling
   through to fuzzy.

3. **Resolver gate** (`ResolveCtx`): when `ReceiverType` starts with
   `_generic.`, filter candidates to those whose parent QN matches
   the trait. If exactly one candidate matches, emit with
   `Strategy=type_dispatch_generic`, `Confidence=0.85`.

## Tech Stack

- Go 1.26, tree-sitter Rust grammar (vendored, no changes)
- CBM C extractor — modify the `visit_item_fn` walker to capture
  generic parameter bounds
- No new dependencies

## Repo

`redacted-org/code-graph`. Implementation lands on a separate
`feat/tier2-v0.2-generic-bound` branch after this plan PR merges.

## Dependencies

- Builds on v0.1's `cargo_metadata.go` (no API change required)
- Builds on existing `PerFuncTypeMap` infrastructure
- Builds on existing `_external.<crate>` sentinel pattern (v0.2's
  `_generic.<traitQN>` mirrors the same shape)
- Builds on existing `closure_typemap.go` (closures already bind
  receiver types — v0.2 extends similar logic to generic bounds)

## Regression gate

- `bench/accuracy/compare.py psm-rust` must
  show assetman scope-aligned F1 ≥ 0.90 (currently 0.902 post-v0.1
  + oracle improvements). No regression on canstatd/calibd/adsbd/apid.
- New unit tests in `internal/pipeline/tier2_generic_test.go`:
  - generic-bound dispatch resolves to single trait candidate
  - generic-bound dispatch drops on 0 candidates (trait not in graph)
  - generic-bound dispatch is ambiguous on 2+ candidates (different
    impls of same trait)
- Existing tests in `tier2_external_test.go` (from v0.1) must still
  pass — no behavior change for external-chain drops.

## Out of scope

- Gap class 3 (chained method call landing on internal type) — that's
  v0.3 (`TIER2_V0_3_PLAN.md`). Covers `progress.current_stage()`,
  `request.revision.tag_branch()`, `p.is_telem_capable()` shapes.
- Gap class 4 (legitimately ambiguous) — not Tier-2-shaped.
- `where` clauses with trait bounds on associated types — v0.2 covers
  parameter bounds only; associated-type bounds are rarer and need
  separate analysis.
- Multi-bound generics (`R: Trait1 + Trait2`) — v0.2 captures the
  first trait bound; multi-bound is a v0.2.1 follow-up.

---

## Task 1: CBM extractor — emit BindingKind=generic_bound

**Finding:** The CBM C extractor's `visit_item_fn` walker today
emits `TypeAssign` records for typed parameters with concrete types
(`arg: SomeStruct`). Generic-bound parameters (`R: Trait`) are
currently skipped — the walker sees the binding but doesn't have a
record shape to emit it under.

**Files:**
- Modify: `internal/cbm/c/extract.c` (or equivalent) — add a
  `BindingKind` field to the `TypeAssign` record output
- Modify: `internal/pipeline/typeinfer.go` — add `BindingKind` to the
  Go `TypeAssign` struct + JSON unmarshaling
- New: `internal/pipeline/testdata/rust-generic-bound-fixture/` —
  synthetic test crate with a generic-bound function

**Step 1: Write failing test**

```go
func TestExtractor_GenericBoundEmitsTypeAssign(t *testing.T) {
    src := `
        trait Greeter { fn greet(&self) -> String; }
        struct EnHello;
        impl Greeter for EnHello { fn greet(&self) -> String { "hi".into() } }
        fn use_greeter<G: Greeter>(g: &G) -> String { g.greet() }
    `
    extracted := extractWithCBM(t, src)
    assigns := assignsForFunc(extracted, "use_greeter")
    require.Len(t, assigns, 1)
    assert.Equal(t, "g", assigns[0].VarName)
    assert.Equal(t, "Greeter", assigns[0].TypeName)
    assert.Equal(t, "generic_bound", assigns[0].BindingKind)
}
```

**Step 2: Run test → fails (extractor doesn't emit on generic_bound)**

**Step 3: Implement extractor change**

In the C extractor, when visiting a function signature:
- For each parameter, walk the parameter's type expression
- If the type is a single-segment ident that matches a generic
  parameter name (collected from the `<...>` clause), look up the
  bound for that param
- If a bound exists and resolves to a known trait name, emit a
  `TypeAssign` with `BindingKind="generic_bound"` and
  `TypeName="<traitQN-resolved-via-import-map>"`

**Step 4: Re-run test → passes**

**Step 5: Lint + format**

## Task 2: Chain walker — produce _generic.<traitQN> sentinel

**Files:**
- Modify: `internal/pipeline/pipeline.go::resolveCallWithTypes`

**Step 1: Write failing test**

```go
func TestChainWalker_GenericBoundProducesSentinel(t *testing.T) {
    // Pipeline state: PerFuncTypeMap["use_greeter"]["g"] has
    // TypeName="proj.Greeter" BindingKind="generic_bound"
    p := newPipelineFixture(t, /* ... */ )
    p.perFuncTypeMap.Set("use_greeter", "g", "proj.Greeter", "generic_bound")
    receiverType := p.resolveChainReceiver(
        "use_greeter",
        []string{"g"},  // chain: just `g`
    )
    assert.Equal(t, "_generic.proj.Greeter", receiverType)
}
```

**Step 2: Run test → fails (no _generic. handling)**

**Step 3: Implement walker change**

When the chain walker resolves the root segment via
`PerFuncTypeMap.Get(callerQN, segmentName)`:
- Check `BindingKind`; if `"generic_bound"`, return
  `chainReceiverType = "_generic." + TypeName` immediately (skip
  the further chain walking — trait dispatch is final).

**Step 4: Re-run test → passes**

## Task 3: Resolver gate — _generic.<traitQN> filter

**Files:**
- Modify: `internal/pipeline/resolver.go::ResolveCtx`
- New: `internal/pipeline/tier2_generic_test.go`

**Step 1: Write failing tests**

```go
func TestResolver_GenericBoundSingleCandidate(t *testing.T) {
    r := NewFunctionRegistry()
    r.Register("Greeter", "proj.Greeter", "Trait")
    r.Register("greet", "proj.Greeter.greet", "Method")
    ctx := CallContext{
        CalleeName:   "greet",
        ReceiverType: "_generic.proj.Greeter",
    }
    result := r.ResolveCtx(ctx)
    assert.Equal(t, "proj.Greeter.greet", result.QualifiedName)
    assert.Equal(t, "type_dispatch_generic", result.Strategy)
    assert.GreaterOrEqual(t, result.Confidence, 0.85)
}

func TestResolver_GenericBoundZeroCandidatesDrops(t *testing.T) {
    r := NewFunctionRegistry()
    // Trait not in graph
    ctx := CallContext{
        CalleeName:   "greet",
        ReceiverType: "_generic.proj.Greeter",
    }
    result := r.ResolveCtx(ctx)
    assert.Empty(t, result.QualifiedName)
}

func TestResolver_GenericBoundAmbiguousDrops(t *testing.T) {
    r := NewFunctionRegistry()
    r.Register("Greeter", "proj.Greeter", "Trait")
    r.Register("greet", "proj.Greeter.greet", "Method")
    r.Register("Greeter", "other.Greeter", "Trait")  // collision
    r.Register("greet", "other.Greeter.greet", "Method")
    ctx := CallContext{
        CalleeName:   "greet",
        ReceiverType: "_generic.proj.Greeter",
    }
    result := r.ResolveCtx(ctx)
    // Ambiguous — multiple traits with same simple name. Drop.
    assert.Empty(t, result.QualifiedName)
}
```

**Step 2: Run tests → fail (no _generic. branch)**

**Step 3: Implement resolver gate**

In `ResolveCtx`, before the fuzzy fallback:

```go
if strings.HasPrefix(ctx.ReceiverType, "_generic.") {
    traitQN := strings.TrimPrefix(ctx.ReceiverType, "_generic.")
    candidates := []string{}
    for _, candQN := range r.byName[ctx.CalleeName] {
        // Parent QN of the candidate
        if idx := strings.LastIndex(candQN, "."); idx > 0 {
            parentQN := candQN[:idx]
            if parentQN == traitQN {
                candidates = append(candidates, candQN)
            }
        }
    }
    if len(candidates) == 1 {
        return ResolutionResult{
            QualifiedName:  candidates[0],
            Strategy:       "type_dispatch_generic",
            Confidence:     0.85,
            CandidateCount: 1,
        }
    }
    // 0 or >1: drop, don't fall through to fuzzy
    return ResolutionResult{}
}
```

**Step 4: Re-run tests → pass**

## Task 4: Real-fixture regression check

**Step 1:** With Tasks 1-3 implemented, run PSM Rust reindex +
compare.py.

**Step 2:** Verify:
- assetman scope-aligned F1 ≥ 0.90 (no regression)
- The 2 generic-bound FNs (`T.to_json_value` and `submit_job`) move
  from FN to TP (or at minimum, get emitted with
  `Strategy=type_dispatch_generic` confidence_band `high`)

**Step 3:** If F1 regresses on any subset, the bound-emission rate
is too high (capturing non-bound cases) or the registry trait-QN
matching is too loose. Bisect by toggling Task 1 emission off; rerun
to confirm Task 1 is the cause.

## Task 5: Document the gate

**Files:**
- Modify: `CLAUDE.md` — add `_generic.<traitQN>` to the chain-receiver
  sentinel reference table
- Modify: `TIER2_RECEIVER_TYPE_DESIGN.md` — mark v0.2 as shipped,
  reference the new test files

## Out-of-scope (deferred to v0.3+)

- Multi-bound generics: when `<R: Trait1 + Trait2>`, only the FIRST
  trait is captured. Add multi-bound support if PSM has >5 examples.
- `where` clauses on associated types: `where R::Item: SomeTrait` is
  not parsed by v0.2.
- Higher-kinded type bounds (`<F: Fn(...)>`): v0.2 treats Fn as a
  regular trait; this may produce incorrect dispatches on closures.

These deferrals are intentional — v0.2 covers the dominant generic-
bound pattern observed on PSM. The deferred cases need fixture
evidence before justifying the extractor complexity.

## Expected timeline

Per the design doc's "v0.2 — ~2 weeks" estimate. Task 1 (extractor)
is the biggest unknown — C extractor changes have historically been
where unforeseen complexity lives.

## Falsifier

If implementation completes and PSM Rust assetman F1 doesn't improve
(stays at 0.902 or regresses), the hypothesis "generic-bound receiver
types account for the 2 documented bucket-E FNs" is false. Two
possible failure modes:

1. The extractor emits but the chain walker doesn't use the binding
   (Task 2 bug). Debug: slog in walker showing whether the binding
   was looked up.

2. The resolver gate is too strict (Task 3 bug). Debug: slog showing
   the trait-QN match attempts and why no candidate matched.

The slog tooling for diagnosing this is already in place from the
`RESOLVER_STATIC_DISPATCH_DEBUG` work (PR #353). Pattern-match.
