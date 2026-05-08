// rust-reqwest synthetic fixture root.
//
// Layered into two pseudo-services so matchAndLink's sameService
// filter (strip-2 directory comparison) classes them as cross-service:
//   - src/server/  — axum route handlers
//   - src/callers/ — reqwest URL call sites (3 shapes)

pub mod callers;
pub mod server;
