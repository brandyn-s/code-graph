// Library root for rust-actix-data-negative. Two service modules
// each define methods (`record`, `flush`) whose bare names collide
// with what a developer might call on `data: web::Data<AppState>`.
// These are the shadow targets the resolver may incorrectly bind to.

pub mod metrics;
pub mod cache;
