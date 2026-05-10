# code-graph accuracy baseline — mcp-servers

- **Date**: 2026-05-10
- **Fixture SHA**: `76b08b11664030ce190ee2dbcf6e026dc0aeb1e3` (short: `76b08b1`)
- **Project name**: `c-Users-user-Documents-GitHub-mcp-servers`

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
| CALLS | pycg | 371 / 511 | 0.712 / 0.981 / 0.825 | 0.997 / 0.981 / 0.989 | 0.997 / 0.981 / 0.989 | 0.997 / 0.981 / 0.989 |
| IMPORTS | ast | 47 / 62 | 0.758 / 1.000 / 0.862 | 0.959 / 1.000 / 0.979 | 0.959 / 1.000 / 0.979 | 0.959 / 1.000 / 0.979 |
| HTTP_CALLS | opus+sonnet (not yet run) | — / — | — (pending) | — | — |

## Caller-kind stratified precision

Each CALLS edge is tagged with the AST scope of its caller (`function-body`, `method-body`, `file-block`, `package-init-block`, `var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The harness reads this property and stratifies precision by it. The **ghost-caller FP rate** is the share of FPs whose caller is a package-level scope rather than a real function/method — alarms above 5%.

### CALLS

| Kind | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `function-body` | 355 | 0 | 1.000 | 355 |

**Package-block caller FP rate**: 0.0000 (0 of 1 FPs)

**Caller-kind complement legitimacy** (function/method-body share of all scope-aligned edges): 0.9704 (361 of 372)

### IMPORTS

> Note: caller_node_kind not yet emitted by the indexed binary; metrics skipped.


## Janusian ambiguity stratified precision

Each CALLS edge carries the resolver's pre-tie-break candidate cardinality (`candidate_set_size`). A call site with >= 2 candidates is **Janusian** — the resolver picked among alternatives. Step 2's LLM-Judge taxonomy predicted `same_named_method_disambiguation` (60% of judged FPs) concentrates on Janusian sites; the precision split below tests that hypothesis on real-fixture data. LSP-resolved edges carry size=1 by definition (LSP returns one target without enumerating alternates), so the Janusian signal lives in the registry strategies.

### CALLS

**method_set_ambiguity_index** — share of call sites with >= 2 candidates:

| Project | Ambiguous sites | Total sites | Index |
|---|---:|---:|---:|
| __all__ | 1 | 183 | 0.0055 |

**janusian_site_precision_split** — precision conditional on call-site ambiguity:

| Bucket | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `ambiguous` | 1 | 0 | 1.0000 | 1 |
| `unambiguous` | 363 | 1 | 0.9973 | 364 |

**janusian_precision_gap** (unambiguous − ambiguous precision): -0.0027. Positive = unambiguous sites resolve more accurately, consistent with Step 2's prediction. Negative or near-zero = ambiguity is not the dominant FP driver.


## CALLS modal split — by edge-kind union

After PR #121, the CALLS family is partitioned into:
- `CALLS` — real-to-real (precision-relevant)
- `CALLS_EXTERNAL` — real-to-stub (LSP-resolved external)
- `CALLS_PSEUDO` — synthetic module-default caller

Each row below recomputes precision/recall/F1 against the same
PyCG oracle but with a different union on the measured side.
Headline `results.CALLS` is the `real_only` row.

| Union | Measured | Exact P/R/F1 | Suffix-3 P/R/F1 | Scope-aligned P/R/F1 |
|---|---|---|---|---|
| real_only | 511 | 0.712 / 0.981 / 0.825 | 0.723 / 0.997 / 0.838 | 0.997 / 0.981 / 0.989 |
| real_plus_external | 511 | 0.712 / 0.981 / 0.825 | 0.723 / 0.997 / 0.838 | 0.997 / 0.981 / 0.989 |
| real_plus_pseudo | 511 | 0.712 / 0.981 / 0.825 | 0.723 / 0.997 / 0.838 | 0.997 / 0.981 / 0.989 |
| all_calls_family | 511 | 0.712 / 0.981 / 0.825 | 0.723 / 0.997 / 0.838 | 0.997 / 0.981 / 0.989 |

Diverging rows expose how each non-real population dilutes the aggregate. Most accuracy regressions live in `real_only`; the other rows are diagnostic.

## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 184

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  security-remix.backend_pool.BackendPool.call_tool --> security-remix.backend_pool.BackendPool.call_tool
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  claude-proxy.claude_proxy.extract_scannable_text --> claude-proxy.claude_proxy.extract_scannable_text.content.get
  crowdstrike.proxy --> shared.mcp_http._build_oauth._get_resource_url
  crowdstrike.proxy --> shared.mcp_http._build_oauth.get_middleware
  crowdstrike.proxy --> shared.mcp_http._build_oauth.get_routes
  crowdstrike.proxy --> shared.opa_middleware.OPAMiddleware
  security-remix.security_remix_server --> security-remix.backend_pool.BackendPool.__init__
  security-remix.security_remix_server --> security-remix.tool_index.ToolIndex.__init__
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  airlock.airlock_mcp_server._lifespan --> security-remix.backend_pool.BackendPool.close
  claude-proxy.claude_proxy.check_guardrails --> confluence.confluence_fedramp_mcp.ConfluenceClient.search
  claude-proxy.manage_guardrails --> claude-proxy.manage_guardrails.main
  claude-proxy.manage_guardrails.cmd_add --> claude-proxy.manage_guardrails.format_rule
  claude-proxy.manage_guardrails.cmd_add --> claude-proxy.manage_guardrails.load_rules
  claude-proxy.manage_guardrails.cmd_add --> claude-proxy.manage_guardrails.save_rules
  claude-proxy.manage_guardrails.cmd_edit --> claude-proxy.manage_guardrails.load_rules
  claude-proxy.manage_guardrails.cmd_edit --> claude-proxy.manage_guardrails.save_rules
  claude-proxy.manage_guardrails.cmd_edit --> security-remix.evals.ab_metrics.ABTestRun.load
  claude-proxy.manage_guardrails.cmd_list --> claude-proxy.manage_guardrails.format_rule
```

**Raw-exact false negatives**:
```
  claude-proxy.claude_proxy.extract_scannable_text --> claude-proxy.claude_proxy.extract_scannable_text.content.get
  crowdstrike.proxy --> shared.mcp_http._build_oauth._get_resource_url
  crowdstrike.proxy --> shared.mcp_http._build_oauth.get_middleware
  crowdstrike.proxy --> shared.mcp_http._build_oauth.get_routes
  crowdstrike.proxy --> shared.opa_middleware.OPAMiddleware
  security-remix.security_remix_server --> security-remix.backend_pool.BackendPool.__init__
  security-remix.security_remix_server --> security-remix.tool_index.ToolIndex.__init__
```

### IMPORTS

Oracle analyzed callers: 33

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  security-remix.security_remix_server --> security-remix.backend_pool
  security-remix.security_remix_server --> security-remix.tool_index
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  scripts.audit_msgraph_params --> scripts.spec_utils
  scripts.test_spec_utils --> scripts.spec_utils
  scripts.test_validate_catalog --> scripts.validate_catalog
  security-remix.evals.test_ab_discovery --> security-remix.evals.ab_metrics
  security-remix.evals.test_ab_discovery --> security-remix.evals.augmented_backends
  security-remix.evals.test_ab_discovery --> security-remix.evals.test_scenarios
  security-remix.evals.test_scenarios --> security-remix.backend_pool
  security-remix.evals.test_scenarios --> security-remix.tool_index
  security-remix.security_remix_server --> security-remix.backend_pool
  security-remix.security_remix_server --> security-remix.tool_index
```

**Raw-exact false negatives**:
```
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).