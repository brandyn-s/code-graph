# EXP-008: Ground Truth

Established 2026-03-14 via manual grep/read of mcp-servers repo.

---

## Structural Queries

### S1: "What calls `_build_oauth`?"

| Caller | File:Line |
|--------|-----------|
| `configure_http_transport` | `shared/mcp_http.py:377` |

**Definition:** `shared/mcp_http.py:130`
**Total callers:** 1 (private function, single call site inside same file)

### S2: "Find dead code - functions nobody calls"

**Note:** This query requires graph analysis (search_graph with max_degree=0). Cannot be fully established via grep. The repo has ~742 `@mcp.tool` / `@server.tool` decorations across 20 files - these are framework-invoked entry points, not dead code.

Ground truth for this query will be established when codebase-memory-mcp is queried during testing. Manual baseline: no obviously dead utility functions identified via grep - most non-tool functions are private helpers called within the same file.

### S3: "Show all HTTP routes"

| Service | Route | Handler | File:Line |
|---------|-------|---------|-----------|
| claude-proxy | `/health` | `health` | `claude-proxy/claude_proxy.py:2582` |
| claude-proxy | `/metrics` | `metrics` | `claude-proxy/claude_proxy.py:2583` |
| claude-proxy | `/v1/messages` | `proxy_messages` | `claude-proxy/claude_proxy.py:2584` |
| claude-proxy | `/v1/messages/count_tokens` | `proxy_count_tokens` | `claude-proxy/claude_proxy.py:2585` |
| crowdstrike | `/health` | `health_check` | `crowdstrike/proxy.py:161` |
| crowdstrike | `/internal/tools` | `internal_tools` | `crowdstrike/proxy.py:162` |
| crowdstrike | `/internal/call` | `internal_call` | `crowdstrike/proxy.py:163` |
| slack-connect | `/health` | `health` | `slack-connect/slack_connect_app.py:394` |
| slack-connect | `/` | `home` | `slack-connect/slack_connect_app.py:395` |
| slack-connect | `/entra/callback` | `entra_callback` | `slack-connect/slack_connect_app.py:396` |
| slack-connect | `/connect` | `connect` | `slack-connect/slack_connect_app.py:397` |
| slack-connect | `/callback` | `callback` | `slack-connect/slack_connect_app.py:398` |
| slack-connect | `/disconnect` | `disconnect` | `slack-connect/slack_connect_app.py:399` |

**Total:** 13 HTTP routes across 3 services (Starlette `Route()` objects)

### S4: "Trace callers of `check_permissions`"

**Definition:** `slack-user/slack_user_mcp.py:576`
**Code-level callers:** 0 - this is an MCP tool function invoked by the MCP framework, not by other code.

### S5: "What's the blast radius of changing `shared/mcp_http.py`?"

**Direct importers (15 services):**

| Service | Import location |
|---------|----------------|
| tenable | `tenable/tenable_mcp.py:2083` |
| lucid | `lucid/lucid_mcp.py:521` |
| airlock | `airlock/airlock_mcp_server.py:1800` |
| tavily | `tavily/tavily_mcp.py:503` |
| claude-compliance | `claude-compliance/claude_compliance_mcp.py:499` |
| lever | `lever/lever_mcp.py:1717` |
| tailscale | `tailscale/tailscale_mcp.py:974` |
| hologram | `hologram/hologram_mcp.py:1231` |
| netcloud | `netcloud/netcloud_mcp.py:1604` |
| msgraph | `msgraph/msgraph_mcp.py:3084` |
| slack-user | `slack-user/slack_user_mcp.py:690` |
| claude_platform | `claude_platform/claude_platform_mcp.py:1184` |
| confluence | `confluence/confluence_fedramp_mcp.py:1514` |
| github | `github/github_mcp.py:682` |
| security-remix | `security-remix/security_remix_server.py:368` |

**Transitive dependency:** `shared/opa_middleware.py` (imported at line 420)
**Total blast radius:** 15 direct + 1 transitive = all MCP services + OPA middleware

---

## Conceptual Queries

### C1: "Find authentication logic"

**Primary (directly implements auth):**

| Function/Class | File:Line | Role |
|---------------|-----------|------|
| `_build_oauth()` | `shared/mcp_http.py:130` | Builds OAuth2 client credentials config |
| `configure_http_transport()` | `shared/mcp_http.py:313` | Wires OAuth into transport layer |
| `_decode_jwt_payload()` | `shared/opa_middleware.py:545` | JWT token decoding |
| `_extract_identity()` | `shared/opa_middleware.py:567` | Identity extraction from HTTP headers |
| `_authorize_tool_call()` | `shared/opa_middleware.py:820` | OPA-based tool call authorization |
| `OPAMiddleware.__call__()` | `shared/opa_middleware.py:684` | ASGI middleware entry point for auth |
| `check_permissions()` | `slack-user/slack_user_mcp.py:576` | Slack permission verification tool |
| `entra_callback()` | `slack-connect/slack_connect_app.py:396` | Entra ID OAuth callback route |

**Secondary (configures/uses auth):**
- All 15 services calling `configure_http_transport` (auth bootstrapping)
- `crowdstrike/proxy.py:173` (OPA middleware import)
- `scripts/test_opa_filtering.py` (test harness)

**Primary count:** 8 | **Total files:** ~20

### C2: "Where do we handle errors?"

**Primary (custom error handling patterns):**

| Pattern | File | Role |
|---------|------|------|
| `_jsonrpc_error()` | `shared/opa_middleware.py:650` | JSON-RPC error formatting |
| `ToolError` raises | All MCP servers | MCP tool input validation errors |
| HTTP error responses | `claude-proxy/claude_proxy.py` | Proxy error wrapping/forwarding |
| `HTTPException` | `slack-connect/slack_connect_app.py` | Starlette HTTP errors |

**Files with error handling (any `except`/`raise`):** 33 files
**This query is intentionally broad** - a good semantic tool should rank custom error handling infrastructure above routine try/except blocks.

### C3: "Show retry patterns"

**Primary (implements retry/backoff logic):**

| Function | File:Line | Pattern |
|----------|-----------|---------|
| `_retry_delay()` | `claude-proxy/claude_proxy.py:1196` | Exponential backoff with jitter |
| `_is_retryable_status()` | `claude-proxy/claude_proxy.py:1209` | Retryable HTTP status classification |
| Retry loop in `proxy_messages` | `claude-proxy/claude_proxy.py:2044-2109` | Full upstream retry with pool rotation |
| 429 retry handling | `tavily/tavily_mcp.py:85` | Rate limit retry (raise ToolError) |
| Merge method retry | `github/github_mcp.py:274-307` | Squash fallback on merge rejection |

**Primary count:** 5 patterns across 3 files

### C4: "Find rate limiting code"

**Primary (implements rate limiting):**

| Function/Pattern | File:Line | Role |
|-----------------|-----------|------|
| `check_rate_limit()` | `claude-proxy/claude_proxy.py:902` | Per-user TPM/RPM enforcement |
| `_rate_limit_overrides` | `claude-proxy/claude_proxy.py:884` | S3-loaded per-key overrides |
| `_get_rate_limits()` | `claude-proxy/claude_proxy.py:894` | Resolve effective limits for a key |
| Redis sliding window | `claude-proxy/claude_proxy.py:903-960` | Token/request counting via Redis |
| Rate limit headers | `claude-proxy/claude_proxy.py:865` | anthropic-ratelimit-* header propagation |
| Rate limit metrics | `claude-proxy/claude_proxy.py:1343` | Prometheus counter for enforced limits |

**Secondary (mentions/handles upstream rate limits):**
- `tavily/tavily_mcp.py:85` - handles 429 from Tavily API
- `github/github_mcp.py:10` - mentions rate limiting in docstring

**Primary count:** 6 patterns, all in `claude-proxy/claude_proxy.py`

### C5: "How is OPA authorization enforced?"

**Primary (OPA implementation):**

| Function/Class | File:Line | Role |
|---------------|-----------|------|
| `OPAMiddleware` class | `shared/opa_middleware.py:679` | ASGI middleware wrapping all tool calls |
| `OPAMiddleware.__call__()` | `shared/opa_middleware.py:684` | Entry point - intercepts ASGI scope |
| `_authorize_tool_call()` | `shared/opa_middleware.py:820` | Queries OPA for allow/deny decision |
| `_filter_tools_list()` | `shared/opa_middleware.py:1105` | Filters tools/list response by policy |
| `_make_filtering_send()` | `shared/opa_middleware.py:1042` | ASGI send wrapper for response filtering |
| `_decode_jwt_payload()` | `shared/opa_middleware.py:545` | Extracts identity from JWT |
| `_extract_identity()` | `shared/opa_middleware.py:567` | Resolves user identity from headers |
| `_notify_slack_write()` | `shared/opa_middleware.py:792` | Slack notification on write-tool usage |
| `_redact_args()` | `shared/opa_middleware.py:631` | Argument redaction for audit logging |

**Integration points:**
- `shared/mcp_http.py:420` - applies OPAMiddleware in configure_http_transport
- `crowdstrike/proxy.py:173` - directly applies OPAMiddleware to proxy

**Test coverage:**
- `scripts/test_opa_filtering.py` - integration tests

**Primary count:** 9 functions in `shared/opa_middleware.py`

---

## Scoring Guide

For each tool tested, score results against this ground truth:
- **Precision** = (relevant results returned) / (total results returned)
- **Recall** = (ground truth items found) / (total ground truth items)
- For conceptual queries, only score against **primary** results. Secondary results are bonus.
