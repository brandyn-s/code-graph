# code-graph accuracy baseline — mcp-servers

- **Date**: 2026-04-22
- **Fixture SHA**: `81fa7d5abe36cac7709c225ce35ad908a9017a0d` (short: `81fa7d5`)
- **Project name**: `c-Users-user-Documents-GitHub-mcp-servers`

## Summary

| Edge type | Oracle | Oracle count | Measured count | Exact P | Exact R | Exact F1 | Suffix-3 P | Suffix-3 R | Suffix-3 F1 |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| CALLS | pycg | 371 | 400 | 0.145 | 0.156 | 0.150 | 0.147 | 0.158 | 0.152 |
| IMPORTS | ast | 42 | 11 | 0.909 | 0.238 | 0.377 | 0.000 | 0.000 | 0.000 |
| HTTP_CALLS | opus+sonnet (not yet run) | — | — | — | — | — | — | — | — |

## Samples (first 10 per edge type)

### CALLS

**False positives** (code-graph found, oracle did NOT):
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

**False negatives** (oracle found, code-graph did NOT):
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

**False positives** (code-graph found, oracle did NOT):
```
  colin.entrypoint --> colin
```

**False negatives** (oracle found, code-graph did NOT):
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

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).