# Phase 3c — External-type-receiver passthrough

**Date:** 2026-05-02
**PR:** feat/registry-resolve-external-receiver-phase3c
**Plan:** [`bench/research/registry-resolve-consolidation-plan.md`](../../research/registry-resolve-consolidation-plan.md)

## What changed

Extends `inferTypesCBM` so that when a parameter / let-binding's declared
type does NOT resolve to a registered internal class, the raw type name
is recorded in the per-function TypeMap **instead of dropping the
binding**. This unlocks the existing Tier 2 (`applyReceiverTypeFilter`)
mechanic for external receivers: when `ReceiverType="Context"` (external)
and no internal Method has parent=="Context", `dropAll=true` fires and
the call is not bound to a same-named internal Method via cross-package-
heuristic.

Single-line change in spirit: replace `if classQN == "" { continue }`
with "record raw `typeName` instead of dropping."

## Why the previous tier ladder didn't fire

The Restate fixture's `entry(ctx: Context)` is a function parameter
where `Context` lives in the external `restate_sdk` crate. The chain:

1. C extractor emits `TypeAssign{var_name="ctx", type_name="Context"}`.
2. `resolveAsClass("Context", ...)` → `""` (Context isn't registered).
3. `inferTypesCBM` `continue`s — binding dropped, never reaches `out`.
4. At call site, `typeMap["ctx"]` doesn't exist → `receiverType=""`.
5. `applyReceiverTypeFilter` short-circuits on empty `ReceiverType`.
6. Resolver falls to `suffix_match` → finds `Workflow.run` /
   `Invocation.target` / `Invocation.send` → emits 3 phantoms.

The Tier 2 mechanic itself was correct; it was starved of the input.

## Phantom corpus impact

| Fixture | Pre-Phase-3c | Post-Phase-3c | Δ | Notes |
|---|---|---|---|---|
| rust-actix-data-negative | 0 | 0 | 0 | Already 0 from Phase 3a. |
| rust-diesel-negative | 1 | 1 | 0 | `users` from `use schema::users::dsl::users;` — value-not-type binding. Phase 3d's use-import-as-receiver mechanism targets this. |
| rust-futures-ready-negative | 0 | 0 | 0 | Already 0 from Phase 3b. |
| rust-restate-chain-negative | 3 | **0** | **-3** | `ctx: Context` parameter recorded raw; Tier 2 drops chain bindings. |
| **TOTAL** | **4** | **1** | **-3** | |

Positive controls: 4/4 still emit (`controls → Workflow.run`,
`Invocation.target`, `Invocation.send`, `helper`).

## Risk surface

The new behavior records raw type names for ANY type whose label isn't in
`resolveAsClass`'s allowed set (Class, Type, Interface, Enum). The
remaining cases are:

- **External crate types** (Context, anyhow Error, tokio Mutex<T>, ...)
  — correctly drop bindings. **Net win**: phantoms eliminated.
- **Trait receivers** (`fn f(t: SomeTrait)`) — Trait isn't registered.
  Bindings on `t.method()` were previously suffix-matched (often randomly
  to some internal Method). Phase 3c drops them. **Likely net win** for
  precision; could cost recall if the trait method genuinely IS
  implemented by the suffix-matched type.
- **Generic types** (`Vec<T>`, `Box<dyn Trait>`) — C extractor already
  strips outer generics and peels deref-wrappers. Most reach
  `resolveAsClass` as the inner concrete type. The remainder (e.g.,
  `Vec` itself) record raw and drop bindings on `vec.push()` calls
  (correct: external stdlib).

Hard rollback criterion: syn-oracle F1 drop > 5pp on real fixtures.

## Real-fixture validation

**Not measured in this PR.** All package tests pass
(`go test ./internal/pipeline/...` → ok 32.5s). If real-fixture
measurement reveals > 5pp drop:

- **Likely cause:** Trait receivers losing recall. The fix would be
  scoping Phase 3c to specific signal types (e.g., only when typeName
  matches a known external-crate prefix) — but that requires a crate
  registry we don't have today.
- **Mitigation:** revert Phase 3c. Phase 3a + 3b's gains (Actix +
  futures-ready) remain.

## Updated baselines

```json
{
  "rust-actix-data-negative": {"phantom_count": 0},
  "rust-diesel-negative": {"phantom_count": 1},
  "rust-futures-ready-negative": {"phantom_count": 0},
  "rust-restate-chain-negative": {"phantom_count": 0}
}
```

## Phase progression

| Phase | Status | Phantoms |
|---|---|---|
| Phase 1 (#156) | merged | 7 (no-op) |
| Phase 2 (#157) | merged | 7 (no-op) |
| Phase 3a (#158) | merged | 5 (Actix dropped) |
| Phase 3b (#159) | merged | 4 (futures-ready dropped) |
| **Phase 3c** | **THIS PR** | **1** (Restate dropped — all 3 chain phantoms) |
| Phase 3d | next | use-import-as-receiver for Diesel (last phantom) |
| Phase 4 | later | Tier 5 confidence downgrade + remove PR #150's symmetric filter (now dead code) |

## Open follow-ups

- **Use-import receivers** (Diesel): `users` from
  `use schema::users::dsl::users;` is a value-not-type binding. Tier 3's
  `ImportBindings` map has it, but Tier 3 only fires for free-function
  calls; `users.execute(conn)` is a method call. Generalize Tier 3 to
  treat use-imports as receiver-type substitutes when a method call's
  receiver name matches an `ImportBindings` entry pointing externally.
- **Real-fixture syn-oracle F1 measurement** before Phase 3d (still owed
  from Phase 3b).
