# code-graph accuracy baseline — code-graph-go

- **Date**: 2026-08-12
- **Fixture SHA**: `1dab656f84135f8f6a448eb3066598c3f31f3fb5` (short: `1dab656`)
- **Project name**: `Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812`

## Summary

Five metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned (all bands)**: restricted to edges whose caller is in the oracle's analyzed-caller set, INCLUDING speculative-janusian band emissions (Janusian-ambiguous cross-package edges). Reflects the recall-friendly operating point — what consumers see by default if they don't filter `confidence_band`.
- **Scope-aligned (high-confidence)**: same scope, EXCLUDING `confidence_band=speculative-janusian` edges. Reflects the precision-friendly operating point for consumers who filter to high-trust edges only.
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

Two operating points are reported because the resolver emits Janusian-ambiguous edges (multiple cross-package candidates with same simple name) at the speculative-janusian band rather than dropping them. Find-one-function-reliably queries should filter to high-confidence; blast-radius / impact-analysis queries should use all-bands.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 (all) | Scope-aligned P/R/F1 (high-conf) | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|---|
| CALLS | go-ast | 2869 / 5310 | 0.540 / 1.000 / 0.702 | 0.953 / 1.000 / 0.976 | 0.953 / 1.000 / 0.976 | 0.953 / 1.000 / 0.976 |
| IMPORTS | go-ast (dropped) | 0 / 80 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| store | 328 / 843 | 328 | 412 | 0 | 0.443 | 1.000 | **0.614** |
| cypher | 289 / 290 | 289 | 0 | 0 | 1.000 | 1.000 | **1.000** |
| pipeline | 1089 / 1322 | 1089 | 225 | 0 | 0.829 | 1.000 | **0.906** |
| tools | 724 / 734 | 724 | 10 | 0 | 0.986 | 1.000 | **0.993** |
| cbm | 439 / 2121 | 439 | 5 | 0 | 0.989 | 1.000 | **0.994** |

**Spread**: min F1 = 0.614, max F1 = 1.000, range = 0.386


## Caller-kind stratified precision

Each CALLS edge is tagged with the AST scope of its caller (`function-body`, `method-body`, `file-block`, `package-init-block`, `var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The harness reads this property and stratifies precision by it. The **ghost-caller FP rate** is the share of FPs whose caller is a package-level scope rather than a real function/method — alarms above 5%.

### CALLS

| Kind | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `function-body` | 286 | 10 | 0.966 | 296 |
| `method-body` | 1259 | 131 | 0.906 | 1390 |
| `test-body` | 1324 | 0 | 1.000 | 1324 |

**Package-block caller FP rate**: 0.0000 (0 of 141 FPs)

**Caller-kind complement legitimacy** (function/method-body share of all scope-aligned edges): 0.5601 (1686 of 3010)


## Janusian ambiguity stratified precision

Each CALLS edge carries the resolver's pre-tie-break candidate cardinality (`candidate_set_size`). A call site with >= 2 candidates is **Janusian** — the resolver picked among alternatives. Step 2's LLM-Judge taxonomy predicted `same_named_method_disambiguation` (60% of judged FPs) concentrates on Janusian sites; the precision split below tests that hypothesis on real-fixture data. LSP-resolved edges carry size=1 by definition (LSP returns one target without enumerating alternates), so the Janusian signal lives in the registry strategies.

### CALLS

**method_set_ambiguity_index** — share of call sites with >= 2 candidates:

| Project | Ambiguous sites | Total sites | Index |
|---|---:|---:|---:|
| cbm | 0 | 207 | 0.0000 |
| cypher | 0 | 134 | 0.0000 |
| pipeline | 3 | 505 | 0.0059 |
| store | 39 | 232 | 0.1681 |
| tools | 0 | 204 | 0.0000 |

**janusian_site_precision_split** — precision conditional on call-site ambiguity:

| Bucket | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `ambiguous` | 2 | 40 | 0.0476 | 42 |
| `unambiguous` | 2867 | 101 | 0.9660 | 2968 |

**janusian_precision_gap** (unambiguous − ambiguous precision): +0.9184. Positive = unambiguous sites resolve more accurately, consistent with Step 2's prediction. Negative or near-zero = ambiguity is not the dominant FP driver.


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 1282

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm.ExtractFile --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm.cbm_extract_file
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm.ExtractFile --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm.cbm_free_result
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.arena.cbm_arena_destroy
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.arena.cbm_arena_init
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.lsp.go_lsp.cbm_run_go_lsp_cross
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.adaptive.adaptivePool.onTick --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.adaptive_csw_unix.getContentionSignal
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.configlink_strategies.Pipeline.matchTerraformEnvVars --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.resolver.FunctionRegistry.Size
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.envscan.ScanProjectEnvURLs --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.resolver.FunctionRegistry.Size
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.go_dep_registry.goLSPDefIndex.integrateThirdPartyDeps --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.go_dep_registry.goDepVersions.resolveModDir
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.infrascan.Pipeline.passInfraFiles --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-pipeline.resolver.FunctionRegistry.Size
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.arena --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.arena.arena_grow
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.arena --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.arena.cbm_arena_alloc
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.arena.cbm_arena_destroy
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.arena.cbm_arena_init
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.arena.cbm_arena_strdup
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm.get_thread_parser
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm.now_ns
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.extract_defs.cbm_extract_definitions
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.extract_imports.cbm_extract_imports
  Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.cbm --> Users-brandyn.schult-worktrees-code-graph-relationship-fixture-0812-internal-cbm.extract_unified.cbm_extract_unified
```

**Raw-exact false negatives**:
```
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).