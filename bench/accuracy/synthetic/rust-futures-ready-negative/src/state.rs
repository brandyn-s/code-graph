// Internal `ready` shadow target #1. Real-world pattern: a state
// module providing a readiness check by the same bare name.

pub fn ready(value: i32) -> bool {
    value > 0
}

pub fn pending() -> bool {
    false
}
