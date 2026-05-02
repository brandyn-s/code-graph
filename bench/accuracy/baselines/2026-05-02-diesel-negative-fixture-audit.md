# Diesel-negative fixture: phantom-edge audit

**Date:** 2026-05-02
**Fixture:** `bench/accuracy/synthetic/rust-diesel-negative/`
**Harness:** `bench/accuracy/check_negative_fixtures.py`
**Per:** `rules/verify-instrument-before-fix.md` — verify failure-cell edges before designing a fix.

## Phantom emissions today

Code-graph emits **1 phantom CALLS edge** against the Diesel-chain
fixture's `forbidden_emitted_calls` patterns:

| from_qn | to_qn | edge_type | resolver_rule |
|---|---|---|---|
| `…src.main.entry` | `…src.repo.AssetRepo.execute` | `CALLS` | `cross-package-heuristic` |

## Per-edge verification (B1)

### Edge 1: `entry → AssetRepo.execute`

**Source inspection of `entry()`** (`src/main.rs:71-91`):

```rust
fn entry(conn: &mut PgConnection) {
    use schema::users::dsl::users;
    let _u: User = users.filter(()).get_result::<User>(conn).unwrap_or_default();
    let _list: Vec<User> = users.filter(()).load::<User>(conn).unwrap_or_default();
    let _n = users.execute(conn).unwrap_or(0);  // ← the call site producing the phantom
    let _l: LockResult = users.filter(()).filter(()).get_result::<LockResult>(conn).unwrap_or_default();
}
```

**Receiver of `.execute(conn)`:** `users` — bound earlier in scope to
`schema::users::dsl::users` (a struct from a `mod schema { … }`
declaration with no method definitions). There is NO local `execute`
method on `users`, NO blanket impl in scope, and `AssetRepo` is in a
DIFFERENT module (`rust_diesel_negative::repo`) used only by
`controls()`. The legitimate target for `users.execute(conn)` is
Diesel's `RunQueryDsl::execute` — external trait method on an
external crate.

**code-graph emitted:** `entry → AssetRepo.execute` via
`cross-package-heuristic`. Bare-name suffix-match found
`AssetRepo.execute` (defined in `src/repo.rs`) and bound to it,
ignoring receiver-type information that distinguishes the `users`
binding from the `repo: AssetRepo` binding in the unrelated
`controls()` function.

**Verdict: REAL FAILURE.** The source is unambiguous. The resolver's
`cross-package-heuristic` path is binding by bare name without
receiver-type discrimination. This is the co-hallucination class the
2026-05-02 roundtable Recommendation #1 prescribes negative fixtures
to detect.

## Adjacent findings discovered while building this fixture

These are NOT phantoms (the harness doesn't flag them today) but
they are visible in the indexed graph and clarify the resolver's
behavior. Captured here for the Phase D fixture work and for
future `registry.Resolve` consolidation (Recommendation #2).

### Finding A: Bare-name conflation across receiver types

`controls()` makes two bare-name calls to `get_result`:

```rust
let _ok = repo.get_result();      // repo: AssetRepo
let _stuff = service.get_result();  // service: IntrospectService
```

Code-graph emits **only** `controls → AssetRepo.get_result`. The
`controls → IntrospectService.get_result` edge is silently absorbed.
The cross-package-heuristic picks ONE candidate by bare name and
binds both call sites to it.

**Implication:** centralizing bare-name resolution in `registry.Resolve`
(roundtable Rec #2) needs to pass receiver-type context, otherwise
this absorption persists.

### Finding B: Turbofish chain calls don't emit method-call edges

Three of `entry()`'s four chain calls use turbofish:

```rust
.get_result::<User>(conn)         // turbofish ::<User>
.load::<User>(conn)               // turbofish ::<User>
.get_result::<LockResult>(conn)   // turbofish ::<LockResult>
.execute(conn)                    // simple form ← only this emitted
```

Only the simple form (`.execute(conn)`) produced a CALLS edge.
The turbofish forms appear absent from the emitted edge set. This
explains why `entry → *.get_result` and `entry → *.load` phantoms
don't fire today even though the same suffix-match path that
mishandled `.execute` would presumably mishandle them.

**Implication:** the negative-fixture corpus undercounts phantoms
by however many real-world chain calls use turbofish (most do,
since type inference on chained generics often requires it).
Fixing the simple-form phantom fixes the visible tip; the
turbofish gap is a separate parser/pipeline issue. Capture as
follow-up — do not let it block the Phase C baseline.

### Finding C: Constructor calls (`Foo::new()`) don't emit CALLS edges

`controls()` calls `AssetRepo::new()` and `IntrospectService::new()`.
Neither edge appears in the indexed graph. Constructor-style
associated functions are routed differently than instance method
calls.

**Implication:** any future fixture that relies on counting
`*::new()` calls as positive controls will under-emit. Use
instance method calls only.

## Failure pattern confirmation (B2)

The roundtable's GROK-flagged architectural debt: "monolithic
`registry.Resolve` is the single load-bearing architectural debt —
bare-name shadowing logic spread across 4 paths."

The phantom's `resolver_rule` is `cross-package-heuristic`. That is
one of the 4 paths the PR #150 symmetric filter attempted to
constrain (and caught only 12/369 phantom emissions, indicating
the same logic is duplicated across the other 3 paths).

The fixture exercises:
- The `cross-package-heuristic` path directly (Edge 1)
- The bare-name conflation across receiver types (Finding A)

Both implicate the same load-bearing pattern. **Confirmed: this
fixture exercises the load-bearing failure pattern Recommendation #1
prescribes.**

## Sample size note

`verify-instrument-before-fix.md` calls for 3-5 sampled edges. This
fixture produces only 1 phantom today (because of Finding B: the
turbofish chain forms aren't parsed as method calls). The sample
size is dictated by the pipeline's emission rate, not the fixture's
intent. Once Finding B is addressed (turbofish parsing fix or
pipeline change), the same fixture should produce ≥4 phantoms and
provide the full sample. For Phase C, baseline at 1 and gate
against regression; new phantoms from upstream fixes will REQUIRE
re-baselining (which is the desired flow — every phantom that
appears reflects a real new failure mode worth reviewing).

## Conclusion

- **Verdict:** REAL FAILURE (1/1). Not an instrument artifact.
- **Fix layer:** the system (resolver), not the harness.
- **Recommendation:** proceed to Phase C. Pin baseline at 1 phantom.
- **Open follow-ups** (capture in a separate issue):
  - Finding B: investigate turbofish chain-call parsing
  - Finding C: constructor-call edge emission
  - Finding A: receiver-type discrimination in `cross-package-heuristic`
    — this is the load-bearing fix that Recommendation #2's
    `registry.Resolve` consolidation should address
