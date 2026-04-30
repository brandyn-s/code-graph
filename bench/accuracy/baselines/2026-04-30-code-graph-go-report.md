# code-graph accuracy baseline — code-graph-go

- **Date**: 2026-04-30
- **Fixture SHA**: `82de04884ae995c21803557167c8f41bb4393847` (short: `82de048`)
- **Project name**: `c-Users-user-Documents-GitHub-code-graph`

## Summary

Four metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached).
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|
| CALLS | go-ast | 1752 / 4636 | 0.377 / 0.997 / 0.547 | 0.803 / 0.997 / 0.890 | 0.803 / 0.997 / 0.890 |
| IMPORTS | go-ast (dropped) | 0 / 111 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| store | 197 / 682 | 196 | 336 | 1 | 0.368 | 0.995 | **0.538** |
| cypher | 113 / 250 | 113 | 59 | 0 | 0.657 | 1.000 | **0.793** |
| pipeline | 627 / 1000 | 627 | 251 | 0 | 0.714 | 1.000 | **0.833** |
| tools | 386 / 568 | 381 | 135 | 5 | 0.738 | 0.987 | **0.845** |
| cbm | 429 / 2136 | 429 | 5 | 0 | 0.989 | 1.000 | **0.994** |

**Spread**: min F1 = 0.538, max F1 = 0.994, range = 0.457


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 952

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.ExtractFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.cbm_extract_file
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.ExtractFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.cbm_free_result
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_destroy
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_init
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp.go_lsp.cbm_run_go_lsp_cross
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.Execute --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.executePlan
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.Execute --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.maxRows
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.markTruncated
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.maxRows
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.evaluateCondition --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.compiledRegex
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  c-Users-user-Documents-GitHub-code-graph-internal-store.store.Store.Close --> c-Users-user-Documents-GitHub-code-graph-internal-store.config.ConfigStore.Close
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.errResult
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.getBoolArg
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.getStringArg
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.jsonResult
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.parseArgs
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.arena_grow
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_alloc
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_destroy
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_init
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_strdup
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.get_thread_parser
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.now_ns
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.extract_defs.cbm_extract_definitions
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.extract_imports.cbm_extract_imports
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.extract_unified.cbm_extract_unified
```

**Raw-exact false negatives**:
```
  c-Users-user-Documents-GitHub-code-graph-internal-store.store.Store.Close --> c-Users-user-Documents-GitHub-code-graph-internal-store.config.ConfigStore.Close
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.errResult
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.getBoolArg
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.getStringArg
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.jsonResult
  c-Users-user-Documents-GitHub-code-graph-internal-tools.index.Server.handleIndexRepository --> c-Users-user-Documents-GitHub-code-graph-internal-tools.tools.parseArgs
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).