// Internal Workflow type with method names that collide with the
// Restate SDK's `Context::run(...)` chain method.

pub struct Workflow {
    pub name: String,
}

impl Workflow {
    pub fn new(name: String) -> Self {
        Workflow { name }
    }

    // Bare-name target for `ctx.run(...)` on external Context.
    pub fn run(&self) -> i32 {
        42
    }

    // Adjacent method, also a Restate chain segment name.
    pub fn target(&self) -> &str {
        &self.name
    }
}
