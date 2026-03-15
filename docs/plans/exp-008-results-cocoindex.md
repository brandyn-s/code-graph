# cocoindex-code Results (EXP-008)

## Installation

- **Package:** cocoindex-code 0.1.14
- **Method:** pip install
- **Model:** sentence-transformers/all-MiniLM-L6-v2 (22M params, ~90MB)
- **Index time:** 57s (initial), 0.10s (incremental, nothing changed)
- **Index size:** 1,110 chunks across 104 files
- **Storage:** SQLite + sqlite-vec in `.cocoindex_code/` directory

## Critical Finding: Lazy Model Load Stalls MCP

The MCP server stalls on first query because:

1. `SentenceTransformerEmbedder()` constructor is fast (0.00s) — does NOT load model weights
2. First `embed()` call triggers weight loading: **17.23s** on this machine
3. MCP server startup runs `_initial_index()` in background, which triggers first embed
4. Until that completes, all queries blocked by `_initial_index_done` event
5. Claude Code's MCP client aborts before the 17s elapses → "AbortError"

**Once warmed up**, queries take 0.01-0.02s. The tool is fast after initialization.

**Fix:** Add model pre-warm in `shared.py` (e.g., `embedder.embed("warmup", None)` at import time) or patch `server.py` to eagerly load before accepting connections.

## Structural Queries

### S1: "What calls `_build_oauth`?"

| # | File:Lines | Score | Relevant? |
|---|-----------|-------|-----------|
| 1 | confluence/confluence_fedramp_mcp.py:181-221 | 0.494 | No (uses OAuth, doesn't call _build_oauth) |
| 2 | msgraph/msgraph_mcp.py:723-761 | 0.428 | No |
| 3 | confluence/confluence_fedramp_mcp.py:238-279 | 0.422 | No |
| 4-8 | docs, scripts, CLAUDE.md | 0.367-0.411 | No (docs/unrelated) |
| **9** | **shared/mcp_http.py:376-406** | **0.367** | **Yes — actual call site** |
| 10 | confluence/confluence_fedramp_mcp.py:372-394 | 0.363 | No |

- **Precision:** 1/10 = 10%
- **Recall:** 1/1 = 100% (found the one caller, but ranked #9)
- **Latency:** 17.23s (model load) → subsequent would be 0.02s
- **Verdict:** Semantic search cannot answer "what calls X" — this is a call-graph query

### S2: "Find dead code - functions nobody calls"

| # | File:Lines | Score | Relevant? |
|---|-----------|-------|-----------|
| 1-10 | test files, scripts, docs, tool functions | 0.260-0.351 | No |

- **Precision:** 0/10 = 0%
- **Recall:** N/A (no ground truth baseline)
- **Latency:** 0.02s
- **Verdict:** Semantic search returns test code and scripts, not dead code. This requires graph analysis (in-degree = 0).

### S3: "Show all HTTP routes"

| # | File:Lines | Score | Relevant? |
|---|-----------|-------|-----------|
| 1 | slack-connect/slack_connect_app.py:37-54 | 0.367 | Partial (imports, not route defs) |
| 2 | tavily/tavily_mcp.py:316-358 | 0.357 | No (MCP tool, not HTTP route) |
| 3 | claude-proxy/claude_proxy.py:490-536 | 0.329 | Partial (config, not route table) |
| 4-10 | netcloud, README, scripts, tailscale | 0.305-0.325 | No |

- **Precision:** 0/10 = 0% (no result points to actual Route() definitions)
- **Recall:** 0/13 = 0%
- **Latency:** 0.01s
- **Verdict:** Semantic search doesn't understand "HTTP routes" as Starlette Route() objects. This is a label/type query.

### S4: "Trace callers of `check_permissions`"

| # | File:Lines | Score | Relevant? |
|---|-----------|-------|-----------|
| 1-10 | msgraph, confluence, opa_middleware, test scenarios | 0.342-0.445 | No (none are callers of check_permissions) |

- **Precision:** 0/10 = 0%
- **Recall:** 0/0 = N/A (ground truth: 0 code-level callers exist)
- **Latency:** 0.02s
- **Verdict:** Returns permission-related code but can't trace call relationships

### S5: "Blast radius of changing `shared/mcp_http.py`"

| # | File:Lines | Score | Relevant? |
|---|-----------|-------|-----------|
| 1 | github/test_github.py:1-12 | 0.493 | No (test file) |
| 2 | claude-compliance/test_claude_compliance.py:1-16 | 0.443 | No (test) |
| 3 | shared/mcp_http.py:313-350 | 0.404 | Partial (the file itself) |
| 4 | README.md | 0.399 | No |
| 5 | shared/mcp_http.py:30-65 | 0.397 | Partial (the file itself) |
| 6-8 | docs, tenable header | 0.382-0.395 | Weak |
| 9 | netcloud/netcloud_mcp.py:358-420 | 0.381 | No (doesn't import mcp_http) |
| 10 | github/test_github.py:7-54 | 0.379 | No |

- **Precision:** 0/10 = 0% (found the file itself but no importers)
- **Recall:** 0/15 = 0% (none of the 15 importing services found)
- **Latency:** 0.02s
- **Verdict:** Dependency analysis is a graph query, not semantic

## Conceptual Queries

### C1: "Find authentication logic"

| # | File:Lines | Score | Ground truth? |
|---|-----------|-------|---------------|
| 1 | claude-proxy/claude_proxy.py:662-689 | 0.403 | Bonus (API key auth) |
| 2 | msgraph/msgraph_mcp.py:2517-2552 | 0.392 | Bonus (msgraph auth) |
| 3 | security-remix/evals/test_scenarios.py:408-451 | 0.383 | No (test) |
| 4 | CLAUDE.md:100-116 | 0.378 | No (docs) |
| 5 | confluence/confluence_fedramp_mcp.py:1-49 | 0.365 | Secondary |
| **6** | **shared/opa_middleware.py:574-612** | **0.361** | **Primary (_extract_identity)** |
| 7 | README.md | 0.341 | No (docs) |
| 8 | msgraph/msgraph_mcp.py:538-580 | 0.341 | Bonus |
| 9 | confluence/confluence_fedramp_mcp.py:181-221 | 0.337 | Secondary |
| **10** | **shared/opa_middleware.py:1105-1150** | **0.325** | **Bonus (_filter_tools_list)** |

- **Precision:** 5/10 = 50% (5 results are auth-related code)
- **Recall:** 1/8 = 12.5% primary (found _extract_identity; missed _build_oauth, _authorize_tool_call, OPAMiddleware.__call__, etc.)
- **Latency:** 0.01s
- **Notes:** Finds auth code broadly but misses core functions. Too much doc/README noise.

### C2: "Where do we handle errors?"

| # | File:Lines | Score | Ground truth? |
|---|-----------|-------|---------------|
| 1-3 | scripts/security-and-compliance-audit.py | 0.381-0.460 | Partial (has try/except) |
| 4 | claude-proxy/claude_proxy.py:2358-2368 | 0.370 | Partial |
| 5 | docs/plans | 0.366 | No (docs) |
| 6 | claude-proxy/README.md | 0.357 | No (docs) |
| 7 | lever/lever_mcp.py:92-125 | 0.354 | Partial |
| 8 | scripts/security-and-compliance-audit.py | 0.341 | Partial |
| 9 | docs/plans | 0.338 | No (docs) |
| 10 | lucid/lucid_mcp.py:167-179 | 0.333 | Partial |

- **Precision:** 4/10 = 40% (found files with error handling)
- **Recall:** 0/4 = 0% primary (missed _jsonrpc_error, ToolError pattern, HTTPException)
- **Latency:** 0.01s
- **Notes:** Found generic error handling but not the custom error infrastructure

### C3: "Show retry patterns"

| # | File:Lines | Score | Ground truth? |
|---|-----------|-------|---------------|
| **1** | **claude-proxy/claude_proxy.py:1196-1235** | **0.593** | **Primary (_retry_delay + _is_retryable_status)** |
| 2 | claude-proxy/claude_proxy.py:2358-2368 | 0.513 | Relevant (retry config) |
| 3 | claude-proxy/README.md | 0.486 | No (docs) |
| 4-5 | claude-proxy retry sections | 0.471-0.480 | **Primary (retry loop area)** |
| 6 | claude-proxy/claude_proxy.py:2109-2147 | 0.388 | Primary (near retry loop) |
| 7-8 | docs/plans | 0.387-0.388 | No (docs) |
| 9 | claude-proxy/claude_proxy.py:909-966 | 0.377 | No (rate limit, not retry) |
| 10 | security-remix test | 0.375 | No |

- **Precision:** 4/10 = 40%
- **Recall:** 2/5 = 40% (found _retry_delay + retry loop; missed tavily 429, github merge retry)
- **Latency:** 0.02s
- **Notes:** Best structural query result. Found the primary retry file. Missed cross-service patterns.

### C4: "Find rate limiting code"

| # | File:Lines | Score | Ground truth? |
|---|-----------|-------|---------------|
| **1** | **claude-proxy/claude_proxy.py:884-914** | **0.520** | **Primary (check_rate_limit + _rate_limit_overrides)** |
| 2 | claude-proxy/claude_proxy.py:1787-1830 | 0.400 | Relevant |
| **3** | **claude-proxy/claude_proxy.py:1329-1388** | **0.370** | **Primary (rate limit metrics)** |
| 4 | claude-proxy/claude_proxy.py:1196-1235 | 0.362 | No (retry, not rate limit) |
| 5-10 | airlock, security-remix, README, shared, tavily | 0.319-0.331 | No/weak |

- **Precision:** 3/10 = 30%
- **Recall:** 2/6 = 33% (found check_rate_limit area + metrics; missed Redis window detail, headers)
- **Latency:** 0.02s
- **Notes:** Found the main rate limiting file and functions

### C5: "How is OPA authorization enforced?"

| # | File:Lines | Score | Ground truth? |
|---|-----------|-------|---------------|
| **1** | **shared/opa_middleware.py:1-37** | **0.605** | **Right file, header only** |
| 2 | CLAUDE.md:36-47 | 0.567 | No (docs) |
| 3 | confluence/confluence_fedramp_mcp.py:1-49 | 0.490 | Secondary (uses OPA) |
| 4 | README.md | 0.488 | No (docs) |
| 5 | docs/plans | 0.467 | No (design docs) |
| **6** | **shared/opa_middleware.py:1105-1150** | **0.457** | **Primary (_filter_tools_list)** |
| 7 | docs/plans | 0.434 | No |
| 8 | shared/mcp_http.py:204-230 | 0.425 | Secondary (OPA integration) |
| 9 | docs/plans | 0.417 | No |
| 10 | shared/mcp_http.py:167-201 | 0.412 | Secondary (OPA config) |

- **Precision:** 4/10 = 40% (opa_middleware sections + mcp_http integration)
- **Recall:** 1/9 = 11% primary (only _filter_tools_list; missed _authorize_tool_call, OPAMiddleware class, etc.)
- **Latency:** 0.01s
- **Notes:** Found the right file but not the implementation functions. Too much doc noise.

## Summary

| Metric | Structural (S1-S5) | Conceptual (C1-C5) |
|--------|--------------------|--------------------|
| Avg Precision | 2% | 40% |
| Avg Recall | 20% (S1 only) | 19% |
| Avg Latency | 0.02s (post-warmup) | 0.01s |
| Meets >60% conceptual threshold | — | **NO (19% avg recall)** |
| Meets <5s latency threshold | YES (post-warmup) | YES |

### Key Observations

1. **Structural queries are fundamentally unsuitable** for semantic search (2% precision). Call graph, dependency, and dead-code queries need graph traversal, not embedding similarity.

2. **Conceptual queries find the right files** but struggle with function-level precision. For C3 (retry) and C4 (rate limiting), it found the correct primary file (claude-proxy). For C5 (OPA), it found the correct file but only the header, not the implementation functions.

3. **Document noise is significant.** README.md, CLAUDE.md, and docs/plans/ appear in 7/10 query results, diluting code results. A `paths` filter for `*.py` files would help but shouldn't be required.

4. **Post-warmup latency is excellent** at 0.01-0.02s per query.

5. **The MCP stall is a fixable bug**, not a fundamental limitation. Eager model loading would make it work in MCP context.

### Verdict

**Does NOT meet the >60% conceptual accuracy threshold.** Conceptual recall averages 19%. The tool finds related code regions but misses most specific ground truth functions. For the "find code that does X" use case, it provides directional guidance (right file) but not precise answers (right function).
