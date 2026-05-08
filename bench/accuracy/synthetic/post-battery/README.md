# post-battery synthetic fixture

Regression-protects the 12-item psm (PSM) test
battery shipped April-May 2026. The fixture is small (4 Rust modules
+ 1 TS file) but exercises every pattern the battery covered, so a
PR that silently regresses any of them fails this gate before it
ships to PSM users.

## What gets regression-protected

| Pattern | Module | Floor | Provenance |
|---|---|---|---|
| HTTP_CALLS edge emission | `web/fetch.tsx` cross-resolved to `src/axum_routes.rs` | ≥ 2 | PRs #251 (SVG filter), #246 (filesystem-path FP), pre-existing fetch+route URL match |
| HANDLES edge emission | `src/axum_routes.rs` | ≥ 3 | PR #250 (axum builder routes) |
| IMPLEMENTS edge emission | `src/impl_trait.rs` | ≥ 2 | Existing Rust extractor for `impl Trait for Struct` happy path |
| SAFETY rationale extraction | `src/safety_block.rs` | ≥ 1 | Existing rationale extractor (kind=SAFETY) |

The harness is `bench/accuracy/check_post_battery.py`. Run via
`make bench-post-battery`.

## What is NOT yet protected

- **reqwest URL extraction** (Phase C1): the `src/reqwest_calls.rs`
  module exists but currently emits zero HTTP_CALLS — the existing
  extractor doesn't fire on the `Client::new().get(...).send()`
  chained pattern. C1 will close that gap. When C1 lands, raise
  `expected_http_calls_min` from 2 to 6 in `ground_truth.json`.
- **TS/JSX fetch direct classification** (Phase C2): `web/fetch.tsx`
  already emits HTTP_CALLS via path cross-resolution to the axum
  router, not via direct fetch-call classification. C2 makes the
  classification direct; the floor stays the same but the
  resolution path changes.
- **Generic-handler resolution** (Phase D1): doesn't apply on this
  fixture (no router/main name collision); covered by a separate
  `handler-resolution` adversarial fixture in D1.

## When to update the floors

Raise a floor in `ground_truth.json` IN THE SAME PR that ships the
new capability. The harness will keep passing automatically — but
without the floor raise, a future regression that drops the new
capability back to the old level won't be detected.

Lowering a floor is always intentional: surface it in the PR
description with explanation.

## How to add a new pattern

1. Add the module file in `src/` (Rust) or `web/` (TS/JSX).
2. Add a `mod NAME;` line to `src/lib.rs` and `src/main.rs` if Rust.
3. Add a new `expected_*_min` field in `ground_truth.json`.
4. Add a new check entry in `check_post_battery.py` matching the
   pattern's edge type or rationale kind.
5. Run `make bench-post-battery` and verify the new check passes.
