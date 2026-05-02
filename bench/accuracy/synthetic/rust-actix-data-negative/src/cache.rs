// Second internal type with colliding method names — second shadow
// target. Bare-name resolver picks ONE of MetricsCollector / Cache
// when given just `record` or `flush`; we want neither to be picked
// for `data.record()` where data is web::Data<AppState>.

pub struct Cache;

impl Cache {
    pub fn new() -> Self {
        Cache
    }

    pub fn record(&self, _key: &str) {}

    pub fn flush(&self) {}
}
