// Synthetic Rust fixture for the negative-benchmark gate (PR per
// 2026-05-02 roundtable Recommendation #1).
//
// Reproduces the Diesel-chain co-hallucination class observed on
// psm/assetman: external trait-method calls
// (`.get_result`, `.load`, `.execute`) chained on a query builder
// share bare names with INTERNAL methods defined elsewhere in the
// codebase (asset_introspect.rs:33,118; pg_lock.rs:40,58,76).
//
// code-graph's bare-name resolution paths (suffix-match + fuzzy
// fallback) shadow-bind the external chain method to whichever
// internal `Foo::get_result` was indexed first. This fixture exists
// to detect that and to gate any future change to those paths.
//
// Like all synthetic fixtures here, this is a tree-sitter input.
// It does NOT need to compile under `cargo build` — Diesel is an
// external crate referenced by `use` only. code-graph's resolver
// must handle the chain calls without a local definition; that's
// the actual real-world condition we're testing.
//
// Structure (separation of caller QNs is load-bearing — if `entry`
// also called `repo.get_result()` legitimately, code-graph would
// dedupe the legit edge and the phantom into one and we couldn't
// tell them apart):
//
//   - `entry()`     ONLY issues Diesel-style chains. Any internal
//                   `*.get_result` / `*.load` / `*.execute` edge
//                   from `entry` is a phantom by construction.
//   - `controls()`  issues the legitimate internal method calls.
//                   Positive-control caller; verifies fixture indexes.
//   - `repo` and `service` modules each define internal methods
//                   `get_result`, `load`, `execute` — the shadow
//                   targets the bare-name resolver may incorrectly
//                   bind to.

use diesel::prelude::*;
use diesel::pg::PgConnection;

use rust_diesel_negative::repo::AssetRepo;
use rust_diesel_negative::service::IntrospectService;

mod schema {
    // Diesel macro output — code-graph won't see expanded form
    // but tree-sitter parses the path syntax fine.
    pub mod users {
        pub mod dsl {
            pub struct users;
        }
    }
}

#[derive(Default)]
struct User;

#[derive(Default)]
struct LockResult;

// Caller for Diesel chain calls ONLY. No legitimate internal
// `get_result` / `load` / `execute` calls from this function.
fn entry(conn: &mut PgConnection) {
    use schema::users::dsl::users;

    // Forbidden #1: chain `.get_result` (Diesel RunQueryDsl trait method).
    // External — must not bind to AssetRepo.get_result or IntrospectService.get_result.
    let _u: User = users
        .filter(())
        .get_result::<User>(conn)
        .unwrap_or_default();

    // Forbidden #2: chain `.load` (Diesel RunQueryDsl trait method).
    let _list: Vec<User> = users
        .filter(())
        .load::<User>(conn)
        .unwrap_or_default();

    // Forbidden #3: chain `.execute` (Diesel RunQueryDsl trait method).
    let _n = users.execute(conn).unwrap_or(0);

    // Forbidden #4: nested chain — exercises the multi-segment
    // chain resolver added in PR #149.
    let _l: LockResult = users
        .filter(())
        .filter(())
        .get_result::<LockResult>(conn)
        .unwrap_or_default();
}

// Positive controls. Issues the LEGITIMATE internal method calls.
// These edges MUST emit; if they don't, the fixture isn't indexing.
fn controls() {
    let repo = AssetRepo::new();
    let service = IntrospectService::new();

    let _ok = repo.get_result();
    let _list = repo.load();
    let _n = repo.execute(0);
    let _stuff = service.get_result();
    let _h = helper(7);
}

fn helper(n: i32) -> i32 {
    n + 1
}

fn main() {
    // Stand-in connection — fixture doesn't actually run; the chain
    // calls in `entry` need a `conn` argument for tree-sitter to
    // produce the expected method-call AST.
    let conn: &mut PgConnection = unsafe { std::mem::zeroed() };
    entry(conn);
    controls();
}
