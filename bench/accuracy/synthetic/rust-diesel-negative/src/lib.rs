// Library root for rust-diesel-negative. Splits internal modules into
// repo + service so each gets its own `get_result` / `load` / `execute`
// method — that's the shadow target for the Diesel chain methods in
// main.rs.
//
// IMPORTANT: the methods here are unrelated to Diesel. They're named
// to MATCH the chain method names because that's the real-world
// pattern that triggers the co-hallucination (assetman has both a
// db/pg_lock.rs that chains `.get_result` AND a reload/restate/
// asset_introspect.rs that defines `get_result` on a service trait).

pub mod repo;
pub mod service;
