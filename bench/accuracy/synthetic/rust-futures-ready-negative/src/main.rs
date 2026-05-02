// Synthetic Rust fixture for the negative-benchmark gate (PR per
// 2026-05-02 roundtable Recommendation #1).
//
// Reproduces the futures_util::ready free-function bare-name shadow
// class. futures_util provides `ready(value)` (creates a ready
// future). When project source defines free-function `ready` in any
// module, bare-name resolution may bind `ready(...)` calls in
// unrelated functions to that internal definition, producing
// phantom edges to functions that have nothing to do with the
// futures-related call.
//
// Free-function bare names (`ready`, `init`, `process`, `start`,
// `run`, `setup`) collide heavily in real Rust codebases.
//
// Like all synthetic fixtures here, this is a tree-sitter input.
//
// Structure (separation of caller QNs is load-bearing):
//
//   - `entry()`     uses BARE-NAME `ready(...)` after
//                   `use futures_util::future::ready;`. Any internal
//                   state.ready or signal.ready edge from `entry` is
//                   a phantom by construction — the import binds the
//                   bare name to the external symbol.
//   - `controls()`  uses BARE-NAME `ready(...)` calls but with
//                   different `use` imports — should bind to the
//                   imported internal `ready`.
//   - `state` and `signal` modules each define free `ready`.

// External re-export. Bare-name `ready` in `entry()` should resolve
// to this external symbol, NOT to state::ready or signal::ready.
use futures_util::future::ready;

fn entry() {
    // Forbidden #1: bare-name `ready(42)` with futures_util import in
    // scope. MUST NOT bind to state::ready or signal::ready.
    let _f1 = ready(42);

    // Forbidden #2: bare-name `ready(0)` — same shape, different arg.
    let _f2 = ready(0);

    // Forbidden #3: bare-name `ready(99)` — third call site.
    let _f3 = ready(99);
}

// Positive controls. Uses crate-prefixed module-path forms so
// resolution is unambiguous and the resolver has no excuse to bind
// to the wrong module. (Aliased imports `use X as Y` are not
// reliably resolved cross-file by code-graph today.)
fn controls() {
    let _r1 = rust_futures_ready_negative::state::ready(5);
    let _r2 = rust_futures_ready_negative::signal::ready(0);
    let _p = rust_futures_ready_negative::state::pending();
    let _h = helper(7);
}

fn helper(n: i32) -> i32 {
    n + 1
}

fn main() {
    entry();
    controls();
}
