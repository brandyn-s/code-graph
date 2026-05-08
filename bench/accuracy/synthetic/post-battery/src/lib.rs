// Post-battery synthetic fixture library root.
//
// Exposes the four pattern modules so a bench harness can index this
// crate as a real Rust target rather than a script-style binary.

pub mod axum_routes;
pub mod impl_trait;
pub mod reqwest_calls;
pub mod safety_block;
