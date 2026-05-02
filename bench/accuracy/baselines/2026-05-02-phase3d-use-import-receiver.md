# Phase 3d — Function-scoped use-import receiver

**Date:** 2026-05-02
**PR:** feat/registry-resolve-use-import-receiver-phase3d
**Plan:** [`bench/research/registry-resolve-consolidation-plan.md`](../../research/registry-resolve-consolidation-plan.md)

## What changed

Adds Rust **function-scoped `use_declaration`** handling in
`internal/cbm/extract_type_assigns.c::handle_type_assigns`. When the
extractor encounters a `use foo::bar::Baz;` inside a function body, it
emits a `CBMTypeAssign{var_name="Baz", type_name="Baz",
enclosing_func_qn="<fn-qn>"}`. The pipeline then resolves "Baz" via the
registry's byName index — internal types resolve to their full QN; the
fallback to Phase 3c's raw recording handles external crate types.

This closes the last gap in the negative-fixture corpus.

## Why parse_rust_imports didn't catch this

`parse_rust_imports` walks `ctx->root` (file scope) for `use_declaration`
nodes. Function-scoped uses live INSIDE function bodies and are
invisible to that walker. The Diesel fixture's `entry()` has:

```rust
fn entry(conn: &mut PgConnection) {
    use schema::users::dsl::users;  // function-scoped — invisible to file walker

    let _n = users.execute(conn).unwrap_or(0);  // phantom: bound to AssetRepo.execute
    ...
}
```

`handle_type_assigns` already walks function bodies (parameters, let
bindings, self_parameter, field_declaration). Adding `use_declaration`
piggybacks on that walker — the existing `state->enclosing_func_qn`
context tells us the enclosing function, and we just emit a TypeAssign.

## Restrictions (intentional, for now)

- **Single-path uses only**. `use foo::bar::Baz;` works.
- **Skips `use foo::{a, b}` (use_list)** — needs a recursive descent
  into each subitem.
- **Skips `use foo::*` (glob)** — no specific binding.
- **Skips `use foo as alias` (rename)** — needs to split on " as ".

The simple-path case is enough to close the negative-fixture corpus.
Elaborate forms can be added when the corpus surfaces them — flag
those in followups.

## Phantom corpus impact

| Fixture | Pre-Phase-3d | Post-Phase-3d | Δ | Notes |
|---|---|---|---|---|
| rust-actix-data-negative | 0 | 0 | 0 | Already 0 from Phase 3a. |
| rust-diesel-negative | 1 | **0** | **-1** | `users.execute(conn)` bound to local Struct → Tier 2 drops. |
| rust-futures-ready-negative | 0 | 0 | 0 | Already 0 from Phase 3b. |
| rust-restate-chain-negative | 0 | 0 | 0 | Already 0 from Phase 3c. |
| **TOTAL** | **1** | **0** | **-1** | **Full corpus eliminated.** |

Positive controls: 6/6 in Diesel still emit (`controls → AssetRepo.{get_result, load, execute}`, `IntrospectService.get_result`, `helper`).

## Risk surface

The change adds TypeAssigns for function-scoped uses. Risks:

- **Internal type collision**: `use foo::Bar;` where `Bar` is also a
  unique name in the registry → resolves correctly via byName.
- **Internal name collision**: `use foo::Bar;` where `Bar` is shared
  across multiple modules (e.g., several crates each have a struct
  named `Bar`) → byName picks one, Tier 2 may drop legitimate edges.
  Risk is bounded — the same collision exists for parameter type
  ascriptions today.
- **External crate types** (e.g., `use std::collections::HashMap`):
  resolveAsClass fails → Phase 3c records raw "HashMap" → if the user
  later does `let m = HashMap::new(); m.insert(...)`, Tier 2 drops
  bindings (correct: external).
- **`use_list` / glob / `as`** forms are silently skipped. No false
  positives there; just no extra signal.

Hard rollback at >5pp real-fixture syn-oracle F1 drop. Phases 3a-3c
remain in place if 3d is reverted.

## Real-fixture validation

**Not measured this PR.** All package tests pass
(`go test ./internal/pipeline/...` → ok 27.4s). Real-fixture
syn-oracle F1 owed across Phase 3b/3c/3d combined.

## Updated baselines

```json
{
  "rust-actix-data-negative": {"phantom_count": 0},
  "rust-diesel-negative": {"phantom_count": 0},
  "rust-futures-ready-negative": {"phantom_count": 0},
  "rust-restate-chain-negative": {"phantom_count": 0}
}
```

## Phase progression

| Phase | Status | Phantoms |
|---|---|---|
| Phase 1 (#156) | merged | 7 (no-op) |
| Phase 2 (#157) | merged | 7 (no-op) |
| Phase 3a (#158) | merged | 5 |
| Phase 3b (#159) | merged | 4 |
| Phase 3c (#160) | merged | 1 |
| **Phase 3d** | **THIS PR** | **0** (full corpus eliminated) |
| Phase 4 | next | Tier 5 confidence downgrade + remove PR #150's symmetric filter (now dead code) |

## Open follow-ups

- **`use_list` form**: `use foo::{a, b, c};` needs descent into the
  use_list subnodes; each binding emits a TypeAssign. Not blocking
  the negative fixtures but easy follow-up.
- **`as` rename form**: `use foo::Bar as MyBar;` — split on " as " and
  use the right-side name as `var_name`, left-side path's last segment
  as `type_name`.
- **Real-fixture syn-oracle F1 measurement** across Phases 3b/3c/3d
  combined — owed before Phase 4.
- **Phase 4** can now remove PR #150's symmetric filter (dead code now
  that Tier 2/3 + external + use-import all drop phantoms upstream).
