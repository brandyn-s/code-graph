# EXP-008: Semantic Code Search — Experiment Summary

## Decision: SKIP (do not adopt either candidate)

Neither candidate meets the >60% conceptual accuracy threshold required for adoption.

## Results Overview

| Candidate | Status | Conceptual Accuracy | Structural Accuracy | Latency | Verdict |
|-----------|--------|--------------------|--------------------|---------|---------|
| cocoindex-code 0.1.14 | Functional (with workaround) | 19% avg recall | 2% avg precision | 0.01-0.02s (post-warmup) | Below threshold |
| code-search-mcp 0.6.1 | Failed | N/A | N/A | N/A | Disqualified |

## What We Learned

### 1. Semantic search finds files, not functions

cocoindex-code consistently identified the correct *file* for conceptual queries (e.g., `claude-proxy/claude_proxy.py` for retry/rate-limit, `shared/opa_middleware.py` for OPA) but rarely pinpointed specific *functions*. Chunk-level granularity (20-40 line blocks) means the answer is "somewhere in this chunk" not "this function on this line."

### 2. Structural queries are fundamentally out of scope

"What calls X?", "find dead code", "show all routes", "blast radius of file Y" — these require graph traversal (call graphs, import graphs, label queries). Semantic similarity cannot answer them. codebase-memory-mcp's graph tools are the right approach for these.

### 3. Document noise is a significant problem

README.md, CLAUDE.md, and docs/plans/ files appeared in 7/10 query result sets, occupying 30-50% of slots. A production integration would need to filter to `*.py` by default, but even with that filter, the function-level recall was low.

### 4. The MCP integration has a fixable startup bug

cocoindex-code's `SentenceTransformerEmbedder` lazy-loads model weights (~17s) on first `embed()` call. The MCP server blocks queries until initial indexing completes, but Claude Code's MCP client times out before the 17s elapses. Fix: eager model load at import time. This is a contributor PR opportunity if we revisit.

### 5. code-search-mcp is not viable on Windows

Chinese-language tool descriptions, cp1252 encoding crashes, missing Database methods. Package quality is pre-alpha.

## Recommendations

1. **Do not install either candidate** as a permanent MCP server.
2. **Keep codebase-memory-mcp as the sole code search tool.** Its graph-based approach (call paths, dependency analysis, dead code detection) covers structural queries that semantic search cannot touch.
3. **For conceptual "find code that does X" queries**, use Grep with semantic keywords as the primary approach, supplemented by codebase-memory-mcp's `search_code` for file-level discovery. This combination already achieves better results than cocoindex-code's embedding search.
4. **Revisit in 6 months.** The semantic code search space is maturing rapidly. cocoindex-code's architecture is sound (local embeddings, sqlite-vec, incremental indexing) — it just needs better chunk granularity and a model warmup fix.

## Cleanup

- [x] cocoindex-code: remove from `~/.claude.json` after experiment
- [x] code-search-mcp: already uninstalled
- [ ] Delete `.cocoindex_code/` directory from mcp-servers repo if disk space matters
- [ ] Consider filing upstream issue for cocoindex-code model warmup bug
