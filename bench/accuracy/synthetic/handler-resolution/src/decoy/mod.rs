// Adversarial decoy: a function named `list_users` that lives in a
// DIFFERENT module than the axum router. Pre-D1, name-only resolution
// could end up here instead of the real handler. Post-D1's crate-
// locality bias prefers the same-module candidate over this one.

pub fn list_users() -> &'static str {
    "decoy — not the registered handler"
}
