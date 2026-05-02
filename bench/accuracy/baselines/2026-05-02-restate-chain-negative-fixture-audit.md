# Restate-chain-negative fixture: phantom-edge audit

**Date:** 2026-05-02
**Fixture:** `bench/accuracy/synthetic/rust-restate-chain-negative/`
**Harness:** `bench/accuracy/check_negative_fixtures.py`

## Phantom emissions today

| from_qn | to_qn | edge_type | resolver_rule |
|---|---|---|---|
| `…src.main.entry` | `…src.workflow.Workflow.run` | `CALLS` | `cross-package-heuristic` |
| `…src.main.entry` | `…src.invocation.Invocation.target` | `CALLS` | `cross-package-heuristic` |
| `…src.main.entry` | `…src.invocation.Invocation.send` | `CALLS` | `cross-package-heuristic` |

## Per-edge verification

### Edge 1: `entry → Workflow.run`

**Source inspection of `entry()`** (`src/main.rs`):

```rust
fn entry(ctx: Context) {
    let _r = ctx.run("op", || async { 42 });   // ← single-segment chain phantom
    let _i = ctx.invocation();
    let _result = ctx.invocation().target().send();   // multi-segment chain
}
```

**Receiver of `.run(...)`:** `ctx: Context` (Restate SDK external type, imported via `use restate_sdk::context::Context`).

**Internal candidates with bare name `run`:** only `Workflow::run` (declared in `src/workflow.rs`).

**code-graph emitted:** `entry → Workflow.run` via `cross-package-heuristic`. The resolver bound the external Restate `Context::run` chain method to the internal `Workflow::run`.

**Verdict: REAL FAILURE.** Single-segment chain bare-name shadow.

### Edge 2: `entry → Invocation.target`

**Source:** `ctx.invocation().target().send()` — multi-segment chain. The `.target()` call is on the intermediate builder type returned by `ctx.invocation()` (external).

**code-graph emitted:** `entry → Invocation.target` via `cross-package-heuristic`. Two internal candidates exist (`Workflow.target` and `Invocation.target`); the resolver picked `Invocation`.

**Verdict: REAL FAILURE.** Multi-segment chain bare-name shadow with same-name conflation.

### Edge 3: `entry → Invocation.send`

**Source:** Same multi-segment chain, terminal segment.

**code-graph emitted:** `entry → Invocation.send` via `cross-package-heuristic`. Single internal candidate (`Invocation.send`); the resolver bound to it.

**Verdict: REAL FAILURE.**

## Adjacent finding: positive-control conflation (same as Diesel)

`controls()` calls `w.target()` (where `w: Workflow`) AND `i.target()` (where `i: Invocation`). The emitted edge set shows ONLY `controls → Invocation.target`. The `controls → Workflow.target` edge is silently absorbed.

This is the SAME pattern documented in `2026-05-02-diesel-negative-fixture-audit.md` Finding A: bare-name resolution conflates same-named methods across receiver types. The Diesel finding said this would persist until receiver-type discrimination is added to `cross-package-heuristic`. Restate confirms it.

## Why the 4th forbidden pattern doesn't fire

`forbidden_emitted_calls` lists `entry → Workflow.target` as a pattern. It doesn't fire because `Workflow.target` and `Invocation.target` share a bare name; the resolver picked `Invocation.target` for `entry`'s chain segment (Edge 2 above). The `Workflow.target` shadow target lost the conflation race.

If the conflation is fixed (each call site binds to its receiver-type-correct method), expect `entry → Workflow.target` to appear at SOME chain segment if the multi-segment resolver still doesn't peel through external builder types. This is a layered fix: receiver-type discrimination + chain-segment external-type recognition.

## Class summary (full corpus, 4 fixtures)

| Fixture | Phantom class | Receiver | Resolver path | Phantoms |
|---|---|---|---|---|
| `rust-diesel-negative` | Cross-file bare-name conflation across receiver types | method | cross-package-heuristic | 1 |
| `rust-actix-data-negative` | Same-file shadow vs cross-file receiver-type method | method | cross-package-heuristic | 2 |
| `rust-futures-ready-negative` | Bare-name free-fn shadow ignoring `use` import | free fn | cross-package-heuristic | 1 |
| `rust-restate-chain-negative` | Multi-segment chain bare-name shadow | method (chain) | cross-package-heuristic | 3 |

**Total: 7 phantoms across 4 fixtures, ALL via `cross-package-heuristic`.**

The Restate fixture is the most prolific because multi-segment chains create multiple binding opportunities per call site. Each `.method()` segment is independently subject to bare-name shadowing.

## Conclusion

- **Verdict:** REAL FAILURES (3/3).
- **Fix layer:** the system (resolver), not the harness.
- **Multi-segment chain class confirmed:** distinct from single-segment bare-name shadow only in the number of binding opportunities; same architectural root.
- **Recommendation:** baseline at 3 phantoms. Recommendation #2's `registry.Resolve` consolidation must address all four classes simultaneously.
