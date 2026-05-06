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
| CALLS | go-ast | 1437 / 2866 | 0.342 / 0.683 / 0.456 | 0.923 / 0.683 / 0.785 | 0.923 / 0.683 / 0.785 | 0.923 / 0.683 / 0.785 |
| IMPORTS | go-ast (dropped) | 0 / 25 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| gin | 1437 / 2866 | 981 | 1144 | 456 | 0.462 | 0.683 | **0.551** |

**Spread**: min F1 = 0.551, max F1 = 0.551, range = 0.000


## Caller-kind stratified precision

Each CALLS edge is tagged with the AST scope of its caller (`function-body`, `method-body`, `file-block`, `package-init-block`, `var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The harness reads this property and stratifies precision by it. The **ghost-caller FP rate** is the share of FPs whose caller is a package-level scope rather than a real function/method — alarms above 5%.

### CALLS

| Kind | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `function-body` | 93 | 32 | 0.744 | 125 |
| `method-body` | 211 | 50 | 0.808 | 261 |
| `test-body` | 669 | 0 | 1.000 | 669 |

**Package-block caller FP rate**: 0.0000 (0 of 82 FPs)

**Caller-kind complement legitimacy** (function/method-body share of all scope-aligned edges): 0.2541 (386 of 1519)


## Janusian ambiguity stratified precision

Each CALLS edge carries the resolver's pre-tie-break candidate cardinality (`candidate_set_size`). A call site with >= 2 candidates is **Janusian** — the resolver picked among alternatives. Step 2's LLM-Judge taxonomy predicted `same_named_method_disambiguation` (60% of judged FPs) concentrates on Janusian sites; the precision split below tests that hypothesis on real-fixture data. LSP-resolved edges carry size=1 by definition (LSP returns one target without enumerating alternates), so the Janusian signal lives in the registry strategies.

### CALLS

**method_set_ambiguity_index** — share of call sites with >= 2 candidates:

| Project | Ambiguous sites | Total sites | Index |
|---|---:|---:|---:|
| gin | 60 | 596 | 0.1007 |

**janusian_site_precision_split** — precision conditional on call-site ambiguity:

| Bucket | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `ambiguous` | 37 | 55 | 0.4022 | 92 |
| `unambiguous` | 944 | 27 | 0.9722 | 971 |

**janusian_precision_gap** (unambiguous − ambiguous precision): +0.5700. Positive = unambiguous sites resolve more accurately, consistent with Step 2's prediction. Negative or near-zero = ambiguity is not the dominant FP driver.


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 917

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
  c-Users-user-Documents-bench-fixtures-gin.auth.authPairs.searchCredential --> c-Users-user-Documents-bench-fixtures-gin.bytesconv.StringToBytes
  c-Users-user-Documents-bench-fixtures-gin.auth.authorizationHeader --> c-Users-user-Documents-bench-fixtures-gin.bytesconv.StringToBytes
  c-Users-user-Documents-bench-fixtures-gin.binding_msgpack_test.TestBindingMsgPack --> c-Users-user-Documents-bench-fixtures-gin.binding_msgpack_test.testMsgPackBodyBinding
  c-Users-user-Documents-bench-fixtures-gin.binding_msgpack_test.testMsgPackBodyBinding --> c-Users-user-Documents-bench-fixtures-gin.binding_test.requestWithBody
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingBSON --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testBodyBinding
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingDefaultValueFormPost --> c-Users-user-Documents-bench-fixtures-gin.binding_test.createDefaultFormPostRequest
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingForm --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testFormBinding
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingForm2 --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testFormBinding
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingFormDefaultValue --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testFormBindingDefaultValue
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingFormDefaultValue2 --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testFormBindingDefaultValue
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
  c-Users-user-Documents-bench-fixtures-gin.auth.authPairs.searchCredential --> c-Users-user-Documents-bench-fixtures-gin.bytesconv.StringToBytes
  c-Users-user-Documents-bench-fixtures-gin.auth.authorizationHeader --> c-Users-user-Documents-bench-fixtures-gin.bytesconv.StringToBytes
  c-Users-user-Documents-bench-fixtures-gin.binding_msgpack_test.TestBindingMsgPack --> c-Users-user-Documents-bench-fixtures-gin.binding_msgpack_test.testMsgPackBodyBinding
  c-Users-user-Documents-bench-fixtures-gin.binding_msgpack_test.testMsgPackBodyBinding --> c-Users-user-Documents-bench-fixtures-gin.binding_test.requestWithBody
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingBSON --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testBodyBinding
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingDefaultValueFormPost --> c-Users-user-Documents-bench-fixtures-gin.binding_test.createDefaultFormPostRequest
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingForm --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testFormBinding
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingForm2 --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testFormBinding
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingFormDefaultValue --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testFormBindingDefaultValue
  c-Users-user-Documents-bench-fixtures-gin.binding_test.TestBindingFormDefaultValue2 --> c-Users-user-Documents-bench-fixtures-gin.binding_test.testFormBindingDefaultValue
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).