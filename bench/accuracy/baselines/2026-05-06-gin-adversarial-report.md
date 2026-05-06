# code-graph accuracy baseline — gin-adversarial

- **Date**: 2026-05-06
- **Fixture SHA**: `d3ffc9985281dcf4d3bef604cce4e662b1a327a6` (short: `d3ffc99`)
- **Project name**: `c-Users-user-Documents-bench-fixtures-gin`

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
| CALLS | go-ast | 1591 / 2866 | 0.328 / 0.590 / 0.421 | 0.908 / 0.590 / 0.715 | 0.908 / 0.590 / 0.715 | 0.908 / 0.590 / 0.715 |
| IMPORTS | go-ast (dropped) | 0 / 25 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| gin | 1591 / 2866 | 939 | 1382 | 652 | 0.405 | 0.590 | **0.480** |

**Spread**: min F1 = 0.480, max F1 = 0.480, range = 0.000


## Caller-kind stratified precision

Each CALLS edge is tagged with the AST scope of its caller (`function-body`, `method-body`, `file-block`, `package-init-block`, `var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The harness reads this property and stratifies precision by it. The **ghost-caller FP rate** is the share of FPs whose caller is a package-level scope rather than a real function/method — alarms above 5%.

### CALLS

| Kind | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `function-body` | 150 | 85 | 0.638 | 235 |
| `test-body` | 779 | 0 | 1.000 | 779 |

**Package-block caller FP rate**: 0.1053 (10 of 95 FPs) ALARM

**Caller-kind complement legitimacy** (function/method-body share of all scope-aligned edges): 0.1394 (235 of 1686)


## Janusian ambiguity stratified precision

Each CALLS edge carries the resolver's pre-tie-break candidate cardinality (`candidate_set_size`). A call site with >= 2 candidates is **Janusian** — the resolver picked among alternatives. Step 2's LLM-Judge taxonomy predicted `same_named_method_disambiguation` (60% of judged FPs) concentrates on Janusian sites; the precision split below tests that hypothesis on real-fixture data. LSP-resolved edges carry size=1 by definition (LSP returns one target without enumerating alternates), so the Janusian signal lives in the registry strategies.

### CALLS

**method_set_ambiguity_index** — share of call sites with >= 2 candidates:

| Project | Ambiguous sites | Total sites | Index |
|---|---:|---:|---:|
| gin | 76 | 682 | 0.1114 |

**janusian_site_precision_split** — precision conditional on call-site ambiguity:

| Bucket | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `ambiguous` | 45 | 48 | 0.4839 | 93 |
| `unambiguous` | 894 | 47 | 0.9501 | 941 |

**janusian_precision_gap** (unambiguous − ambiguous precision): +0.4662. Positive = unambiguous sites resolve more accurately, consistent with Step 2's prediction. Negative or near-zero = ambiguity is not the dominant FP driver.


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 880

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.auth.authPairs.searchCredential
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.benchmarks_test.mockWriter.Header
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.Context.AbortWithStatus
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.Context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.Context.requestHeader
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.auth.authPairs.searchCredential
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.benchmarks_test.mockWriter.Header
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Context.AbortWithStatus
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Context.requestHeader
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuth401 --> c-Users-user-Documents-bench-fixtures-gin.context.Get
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuth401 --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuth401WithCustomRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Get
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuth401WithCustomRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxy407 --> c-Users-user-Documents-bench-fixtures-gin.context.Get
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxy407 --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxySucceed --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxySucceed --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthSucceed --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthSucceed --> c-Users-user-Documents-bench-fixtures-gin.context.String
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.auth.authPairs.searchCredential
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.benchmarks_test.mockWriter.Header
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.Context.AbortWithStatus
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.Context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.Context.requestHeader
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.auth.authPairs.searchCredential
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.benchmarks_test.mockWriter.Header
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Context.AbortWithStatus
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Context.requestHeader
```

**Raw-exact false negatives**:
```
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuth401 --> c-Users-user-Documents-bench-fixtures-gin.context.Get
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuth401 --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuth401WithCustomRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Get
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuth401WithCustomRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxy407 --> c-Users-user-Documents-bench-fixtures-gin.context.Get
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxy407 --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxySucceed --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxySucceed --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthSucceed --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthSucceed --> c-Users-user-Documents-bench-fixtures-gin.context.String
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).