# code-graph accuracy baseline — mcp-servers

- **Date**: 2026-04-23
- **Fixture SHA**: `81fa7d5abe36cac7709c225ce35ad908a9017a0d` (short: `81fa7d5`)
- **Project name**: `c-Users-user-Documents-GitHub-mcp-servers`

## Summary

Three metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached) to give an apples-to-apples accuracy reading.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Suffix-3 P/R/F1 | Scope-aligned P/R/F1 |
|---|---|---|---|---|---|
| CALLS | pycg | 371 / 400 | 0.145 / 0.156 / 0.150 | 0.147 / 0.158 / 0.152 | 0.935 / 0.156 / 0.268 |
| IMPORTS | ast | 42 / 11 | 0.909 / 0.238 / 0.377 | 0.000 / 0.000 / 0.000 | 1.000 / 0.238 / 0.385 |
| HTTP_CALLS | opus+sonnet | 411 / 0 | 0.000 / 0.000 / 0.000 | 0.000 / 0.000 / 0.000 | 0.000 / 0.000 / 0.000 |

## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 184

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  claude-proxy.claude_proxy._load_rules_from_s3 --> confluence.confluence_fedramp_mcp.client
  claude-proxy.claude_proxy._poll_s3_guardrails --> confluence.confluence_fedramp_mcp.client
  claude-proxy.claude_proxy.load_guardrail_config --> claude-proxy.claude_proxy.GuardrailRule
  claude-proxy.claude_proxy.redact_request_body --> .gitleaks.extend
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  airlock.airlock_mcp_server --> shared.mcp_http.configure_http_transport
  airlock.airlock_mcp_server._post --> shared.errors.api_error
  airlock.airlock_mcp_server._post_raw --> shared.errors.api_error
  airlock.airlock_mcp_server.airlock_add_allowlist_metarule_criteria --> airlock.airlock_mcp_server._j
  airlock.airlock_mcp_server.airlock_add_allowlist_metarule_criteria --> airlock.airlock_mcp_server._post
  airlock.airlock_mcp_server.airlock_add_blocklist_metarule_criteria --> airlock.airlock_mcp_server._j
  airlock.airlock_mcp_server.airlock_add_blocklist_metarule_criteria --> airlock.airlock_mcp_server._post
  airlock.airlock_mcp_server.airlock_add_hash --> airlock.airlock_mcp_server._j
  airlock.airlock_mcp_server.airlock_add_hash --> airlock.airlock_mcp_server._post
  airlock.airlock_mcp_server.airlock_add_hash_to_baseline --> airlock.airlock_mcp_server._j
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  claude-compliance.test_claude_compliance --> claude-compliance.test_claude_compliance.test_read_tools_have_readonly_hint
  claude-compliance.test_claude_compliance --> claude-compliance.test_claude_compliance.test_tools_load
  claude-compliance.test_claude_compliance --> claude-compliance.test_claude_compliance.test_write_tools_have_destructive_hint
  claude-compliance.test_claude_compliance.test_read_tools_have_readonly_hint --> security-remix.tool_index.ToolIndex.get_tool
  claude-compliance.test_claude_compliance.test_write_tools_have_destructive_hint --> security-remix.tool_index.ToolIndex.get_tool
  claude-proxy.claude_proxy._get_circuit --> claude-proxy.claude_proxy.CircuitState
  claude-proxy.claude_proxy._get_s3_client --> confluence.confluence_fedramp_mcp.client
  claude-proxy.claude_proxy._load_key_pool --> claude-proxy.claude_proxy.PoolKey
  claude-proxy.claude_proxy._load_rules_from_s3 --> confluence.confluence_fedramp_mcp.client
  claude-proxy.claude_proxy._parse_rules_json --> claude-proxy.claude_proxy.GuardrailRule
```

**Raw-exact false negatives**:
```
  airlock.airlock_mcp_server --> shared.mcp_http.configure_http_transport
  airlock.airlock_mcp_server._post --> shared.errors.api_error
  airlock.airlock_mcp_server._post_raw --> shared.errors.api_error
  airlock.airlock_mcp_server.airlock_add_allowlist_metarule_criteria --> airlock.airlock_mcp_server._j
  airlock.airlock_mcp_server.airlock_add_allowlist_metarule_criteria --> airlock.airlock_mcp_server._post
  airlock.airlock_mcp_server.airlock_add_blocklist_metarule_criteria --> airlock.airlock_mcp_server._j
  airlock.airlock_mcp_server.airlock_add_blocklist_metarule_criteria --> airlock.airlock_mcp_server._post
  airlock.airlock_mcp_server.airlock_add_hash --> airlock.airlock_mcp_server._j
  airlock.airlock_mcp_server.airlock_add_hash --> airlock.airlock_mcp_server._post
  airlock.airlock_mcp_server.airlock_add_hash_to_baseline --> airlock.airlock_mcp_server._j
```

### IMPORTS

Oracle analyzed callers: 31

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  airlock.airlock_mcp_server --> shared.errors
  airlock.airlock_mcp_server --> shared.mcp_http
  claude_compliance.claude_compliance_mcp --> shared.errors
  claude_compliance.claude_compliance_mcp --> shared.mcp_http
  claude_platform.claude_platform_mcp --> shared.errors
  claude_platform.claude_platform_mcp --> shared.mcp_http
  confluence.confluence_fedramp_mcp --> shared.mcp_http
  crowdstrike.proxy --> shared.mcp_http
  crowdstrike.proxy --> shared.opa_middleware
  github.github_mcp --> shared.mcp_http
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  colin.entrypoint --> colin
```

**Raw-exact false negatives**:
```
  airlock.airlock_mcp_server --> shared.errors
  airlock.airlock_mcp_server --> shared.mcp_http
  claude_compliance.claude_compliance_mcp --> shared.errors
  claude_compliance.claude_compliance_mcp --> shared.mcp_http
  claude_platform.claude_platform_mcp --> shared.errors
  claude_platform.claude_platform_mcp --> shared.mcp_http
  confluence.confluence_fedramp_mcp --> shared.mcp_http
  crowdstrike.proxy --> shared.mcp_http
  crowdstrike.proxy --> shared.opa_middleware
  github.github_mcp --> shared.mcp_http
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