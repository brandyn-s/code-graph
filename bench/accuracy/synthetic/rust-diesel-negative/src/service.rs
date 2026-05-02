// Second internal type with colliding method names — mirrors the
// redacted assetman pattern where IntrospectService and pg_lock.rs
// both have `get_result` in scope.

pub struct IntrospectService;

impl IntrospectService {
    pub fn new() -> Self {
        IntrospectService
    }

    // Same name as Diesel's RunQueryDsl::get_result. Unrelated.
    pub fn get_result(&self) -> String {
        "introspect".to_string()
    }
}
