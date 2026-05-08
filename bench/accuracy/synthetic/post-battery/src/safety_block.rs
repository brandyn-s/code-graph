// Exercises rationale extraction (find_rationale, kind=SAFETY).
//
// The extractor scans for `// SAFETY:` / `// WHY:` / `// HACK:` /
// `// NOTE:` / `// TODO:` / `// FIXME:` / `// IMPORTANT:` / `// XXX:`
// comment annotations and emits a Rationale node attached to the
// enclosing function (RATIONALE_FOR edge).

use std::ptr;

pub fn do_unsafe_thing() -> i32 {
    // SAFETY: the value 42 is statically allocated and lives for the
    // entire program; the raw pointer reconstructed here is always
    // valid for read. This rationale exists to be discoverable via
    // find_rationale(kind="SAFETY") — losing it would mean the
    // rationale extractor has regressed.
    let value = 42_i32;
    let raw: *const i32 = &value as *const i32;
    unsafe { ptr::read(raw) }
}
