# code-graph accuracy baseline — code-graph-go

- **Date**: 2026-05-10
- **Fixture SHA**: `1dab656f84135f8f6a448eb3066598c3f31f3fb5` (short: `1dab656`)
- **Project name**: `c-Users-user-Documents-GitHub-code-graph`

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
| CALLS | go-ast | 2779 / 5340 | 0.520 / 1.000 / 0.685 | 0.951 / 1.000 / 0.975 | 0.951 / 1.000 / 0.975 | 0.951 / 1.000 / 0.975 |
| IMPORTS | go-ast (dropped) | 0 / 109 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| store | 324 / 843 | 324 | 415 | 0 | 0.438 | 1.000 | **0.610** |
| cypher | 247 / 290 | 247 | 41 | 0 | 0.858 | 1.000 | **0.923** |
| pipeline | 1052 / 1322 | 1052 | 234 | 0 | 0.818 | 1.000 | **0.900** |
| tools | 717 / 734 | 717 | 10 | 0 | 0.986 | 1.000 | **0.993** |
| cbm | 439 / 2151 | 439 | 5 | 0 | 0.989 | 1.000 | **0.994** |

**Spread**: min F1 = 0.610, max F1 = 0.994, range = 0.385


## Caller-kind stratified precision

Each CALLS edge is tagged with the AST scope of its caller (`function-body`, `method-body`, `file-block`, `package-init-block`, `var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The harness reads this property and stratifies precision by it. The **ghost-caller FP rate** is the share of FPs whose caller is a package-level scope rather than a real function/method — alarms above 5%.

### CALLS

| Kind | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `function-body` | 272 | 12 | 0.958 | 284 |
| `method-body` | 1259 | 131 | 0.906 | 1390 |
| `test-body` | 1248 | 0 | 1.000 | 1248 |

**Package-block caller FP rate**: 0.0000 (0 of 143 FPs)

**Caller-kind complement legitimacy** (function/method-body share of all scope-aligned edges): 0.5729 (1674 of 2922)


## Janusian ambiguity stratified precision

Each CALLS edge carries the resolver's pre-tie-break candidate cardinality (`candidate_set_size`). A call site with >= 2 candidates is **Janusian** — the resolver picked among alternatives. Step 2's LLM-Judge taxonomy predicted `same_named_method_disambiguation` (60% of judged FPs) concentrates on Janusian sites; the precision split below tests that hypothesis on real-fixture data. LSP-resolved edges carry size=1 by definition (LSP returns one target without enumerating alternates), so the Janusian signal lives in the registry strategies.

### CALLS

**method_set_ambiguity_index** — share of call sites with >= 2 candidates:

| Project | Ambiguous sites | Total sites | Index |
|---|---:|---:|---:|
| cbm | 0 | 207 | 0.0000 |
| cypher | 0 | 133 | 0.0000 |
| pipeline | 3 | 496 | 0.0060 |
| store | 39 | 231 | 0.1688 |
| tools | 0 | 200 | 0.0000 |

**janusian_site_precision_split** — precision conditional on call-site ambiguity:

| Bucket | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `ambiguous` | 2 | 40 | 0.0476 | 42 |
| `unambiguous` | 2777 | 103 | 0.9642 | 2880 |

**janusian_precision_gap** (unambiguous − ambiguous precision): +0.9166. Positive = unambiguous sites resolve more accurately, consistent with Step 2's prediction. Negative or near-zero = ambiguity is not the dominant FP driver.


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 1267

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.ExtractFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.cbm_extract_file
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.ExtractFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.cbm.cbm_free_result
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_destroy
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.arena.cbm_arena_init
  c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp_bridge.RunGoLSPCrossFile --> c-Users-user-Documents-GitHub-code-graph-internal-cbm.lsp.go_lsp.cbm_run_go_lsp_cross
  c-Users-user-Documents-GitHub-code-graph-internal-cypher.parser.Parse --> c-Users-user-Documents-GitHub-code-graph-internal-cypher.parser.Parser.parseQuery
  c-Users-user-Documents-GitHub-code-graph-internal-pipeline.adaptive.adaptivePool.onTick --> c-Users-user-Documents-GitHub-code-graph-internal-pipeline.adaptive_csw_unix.getContentionSignal
  c-Users-user-Documents-GitHub-code-graph-internal-pipeline.configlink_strategies.Pipeline.matchTerraformEnvVars --> c-Users-user-Documents-GitHub-code-graph-internal-pipeline.resolver.FunctionRegistry.Size
  c-Users-user-Documents-GitHub-code-graph-internal-pipeline.envscan.ScanProjectEnvURLs --> c-Users-user-Documents-GitHub-code-graph-internal-pipeline.resolver.FunctionRegistry.Size
  c-Users-user-Documents-GitHub-code-graph-internal-pipeline.go_dep_registry.goLSPDefIndex.integrateThirdPartyDeps --> c-Users-user-Documents-GitHub-code-graph-internal-pipeline.go_dep_registry.goDepVersions.resolveModDir
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
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
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).