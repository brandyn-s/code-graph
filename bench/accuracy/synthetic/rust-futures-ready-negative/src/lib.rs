// Library root for rust-futures-ready-negative. Two internal modules
// (state, signal) each define a free function `ready` whose name
// collides with futures_util::future::ready and the futures_util::ready!
// macro extraction call site. These are the shadow targets for any
// `ready(...)` call in main.rs.

pub mod state;
pub mod signal;
