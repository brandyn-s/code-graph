# Phase 3b — Import-binding discrimination

**Date:** 2026-05-02
**PR:** feat/registry-resolve-import-binding-phase3b
**Plan:** [`bench/research/registry-resolve-consolidation-plan.md`](../../research/registry-resolve-consolidation-plan.md)

## What changed

Adds Tier 3 of the discrimination ladder for **free-function calls** where the callee bare-name is brought into scope via a `use` / `import` statement. Free-function calls are the tier Phase 3a explicitly skipped.

Mechanic: `applyImportBindingFilter`. When `ctx.ImportBindings[calleeName]` is set:
1. **Internal match** — narrow candidates to those whose QN ends with the import target. Suffix-match handles project-prefix differences (`utils.fetch_data` matches `<project>.utils.fetch_data`).
2. **External (no match anywhere)** — drop all candidates entirely. The call is going outside the project; emitting any internal binding would be a phantom.
3. **Pass through** — when the import target IS registered somewhere but didn't surface in the current candidate set, leave the candidate set unchanged. Avoids dropping legitimate edges when the target is internal but the candidate-finding logic missed it.

## Adjacent fix: `bareNameOfImport`

Imports come from the C extractor with full paths in `LocalName`:
- Rust: `LocalName="futures_util::future::ready"` (full `::` path)
- Python: `LocalName="fetch_data"` (bare alias, already)

`bareNameOfImport` strips the leading path and returns just the last segment so `ctx.ImportBindings` is keyed by the bare name the resolver looks up.

## Adjacent fix: `FuzzyResolve` call site routed through `FuzzyResolveCtx`

`pipeline_cbm.go:462` previously called the legacy `FuzzyResolve(calleeName, moduleQN, importMap)` signature, which forwards to `FuzzyResolveCtx` with empty `ReceiverType` and `ImportBindings`. Tier 2 and Tier 3 silently bypassed the fuzzy fallback.

Routed through `FuzzyResolveCtx` with full context (receiver type from typeMap, import bindings from p.importBindings). Without this, the futures-ready phantom persisted because the fuzzy path re-emitted what the upstream paths correctly dropped.

## Phantom corpus impact

| Fixture | Pre-Phase-3b | Post-Phase-3b | Δ | Notes |
|---|---|---|---|---|
| rust-actix-data-negative | 0 | 0 | 0 | Already 0 from Phase 3a. |
| rust-diesel-negative | 1 | 1 | 0 | `users` from `use schema::users::dsl::users;` IS a use-import — but the chain call `users.execute(conn)` has receiver `users`, not free-function shape. Tier 3 doesn't fire on method calls. Diesel needs a different mechanism (use-imports as receiver-type bindings). |
| rust-futures-ready-negative | 1 | **0** | **-1** | Tier 3 fired correctly. |
| rust-restate-chain-negative | 3 | 3 | 0 | External `Context` receiver — needs Phase 3c or external-type-name passthrough. |
| **TOTAL** | **5** | **4** | **-1** | |

## Real-fixture validation

Hard rollback criterion: syn-oracle F1 drop > 5pp. **Not measured in this PR.** TestPythonCrossModuleCallViaImport caught one regression early (suffix-match needed for project-prefix); after the fix, all package tests pass.

If future real-fixture measurement reveals > 5pp drop:
- **Likely cause:** `bareNameOfImport` produces a bare name that collides with an in-project symbol, and Tier 3 incorrectly drops legitimate internal candidates because the import target's full path doesn't end with any registered QN.
- **Mitigation:** tighten the "pass through if registered anywhere" check OR scope Tier 3 to fixtures where the import target prefix is a known external crate.

## Updated baselines

```json
{
  "rust-actix-data-negative": {"phantom_count": 0},
  "rust-diesel-negative": {"phantom_count": 1},
  "rust-futures-ready-negative": {"phantom_count": 0},
  "rust-restate-chain-negative": {"phantom_count": 3}
}
```

## Phase progression

| Phase | Status | Phantoms |
|---|---|---|
| Phase 1 (#156) | merged | 7 (no-op) |
| Phase 2 (#157) | merged | 7 (no-op) |
| Phase 3a (#158) | merged | 5 (Actix dropped) |
| **Phase 3b** | **THIS PR** | **4** (futures-ready dropped) |
| Phase 3c | next | (Tier 4 same-package proximity OR external-type passthrough for Restate/Diesel) |

## Open follow-ups

- **External Context receiver** (Restate): when `ctx: Context` is a parameter and `Context` isn't a registered class, store a marker in the type map so Tier 2 can drop bindings on calls through `ctx`. Targets the 3-phantom Restate fixture.
- **Use-import receivers** (Diesel): generalize Tier 3 to handle use-imports as receiver-type substitutes when a method call's receiver name matches an `import` binding pointing externally.
- **Real-fixture syn-oracle F1** measurement before Phase 3c.
