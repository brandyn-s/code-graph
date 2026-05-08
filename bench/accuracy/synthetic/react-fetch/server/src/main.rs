// axum routes that match each fetch caller's URL path.
//
// Three routes — one each for the three fetch shapes the web/
// fixture exercises (literal, template-literal-with-prefix,
// template-literal-with-id-slot).

use axum::{routing::get, Router};

#[tokio::main]
async fn main() {
    let _ = build_router();
}

pub fn build_router() -> Router {
    Router::new()
        .route("/api/items", get(handle_items))
        .route("/api/orders", get(handle_orders))
        .route("/api/users", get(handle_users_collection))
}

async fn handle_items() -> &'static str {
    "items"
}

async fn handle_orders() -> &'static str {
    "orders"
}

async fn handle_users_collection() -> &'static str {
    "users"
}
