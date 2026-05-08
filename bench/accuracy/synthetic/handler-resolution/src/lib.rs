// handler-resolution synthetic fixture root.
//
// Adversarial fixture for Phase D1 handler resolution. Two modules
// (`server` and `decoy`) BOTH define a function named `list_users`.
// Only the `server` one is the real handler — registered on the axum
// router. The `decoy` module's `list_users` is unused glue code that
// happens to share the name.
//
// Pre-D1: matchAndLink resolved every fetch->router edge to the
// route-declaring function (`build_router`). The handler-name
// collision was invisible.
// Post-D1: matchAndLink walks rh.HandlerRef + crate-locality. The
// `server::list_users` candidate has the longer common QN-prefix with
// the route-declaring function (`server::build_router`), so it wins
// over `decoy::list_users`. The fixture pins this preference.

pub mod client;
pub mod decoy;
pub mod server;
