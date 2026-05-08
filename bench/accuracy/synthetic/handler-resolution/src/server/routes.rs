// axum router that registers the *server* module's list_users.

use axum::{routing::get, Router};

pub fn build_router() -> Router {
    Router::new().route("/api/users", get(super::list_users))
}
