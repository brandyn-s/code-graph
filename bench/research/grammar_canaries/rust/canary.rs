//! Canary fixture for the rust tree-sitter grammar.
//!
//! Exercises features the code-graph Rust extractor depends on:
//!   - free function + impl method
//!   - trait + impl
//!   - generics + lifetimes
//!   - macro invocation (println!)
//!   - turbofish
//!
//! If the AST shape changes, extraction quality changes — drift_check fires.

pub trait Handler {
    fn handle(&self, name: &str) -> Result<(), String>;
}

pub struct Service {
    pub name: String,
}

impl Handler for Service {
    fn handle(&self, name: &str) -> Result<(), String> {
        Err(format!("service {}: {}", self.name, name))
    }
}

pub fn dispatch_to<H: Handler>(h: &H, name: &str) -> Result<(), String> {
    h.handle(name)
}

pub fn parse_int(s: &str) -> Option<i32> {
    s.parse::<i32>().ok()
}

pub fn loud(name: &'static str) {
    println!("hello {}", name);
}

pub fn lifetimes_test<'a>(s: &'a str) -> &'a str {
    s
}
