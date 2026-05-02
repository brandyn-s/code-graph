// Internal Invocation type with method names that collide with the
// Restate SDK's `ctx.invocation().target().send()` chain.

pub struct Invocation;

impl Invocation {
    pub fn new() -> Self {
        Invocation
    }

    pub fn target(&self) -> &str {
        "target"
    }

    pub fn send(&self) -> i32 {
        0
    }
}
