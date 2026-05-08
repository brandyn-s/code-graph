// Exercises axum builder-style route extraction (PR #250).
//
// Router::new().route(PATH, METHOD(handler)) — the extractor reads
// the route literal + method + handler symbol and emits HANDLES edges
// from the route node to the handler function.

use axum::{
    routing::{get, post},
    Router,
};

pub fn build_router() -> Router {
    Router::new()
        .route("/api/users", get(list_users))
        .route("/api/users", post(create_user))
        .route("/api/users/:id", get(get_user))
}

async fn list_users() -> &'static str {
    "list"
}

async fn create_user() -> &'static str {
    "create"
}

async fn get_user() -> &'static str {
    "get"
}
