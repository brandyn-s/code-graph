# code-graph accuracy baseline — gin-adversarial

- **Date**: 2026-04-24
- **Fixture SHA**: `d3ffc9985281dcf4d3bef604cce4e662b1a327a6` (short: `d3ffc99`)
- **Project name**: `c-Users-user-Documents-bench-fixtures-gin`

## Summary

Four metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached).
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|
| CALLS | go-ast | 1591 / 3570 | 0.361 / 0.811 / 0.500 | 0.410 / 0.811 / 0.544 | 0.410 / 0.811 / 0.544 |
| IMPORTS | go-ast (dropped) | 0 / 25 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| gin | 1591 / 3570 | 1290 | 1859 | 301 | 0.410 | 0.811 | **0.544** |

**Spread**: min F1 = 0.544, max F1 = 0.544, range = 0.000


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 880

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.auth.searchCredential
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.benchmarks_test.Header
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.AbortWithStatus
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.requestHeader
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> strconv.Quote
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.auth.searchCredential
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.benchmarks_test.Header
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.AbortWithStatus
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Set
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxySucceed --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthSucceed --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.binding.binding_test.Read --> c-Users-user-Documents-bench-fixtures-gin.gin.New
  c-Users-user-Documents-bench-fixtures-gin.binding.binding_test.TestBindingFormFilesMultipart --> c-Users-user-Documents-bench-fixtures-gin.fs.Open
  c-Users-user-Documents-bench-fixtures-gin.binding.binding_test.testFormBindingForTime --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.binding.default_validator_benchmark_test.BenchmarkSliceValidationError --> c-Users-user-Documents-bench-fixtures-gin.gin.New
  c-Users-user-Documents-bench-fixtures-gin.binding.default_validator_test.TestDefaultValidator --> c-Users-user-Documents-bench-fixtures-gin.binding.default_validator.ValidateStruct
  c-Users-user-Documents-bench-fixtures-gin.binding.default_validator_test.TestSliceValidationError --> c-Users-user-Documents-bench-fixtures-gin.gin.New
  c-Users-user-Documents-bench-fixtures-gin.binding.form_mapping.setByForm --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.binding.form_mapping_test.TestMappingBaseTypes --> c-Users-user-Documents-bench-fixtures-gin.context.String
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.auth.searchCredential
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.benchmarks_test.Header
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.AbortWithStatus
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.Set
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> c-Users-user-Documents-bench-fixtures-gin.context.requestHeader
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForProxy --> strconv.Quote
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.auth.searchCredential
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.benchmarks_test.Header
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.AbortWithStatus
  c-Users-user-Documents-bench-fixtures-gin.auth.BasicAuthForRealm --> c-Users-user-Documents-bench-fixtures-gin.context.Set
```

**Raw-exact false negatives**:
```
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthForProxySucceed --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.auth_test.TestBasicAuthSucceed --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.binding.binding_test.Read --> c-Users-user-Documents-bench-fixtures-gin.gin.New
  c-Users-user-Documents-bench-fixtures-gin.binding.binding_test.TestBindingFormFilesMultipart --> c-Users-user-Documents-bench-fixtures-gin.fs.Open
  c-Users-user-Documents-bench-fixtures-gin.binding.binding_test.testFormBindingForTime --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.binding.default_validator_benchmark_test.BenchmarkSliceValidationError --> c-Users-user-Documents-bench-fixtures-gin.gin.New
  c-Users-user-Documents-bench-fixtures-gin.binding.default_validator_test.TestDefaultValidator --> c-Users-user-Documents-bench-fixtures-gin.binding.default_validator.ValidateStruct
  c-Users-user-Documents-bench-fixtures-gin.binding.default_validator_test.TestSliceValidationError --> c-Users-user-Documents-bench-fixtures-gin.gin.New
  c-Users-user-Documents-bench-fixtures-gin.binding.form_mapping.setByForm --> c-Users-user-Documents-bench-fixtures-gin.context.String
  c-Users-user-Documents-bench-fixtures-gin.binding.form_mapping_test.TestMappingBaseTypes --> c-Users-user-Documents-bench-fixtures-gin.context.String
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).