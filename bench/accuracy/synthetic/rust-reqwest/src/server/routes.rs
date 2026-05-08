// axum routes that match each reqwest caller's URL path.

use axum::{routing::get, Router};

pub fn build_router() -> Router {
    Router::new()
        .route("/api/orders", get(handle_orders))
        .route("/api/users", get(handle_users))
        .route("/api/items", get(handle_items))
}

async fn handle_orders() -> &'static str {
    "orders"
}

async fn handle_users() -> &'static str {
    "users"
}

async fn handle_items() -> &'static str {
    "items"
}
