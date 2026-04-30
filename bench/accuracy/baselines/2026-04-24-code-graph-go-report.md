# code-graph accuracy baseline — code-graph-go

- **Date**: 2026-04-24
- **Fixture SHA**: `aca3ae14093166948403e76a831a94e41b9e9ee5` (short: `aca3ae1`)
- **Project name**: `c-Users-user-Documents-GitHub-code-graph`

## Summary

Four metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached).
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|
| CALLS | go-ast | 1678 / 4065 | 0.280 / 0.679 / 0.397 | 0.586 / 0.679 / 0.629 | 0.586 / 0.679 / 0.629 |
| IMPORTS | go-ast (dropped) | 0 / 102 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| store | 196 / 503 | 130 | 299 | 66 | 0.303 | 0.663 | **0.416** |
| cypher | 113 / 121 | 73 | 41 | 40 | 0.640 | 0.646 | **0.643** |
| pipeline | 575 / 1003 | 397 | 379 | 178 | 0.512 | 0.690 | **0.588** |
| tools | 365 / 300 | 111 | 74 | 254 | 0.600 | 0.304 | **0.404** |
| cbm | 429 / 2138 | 429 | 12 | 0 | 0.973 | 1.000 | **0.986** |

**Spread**: min F1 = 0.404, max F1 = 0.986, range = 0.583


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 909

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.ExtractFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.cbm_extract_file
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.ExtractFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.cbm_free_result
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.ExtractFile --> fmt.Errorf
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm_test.TestGoFunctionExtraction --> fmt.Printf
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm_test.TestJSArrowFunction --> fmt.Printf
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm_test.TestPythonDocstring --> fmt.Printf
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.DefsToLSPDefs --> strings.Join
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.DefsToLSPDefs --> strings.TrimLeft
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_destroy
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_init
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.Execute --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.parser.Parse
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.Execute --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.planner.BuildPlan
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.applyLimit
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.buildColumnNames
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.buildGroups
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.resolveOrderColumn
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.sortRows
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.splitAggregateItems
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.evaluateCondition --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.compareNumeric
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.evaluateCondition --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.getEdgeProperty
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
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.Execute --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.parser.Parse
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.Execute --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.planner.BuildPlan
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.applyLimit
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.buildColumnNames
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.buildGroups
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.resolveOrderColumn
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.sortRows
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.aggregateResults --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.splitAggregateItems
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.evaluateCondition --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.compareNumeric
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.Executor.evaluateCondition --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.executor.getEdgeProperty
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).