// Internal metrics collector with method names that may collide with
// methods called via `data: web::Data<AppState>` chains in main.rs.

pub struct MetricsCollector;

impl MetricsCollector {
    pub fn new() -> Self {
        MetricsCollector
    }

    // Bare-name target for `data.record(...)` where data:web::Data<AppState>.
    // The legitimate target should be AppState.record (declared in main.rs);
    // an emit pointing here is a phantom.
    pub fn record(&self, _value: i32) {}

    // Same pattern for `flush`.
    pub fn flush(&self) {}
}
