// Synthetic Rust fixture for the negative-benchmark gate (PR per
// 2026-05-02 roundtable Recommendation #1).
//
// Reproduces the Actix `web::Data<T>` deref-wrapper class. PR #149
// added recursive peeling of Arc/Rc/Box/Mutex/web::Data wrappers in
// receiver-type inference, so `data: web::Data<AppState>` should
// behave as `data: AppState` for chain resolution. This fixture
// exercises that specifically.
//
// Real-world pattern (Actix handlers, Axum extractors, Rocket state
// guards): a handler takes `data: web::Data<AppState>` and calls
// `data.record(value)` expecting it to resolve to `AppState::record`.
// If receiver-type inference doesn't peel `web::Data<AppState>` to
// `AppState`, the bare-name resolver may bind to whichever
// `MetricsCollector::record` or `Cache::record` was indexed first.
//
// Like all synthetic fixtures here, this is a tree-sitter input.
// It does NOT need to compile under `cargo build`.
//
// Structure (separation of caller QNs is load-bearing):
//
//   - `entry()`     ONLY calls methods on `data: web::Data<AppState>`.
//                   Any internal `*.record` / `*.flush` edge from
//                   `entry` to MetricsCollector or Cache is a phantom.
//                   Edges from `entry` to `AppState.*` are correct.
//   - `controls()`  issues the legitimate internal method calls on
//                   MetricsCollector + Cache directly.
//   - `metrics` and `cache` modules each define internal methods
//                   `record`, `flush` — the shadow targets.

use actix_web::web;

use rust_actix_data_negative::cache::Cache;
use rust_actix_data_negative::metrics::MetricsCollector;

pub struct AppState {
    counter: i64,
}

impl AppState {
    pub fn new() -> Self {
        AppState { counter: 0 }
    }

    // The legitimate target of `data.record(...)` in entry().
    pub fn record(&self, _value: i32) {}

    // The legitimate target of `data.flush()` in entry().
    pub fn flush(&self) {}
}

// Caller for web::Data chain calls ONLY. No MetricsCollector or Cache
// references appear here. Edges from `entry` to `metrics.*` / `cache.*`
// are phantoms by construction.
fn entry(data: web::Data<AppState>) {
    // Forbidden #1: `.record(...)` on `data: web::Data<AppState>`.
    // PR #149's deref peeler should treat data as AppState.
    // Phantom shape: `entry -> MetricsCollector.record` or `entry -> Cache.record`.
    data.record(1);

    // Forbidden #2: `.flush()` on the same wrapper-typed receiver.
    data.flush();

    // Same call pattern under explicit re-binding to test that
    // `let x = data.clone()` preserves the wrapper-peeled type
    // tracking, exercising chain resolution past one re-binding.
    let inner = data.clone();
    inner.record(2);
}

// Positive controls. Issues legitimate internal calls on
// MetricsCollector and Cache directly. These edges MUST emit; if
// they don't, the fixture isn't indexing.
fn controls() {
    let metrics = MetricsCollector::new();
    let cache = Cache::new();

    metrics.record(42);
    metrics.flush();
    cache.record("k");
    cache.flush();
    let _ = helper(7);
}

fn helper(n: i32) -> i32 {
    n + 1
}

fn main() {
    let state = AppState::new();
    let data: web::Data<AppState> = web::Data::new(state);
    entry(data);
    controls();
}
