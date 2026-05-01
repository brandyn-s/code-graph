# code-graph accuracy baseline — code-graph-go

- **Date**: 2026-05-01
- **Fixture SHA**: `4b4d9aeb86f6d32b61020a6ff25c92c785dcb6c8` (short: `4b4d9ae`)
- **Project name**: `c-Users-user-Documents-GitHub-code-graph`

## Summary

Four metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached).
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|
| CALLS | go-ast | 1824 / 4732 | 0.384 / 0.997 / 0.555 | 0.809 / 0.997 / 0.893 | 0.809 / 0.997 / 0.893 |
| IMPORTS | go-ast (dropped) | 0 / 110 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| store | 197 / 682 | 196 | 336 | 1 | 0.368 | 0.995 | **0.538** |
| cypher | 113 / 250 | 113 | 59 | 0 | 0.657 | 1.000 | **0.793** |
| pipeline | 699 / 1097 | 699 | 276 | 0 | 0.717 | 1.000 | **0.835** |
| tools | 386 / 568 | 381 | 135 | 5 | 0.738 | 0.987 | **0.845** |
| cbm | 429 / 2135 | 429 | 5 | 0 | 0.989 | 1.000 | **0.994** |

**Spread**: min F1 = 0.538, max F1 = 0.994, range = 0.457


## Caller-kind stratified precision

Each CALLS edge is tagged with the AST scope of its caller (`function-body`, `method-body`, `file-block`, `package-init-block`, `var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The harness reads this property and stratifies precision by it. The **ghost-caller FP rate** is the share of FPs whose caller is a package-level scope rather than a real function/method — alarms above 5%.

### CALLS

| Kind | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `function-body` | 236 | 12 | 0.952 | 248 |
| `method-body` | 579 | 416 | 0.582 | 995 |
| `test-body` | 1003 | 0 | 1.000 | 1003 |

**Package-block caller FP rate**: 0.0000 (0 of 428 FPs)

**Caller-kind complement legitimacy** (function/method-body share of all scope-aligned edges): 0.5520 (1243 of 2252)


## Janusian ambiguity stratified precision

Each CALLS edge carries the resolver's pre-tie-break candidate cardinality (`candidate_set_size`). A call site with >= 2 candidates is **Janusian** — the resolver picked among alternatives. Step 2's LLM-Judge taxonomy predicted `same_named_method_disambiguation` (60% of judged FPs) concentrates on Janusian sites; the precision split below tests that hypothesis on real-fixture data. LSP-resolved edges carry size=1 by definition (LSP returns one target without enumerating alternates), so the Janusian signal lives in the registry strategies.

### CALLS

**method_set_ambiguity_index** — share of call sites with >= 2 candidates:

| Project | Ambiguous sites | Total sites | Index |
|---|---:|---:|---:|
| cbm | 0 | 200 | 0.0000 |
| cypher | 0 | 80 | 0.0000 |
| pipeline | 5 | 389 | 0.0129 |
| store | 35 | 168 | 0.2083 |
| tools | 0 | 156 | 0.0000 |

**janusian_site_precision_split** — precision conditional on call-site ambiguity:

| Bucket | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `ambiguous` | 8 | 32 | 0.2000 | 40 |
| `unambiguous` | 1810 | 396 | 0.8205 | 2206 |

**janusian_precision_gap** (unambiguous − ambiguous precision): +0.6205. Positive = unambiguous sites resolve more accurately, consistent with Step 2's prediction. Negative or near-zero = ambiguity is not the dominant FP driver.


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 994

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