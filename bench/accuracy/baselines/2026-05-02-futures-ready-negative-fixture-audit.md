# Futures-ready-negative fixture: phantom-edge audit

**Date:** 2026-05-02
**Fixture:** `bench/accuracy/synthetic/rust-futures-ready-negative/`
**Harness:** `bench/accuracy/check_negative_fixtures.py`

## Phantom emissions today

| from_qn | to_qn | edge_type | resolver_rule |
|---|---|---|---|
| `…src.main.entry` | `…src.signal.ready` | `CALLS` | `cross-package-heuristic` |

## Per-edge verification

### Edge 1: `entry → signal.ready`

**Source inspection of `entry()`** (`src/main.rs`):

```rust
use futures_util::future::ready;

fn entry() {
    let _f1 = ready(42);
    let _f2 = ready(0);
    let _f3 = ready(99);
}
```

**Resolution context:** `use futures_util::future::ready;` binds the bare name `ready` to the external `futures_util::future::ready` symbol. NO internal `ready` should be resolvable from `entry`'s scope under standard Rust name resolution.

**Internal candidates with bare name `ready`:**
- `state::ready(value: i32) -> bool` (declared in `src/state.rs`)
- `signal::ready(_signal: u8) -> bool` (declared in `src/signal.rs`)

**code-graph emitted:** `entry → signal.ready` via `cross-package-heuristic`. The bare-name resolver picked `signal::ready` (one of two candidates) and ignored the explicit `use` import binding to the external symbol.

Three call sites (`ready(42)`, `ready(0)`, `ready(99)`) all bind to the same target and dedupe to one edge.

**Verdict: REAL FAILURE.** Free-function bare-name shadow class. The resolver does not consult `use` imports when picking among bare-name candidates.

## Adjacent findings

### Finding D: cross-file free-function calls don't emit with qualified path

`controls()` originally used `state::ready(5)`, `signal::ready(0)`, `state::pending()` (qualified-path form). NONE of these emitted CALLS edges. Same with crate-prefixed `rust_futures_ready_negative::state::ready(5)`. Same with `use X as alias` rename imports.

The only edge `controls` emits is `controls → helper` (same-file). Cross-file free-function resolution is silently absent.

**Implication:** real-world Rust code using qualified-path imports is being under-counted in CALLS edges. This is a separate finding from the bare-name shadow phantom — both belong to the same architectural debt (Recommendation #2 consolidation), but they manifest as different metrics: phantom-emit (this fixture's signal) vs no-emit (silent under-counting).

### Class summary

This fixture catches the **third distinct phantom class**:

| Fixture | Class |
|---|---|
| `rust-diesel-negative` | Cross-file bare-name conflation across receiver types (method) |
| `rust-actix-data-negative` | Same-file shadow vs cross-file receiver-type method (method) |
| `rust-futures-ready-negative` | Bare-name free-function shadow ignoring `use` import (free fn) |

All three emit via `cross-package-heuristic`. The shared root cause is bare-name resolution that:
- Doesn't consult receiver-type for methods (Diesel)
- Doesn't prefer cross-file receiver-type over same-file shadow (Actix)
- Doesn't consult `use` imports for free functions (this)

## Failure pattern confirmation

The roundtable's prediction (GROK R2): "the four-path duplication of bare-name resolution is the load-bearing architectural debt." This third fixture closes the case. Three different fixtures, three different surface manifestations, ONE shared resolver path.

## Conclusion

- **Verdict:** REAL FAILURE (1/1).
- **Fix layer:** the system (resolver), not the harness.
- **Recommendation:** baseline pinned at 1 phantom. Open follow-up to investigate why `cross-package-heuristic` ignores `use` import bindings.
