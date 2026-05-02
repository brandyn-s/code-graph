// Library root for rust-restate-chain-negative. Two internal types
// (Workflow, Invocation) define methods whose bare names collide
// with Restate SDK chain methods (`run`, `invocation`, `target`,
// `send`). These are the shadow targets the resolver may bind to
// when an `entry()` chain call is made on an external `Context`.

pub mod workflow;
pub mod invocation;
