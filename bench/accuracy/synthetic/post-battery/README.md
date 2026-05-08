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

- **reqwest URL extraction**: even after Phase C1 (PR #255+,
  format!() macros + const URLs), the `src/reqwest_calls.rs` module
  still emits zero HTTP_CALLS edges in this fixture because all Rust
  files live under `<project>.src.*`. matchAndLink's `sameService`
  filter strips the last 2 segments of the qualified-name path; with
  callers and routes both ending up at `<project>.src`, every pair
  is classified intra-service and the edge is dropped. The dedicated
  `bench/accuracy/synthetic/rust-reqwest/` fixture (added with C1)
  splits the call sites into `src/callers/` vs `src/server/` so the
  strip-2 dirs differ — that's where the 3 reqwest URL shapes are
  validated end-to-end. To raise this fixture's HTTP_CALLS floor,
  restructure post-battery the same way (separate
  `src/callers/reqwest_calls.rs` from `src/server/axum_routes.rs`).
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
