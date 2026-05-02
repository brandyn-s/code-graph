# Actix-data-negative fixture: phantom-edge audit

**Date:** 2026-05-02
**Fixture:** `bench/accuracy/synthetic/rust-actix-data-negative/`
**Harness:** `bench/accuracy/check_negative_fixtures.py`
**Per:** `rules/verify-instrument-before-fix.md`

## Phantom emissions today

Code-graph emits **2 phantom CALLS edges**:

| from_qn | to_qn | edge_type | resolver_rule |
|---|---|---|---|
| `…src.main.controls` | `…src.main.AppState.record` | `CALLS` | `cross-package-heuristic` |
| `…src.main.controls` | `…src.main.AppState.flush` | `CALLS` | `cross-package-heuristic` |

## Per-edge verification

### Edge 1: `controls → AppState.record`

**Source inspection of `controls()`** (`src/main.rs`):

```rust
fn controls() {
    let metrics = MetricsCollector::new();
    let cache = Cache::new();
    metrics.record(42);   // ← receiver: MetricsCollector. Phantom binds to AppState.record.
    metrics.flush();
    cache.record("k");
    cache.flush();
    let _ = helper(7);
}
```

**Receivers in scope:** `metrics: MetricsCollector` (from `lib.rs::metrics::MetricsCollector`), `cache: Cache` (from `lib.rs::cache::Cache`). NO `AppState` binding exists in `controls()`. AppState is only referenced from `entry()` and `main()`.

**Legitimate target:** `metrics.record(42)` should resolve to `rust_actix_data_negative::metrics::MetricsCollector::record` and `cache.record("k")` should resolve to `rust_actix_data_negative::cache::Cache::record`.

**code-graph emitted:** `controls → AppState.record` via `cross-package-heuristic`. The same-file shadow (AppState lives in main.rs alongside `controls`) won the bare-name race against the cross-file receiver-type-correct candidates (MetricsCollector / Cache).

**Verdict: REAL FAILURE.** Same-file shadow class.

### Edge 2: `controls → AppState.flush`

Same analysis as Edge 1. `metrics.flush()` and `cache.flush()` both bind to `AppState.flush` instead of their receiver-type-correct targets.

**Verdict: REAL FAILURE.**

## Adjacent positive findings (PR #149 deref peeling works)

`entry(data: web::Data<AppState>)` makes two chain calls:

```rust
data.record(1);
data.flush();
```

These resolve to `entry → AppState.record` (cross-package-heuristic) and `entry → AppState.flush` (interface-dispatch). The `web::Data<AppState>` wrapper is correctly peeled to `AppState` per PR #149's deref-wrapper logic.

Forbidden patterns from `entry` to MetricsCollector / Cache do NOT fire today — the wrapper-peeling protects against the cross-package phantom that would have appeared without PR #149. These forbidden patterns are kept as **regression prevention**: if the deref-peeler regresses, the gate trips immediately.

## Failure pattern confirmation

The roundtable's load-bearing pattern: bare-name resolution spread across 4 paths, no receiver-type discrimination.

This fixture exposes a NEW SAME-FILE variant of that pattern. The Diesel fixture exposed cross-FILE bare-name conflation; this fixture exposes SAME-FILE shadow dominance. Both implicate the same `cross-package-heuristic` resolver path (despite the name — the rule fires on within-file bare-name matches too).

## Adjacent finding to track

**`cross-package-heuristic` fires on same-file edges.** The rule name suggests it's for cross-package resolution, but `controls → AppState.record` (same file) was emitted via this rule. Either the rule name is misleading or the rule's matcher is too aggressive. Capture as follow-up — informs Recommendation #2 consolidation.

## Conclusion

- **Verdict:** REAL FAILURES (2/2). Not instrument artifacts.
- **Fix layer:** the system (resolver), not the harness.
- **New phantom class identified:** same-file shadow dominance. Distinct from the Diesel cross-file conflation but same architectural root.
- **Recommendation:** pin baseline at 2 phantoms. Open follow-up to investigate why `cross-package-heuristic` fires on same-file edges.
