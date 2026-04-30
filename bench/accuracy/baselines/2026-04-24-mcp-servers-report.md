# code-graph accuracy baseline — mcp-servers

- **Date**: 2026-04-24
- **Fixture SHA**: `81fa7d5abe36cac7709c225ce35ad908a9017a0d` (short: `81fa7d5`)
- **Project name**: `c-Users-user-Documents-GitHub-mcp-servers`

## Summary

Four metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached).
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|
| CALLS | pycg | 371 / 506 | 0.719 / 0.981 / 0.830 | 0.997 / 0.981 / 0.989 | 0.997 / 0.981 / 0.989 |
| IMPORTS | ast | 45 / 51 | 0.882 / 1.000 / 0.938 | 1.000 / 1.000 / 1.000 | 1.000 / 1.000 / 1.000 |
| HTTP_CALLS | opus+sonnet | 411 / 0 | 0.000 / 0.000 / 0.000 | 0.000 / 0.000 / 0.000 | 0.000 / 0.000 / 0.000 |

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
  claude-proxy.manage_guardrails.cmd_list --> claude-proxy.manage_guardrails.format_rule
  claude-proxy.manage_guardrails.cmd_list --> claude-proxy.manage_guardrails.load_rules
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

Oracle analyzed callers: 31

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  scripts.audit_msgraph_params --> scripts.spec_utils
  scripts.test_spec_utils --> scripts.spec_utils
  security-remix.evals.test_ab_discovery --> security-remix.evals.ab_metrics
  security-remix.evals.test_ab_discovery --> security-remix.evals.augmented_backends
  security-remix.evals.test_ab_discovery --> security-remix.evals.test_scenarios
  security-remix.tool_index --> security-remix.classify
```

**Raw-exact false negatives**:
```
```

### HTTP_CALLS

Oracle analyzed callers: 372

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  airlock.airlock_mcp_server._post --> POST {path}
  airlock.airlock_mcp_server._post_raw --> POST {path}
  claude-compliance.claude_compliance_mcp.delete_chat --> DELETE /v1/compliance/apps/chats/{claude_chat_id}
  claude-compliance.claude_compliance_mcp.delete_file --> DELETE /v1/compliance/apps/chats/files/{claude_file_id}
  claude-compliance.claude_compliance_mcp.delete_project --> DELETE /v1/compliance/apps/projects/{claude_proj_id}
  claude-compliance.claude_compliance_mcp.delete_project_document --> DELETE /v1/compliance/apps/projects/documents/{claude_proj_doc_id}
  claude-compliance.claude_compliance_mcp.download_file_content --> GET /v1/compliance/apps/chats/files/{claude_file_id}/content
  claude-compliance.claude_compliance_mcp.get_chat_messages --> GET /v1/compliance/apps/chats/{claude_chat_id}/messages
  claude-compliance.claude_compliance_mcp.get_project --> GET /v1/compliance/apps/projects/{claude_proj_id}
  claude-compliance.claude_compliance_mcp.get_project_document --> GET /v1/compliance/apps/projects/documents/{claude_proj_doc_id}
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
```

**Raw-exact false negatives**:
```
  airlock.airlock_mcp_server._post --> POST {path}
  airlock.airlock_mcp_server._post_raw --> POST {path}
  claude-compliance.claude_compliance_mcp.delete_chat --> DELETE /v1/compliance/apps/chats/{claude_chat_id}
  claude-compliance.claude_compliance_mcp.delete_file --> DELETE /v1/compliance/apps/chats/files/{claude_file_id}
  claude-compliance.claude_compliance_mcp.delete_project --> DELETE /v1/compliance/apps/projects/{claude_proj_id}
  claude-compliance.claude_compliance_mcp.delete_project_document --> DELETE /v1/compliance/apps/projects/documents/{claude_proj_doc_id}
  claude-compliance.claude_compliance_mcp.download_file_content --> GET /v1/compliance/apps/chats/files/{claude_file_id}/content
  claude-compliance.claude_compliance_mcp.get_chat_messages --> GET /v1/compliance/apps/chats/{claude_chat_id}/messages
  claude-compliance.claude_compliance_mcp.get_project --> GET /v1/compliance/apps/projects/{claude_proj_id}
  claude-compliance.claude_compliance_mcp.get_project_document --> GET /v1/compliance/apps/projects/documents/{claude_proj_doc_id}
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).