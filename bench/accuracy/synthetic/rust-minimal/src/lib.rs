// Sibling module that main.rs imports from. Internal-only edges.

pub mod helpers {
    pub fn greet(name: &str) {
        inner_greet(name);  // CALLS: rust-minimal.src.lib.helpers.greet -> inner_greet
    }

    fn inner_greet(_name: &str) {}
}

pub mod math {
    pub fn square(n: i32) -> i32 {
        n * n
    }
}
