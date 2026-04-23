// Synthetic Rust fixture for oracle prove-the-instrument gate.
// Every CALLS and IMPORTS edge is hand-enumerated in ground_truth.json.
// If the oracle's output diverges, the oracle has a bug.

use rust_minimal::helpers::greet;
use rust_minimal::math::square;

fn entry() {
    let v = square(4);  // CALLS: rust-minimal.src.main.entry -> square
    greet("world");      // CALLS: rust-minimal.src.main.entry -> greet
    leaf(v);             // CALLS: rust-minimal.src.main.entry -> leaf
}

fn leaf(n: i32) -> i32 {
    helper(n)            // CALLS: rust-minimal.src.main.leaf -> helper
}

fn helper(n: i32) -> i32 {
    n + 1
}

fn main() {
    entry();             // CALLS: rust-minimal.src.main.main -> entry
}
