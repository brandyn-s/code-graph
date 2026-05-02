// Synthetic Rust fixture for the negative-benchmark gate (PR per
// 2026-05-02 roundtable Recommendation #1).
//
// Reproduces the Restate SDK multi-segment chain class. Restate's
// workflow API uses chains like:
//
//   ctx.run("op", || async { ... }).await?
//   ctx.invocation().target().send().await?
//
// Each chain segment is a method call on a different intermediate
// type (Context → RunBuilder → Result; Context → InvocationBuilder
// → TargetBuilder → SendFuture). PR #149 added a multi-segment
// chain resolver to follow these. This fixture tests that the
// resolver doesn't bind chain segments to internal methods of the
// same bare name.
//
// Like all synthetic fixtures here, this is a tree-sitter input.
//
// Structure (separation of caller QNs is load-bearing):
//
//   - `entry(ctx)`  ONLY makes Restate chain calls on the external
//                   Context. Any internal Workflow.run / Invocation.*
//                   edge from `entry` is a phantom by construction.
//   - `controls()`  issues the legitimate internal method calls on
//                   Workflow + Invocation directly.
//   - `workflow` and `invocation` modules define the shadow targets.

use restate_sdk::context::Context;

use rust_restate_chain_negative::invocation::Invocation;
use rust_restate_chain_negative::workflow::Workflow;

// Caller for Restate chain calls ONLY. No Workflow.run or
// Invocation.* references appear here. Any edge from `entry` to
// those internals is a phantom by construction.
fn entry(ctx: Context) {
    // Forbidden #1: chain `ctx.run(...)`. External — must NOT bind
    // to Workflow.run.
    let _r = ctx.run("op", || async { 42 });

    // Forbidden #2: chain `ctx.invocation()`. External — must NOT
    // bind to Invocation::new (constructor — already known not to
    // emit) or any internal `invocation` method.
    let _i = ctx.invocation();

    // Forbidden #3-5: multi-segment chain `ctx.invocation().target().send()`.
    // .target and .send are external methods on the Restate
    // intermediate builder types. They share bare names with
    // Invocation.target and Invocation.send.
    let _result = ctx.invocation().target().send();
}

// Positive controls. Issues the LEGITIMATE internal method calls
// on Workflow and Invocation directly. These edges MUST emit; if
// they don't, the fixture isn't indexing.
fn controls() {
    let w = Workflow::new("setup".to_string());
    let i = Invocation::new();

    let _r = w.run();
    let _t1 = w.target();
    let _t2 = i.target();
    let _s = i.send();
    let _h = helper(7);
}

fn helper(n: i32) -> i32 {
    n + 1
}

fn main() {
    let ctx: Context = unsafe { std::mem::zeroed() };
    entry(ctx);
    controls();
}
