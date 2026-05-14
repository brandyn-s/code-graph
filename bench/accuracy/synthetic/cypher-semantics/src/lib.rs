//! Cypher-semantics fixture.
//!
//! Exercises the Cypher engine's array-property operators surfaced by
//! PR #308 (CONTAINS-on-array element-of semantics). Each fixture
//! function is hand-authored to land specific values in array-typed
//! node properties (`decorators`, `param_types`, `return_types`,
//! `decorator_tags`) so `bench/accuracy/check_cypher_semantics.py`
//! can pin those operators against regression.
//!
//! This is NOT a code-correctness fixture (no shipping code). It is a
//! query-engine surface fixture. If the extractor changes shape (e.g.,
//! decorator strings start arriving normalized), the fixture's
//! ground-truth expectations need to update in lockstep.

/// Test function with `#[test]` decorator (element exact match).
#[test]
fn test_one() {
    let _x: String = "value".to_string();
    let _y: u32 = 42;
}

/// Second test — used to assert `count(f) >= 3` on
/// `decorators CONTAINS '#[test]'`.
#[test]
fn test_two() {
    let _x: String = "other".to_string();
}

/// Third test — exists so the floor is reachable even if the
/// extractor changes how unit-test-with-no-args functions are
/// represented.
#[test]
fn test_three() {
    assert!(true);
}

/// Feature-gated function. Used to assert
/// `decorators CONTAINS '#[cfg(feature = "experimental")]'` succeeds
/// when given the exact element.
#[cfg(feature = "experimental")]
pub fn gated_function() {
    let _v: Vec<String> = vec!["a".to_string()];
}

/// Function with a `String` parameter type. Exercises `param_types
/// CONTAINS 'String'` element-of semantics.
pub fn take_string(s: String) {
    let _ = s;
}

/// Function returning `Vec<String>`. Exercises `return_types CONTAINS
/// 'Vec'` element-of semantics (Rust extractor stores the outer type
/// constructor name as the element).
pub fn produce_vec() -> Vec<String> {
    vec!["a".to_string(), "b".to_string()]
}

/// Function returning `Result<u32, String>`. The Rust extractor's
/// `return_types` for this function is `["Result"]`. Exercises the
/// same element-of operator on a different element.
pub fn produce_result() -> Result<u32, String> {
    Ok(0)
}
