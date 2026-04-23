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
| CALLS | pycg | 371 / 549 | 0.665 / 0.984 / 0.793 | 0.675 / 0.997 / 0.805 | 0.979 / 0.984 / 0.981 |
| IMPORTS | ast | 42 / 42 | 1.000 / 1.000 / 1.000 | 0.000 / 0.000 / 0.000 | 1.000 / 1.000 / 1.000 |
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
  security-remix.backend_pool.BackendPool.call_tool --> security-remix.backend_pool.BackendPool.call_tool
  security-remix.security_remix_server --> security-remix.backend_pool.BackendPool
  security-remix.security_remix_server --> security-remix.tool_index.ToolIndex
  security-remix.tool_index.ToolIndex.refresh --> security-remix.tool_index.ToolEntry
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  claude-proxy.claude_proxy.extract_scannable_text --> claude-proxy.claude_proxy.extract_scannable_text.content.get
  crowdstrike.proxy --> shared.mcp_http._build_oauth._get_resource_url
  crowdstrike.proxy --> shared.mcp_http._build_oauth.get_middleware
  crowdstrike.proxy --> shared.mcp_http._build_oauth.get_routes
  security-remix.security_remix_server --> security-remix.backend_pool.BackendPool.__init__
  security-remix.security_remix_server --> security-remix.tool_index.ToolIndex.__init__
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  airlock.airlock_mcp_server._lifespan --> security-remix.backend_pool.BackendPool.close
  claude-proxy.claude_proxy._get_circuit --> claude-proxy.claude_proxy.CircuitState
  claude-proxy.claude_proxy._get_s3_client --> confluence.confluence_fedramp_mcp.client
  claude-proxy.claude_proxy._load_key_pool --> claude-proxy.claude_proxy.PoolKey
  claude-proxy.claude_proxy._load_rules_from_s3 --> confluence.confluence_fedramp_mcp.client
  claude-proxy.claude_proxy._parse_rules_json --> claude-proxy.claude_proxy.GuardrailRule
  claude-proxy.claude_proxy._poll_s3_guardrails --> confluence.confluence_fedramp_mcp.client
  claude-proxy.claude_proxy.check_guardrails --> confluence.confluence_fedramp_mcp.ConfluenceClient.search
  claude-proxy.claude_proxy.load_guardrail_config --> claude-proxy.claude_proxy.GuardrailRule
  claude-proxy.claude_proxy.redact_request_body --> .gitleaks.extend
```

**Raw-exact false negatives**:
```
  claude-proxy.claude_proxy.extract_scannable_text --> claude-proxy.claude_proxy.extract_scannable_text.content.get
  crowdstrike.proxy --> shared.mcp_http._build_oauth._get_resource_url
  crowdstrike.proxy --> shared.mcp_http._build_oauth.get_middleware
  crowdstrike.proxy --> shared.mcp_http._build_oauth.get_routes
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