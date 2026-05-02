// Internal `ready` shadow target #2. Bare-name resolver picks ONE
// of state.ready / signal.ready when given just `ready`; we want
// neither to be picked for `ready(42)` called with futures_util in
// scope.

pub fn ready(_signal: u8) -> bool {
    true
}
