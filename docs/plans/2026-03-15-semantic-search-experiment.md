# EXP-008: Semantic Code Search Empirical Testing

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Empirically test semantic code search candidates alongside codebase-memory-mcp to determine if adding a semantic layer meaningfully improves "find code that does X" queries.

**Architecture:** Install each candidate as a separate MCP server, index the mcp-servers repo (~2,935 nodes, ~15K LOC Python), run 10 standardized queries (5 structural, 5 conceptual), and measure precision, recall, token cost, and latency. Compare single-tool results against combined codebase-memory-mcp + candidate results.

**Tech Stack:** Python (candidates are Python-based), MCP stdio transport, SQLite (local storage), HuggingFace/local embeddings

**Test repo:** `C:/Users/user/Documents/GitHub/mcp-servers` (already indexed by codebase-memory-mcp)

**Decision threshold:** >60% accuracy on conceptual queries with <5s latency. If no candidate meets this, the gap doesn't justify the complexity.

---

## Pre-work: Define the Ground Truth

Before testing any tool, establish ground truth answers for all 10 queries by manually identifying the correct results. This prevents bias from accepting whatever a tool returns as "correct."

### The 10 Test Queries

**Structural queries (codebase-memory-mcp should excel):**

| # | Query | Expected tool | Ground truth (verify manually) |
|---|-------|---------------|-------------------------------|
| S1 | "What calls `_build_oauth`?" | trace_call_path | All callers of shared/mcp_http.py:_build_oauth |
| S2 | "Find dead code - functions nobody calls" | search_graph(max_degree=0) | Functions with 0 inbound CALLS edges |
| S3 | "Show all HTTP routes" | search_graph(label='Route') | All Route nodes |
| S4 | "Trace callers of `check_permissions`" | trace_call_path | Inbound call chain for slack-user/slack_user_mcp.py:check_permissions |
| S5 | "What's the blast radius of changing shared/mcp_http.py?" | detect_changes | All downstream symbols |

**Conceptual queries (semantic search should excel):**

| # | Query | What we're looking for | Ground truth (verify manually) |
|---|-------|----------------------|-------------------------------|
| C1 | "Find authentication logic" | Functions that enforce auth - OPA middleware, OAuth flows, token validation | shared/opa_middleware.py, shared/mcp_http.py:_build_oauth, confluence auth, etc. |
| C2 | "Where do we handle errors?" | Error handling patterns - try/except blocks, error response formatting | Various error handlers across services |
| C3 | "Show retry patterns" | Functions with retry/backoff logic | httpx retry config, API retry wrappers |
| C4 | "Find rate limiting code" | Rate limit enforcement or configuration | Any rate limit middleware or config |
| C5 | "How is OPA authorization enforced?" | OPA policy check flow - middleware, policy loading, decision points | shared/opa_middleware.py, OPA-related functions |

---

### Task 1: Establish Ground Truth

**Files:**
- Create: `C:/Users/user/Documents/GitHub/codebase-memory-mcp/docs/plans/exp-008-ground-truth.md`

**Step 1: Run codebase-memory-mcp queries for structural baselines**

Run each structural query using the existing indexed data. Record exact results (function names, file paths, counts).

```bash
# These are MCP tool calls, not CLI commands
# S1: trace_call_path(function_name='_build_oauth', direction='inbound', project='c-Users-user-Documents-GitHub-mcp-servers')
# S2: search_graph(label='Function', relationship='CALLS', direction='inbound', max_degree=0, exclude_entry_points=true, project='c-Users-user-Documents-GitHub-mcp-servers')
# S3: search_graph(label='Route', project='c-Users-user-Documents-GitHub-mcp-servers')
# S4: trace_call_path(function_name='check_permissions', direction='inbound', project='c-Users-user-Documents-GitHub-mcp-servers')
# S5: detect_changes on shared/mcp_http.py (use git diff simulation)
```

**Step 2: Manually identify ground truth for conceptual queries**

For each conceptual query (C1-C5), grep the mcp-servers repo to find the actual relevant functions/files. Record as ground truth.

```bash
# C1: Authentication logic
grep -rn "authenticate\|authorize\|opa\|oauth\|token\|login\|permission" C:/Users/user/Documents/GitHub/mcp-servers/ --include="*.py" -l

# C2: Error handling
grep -rn "except\|raise\|error_response\|handle_error" C:/Users/user/Documents/GitHub/mcp-servers/ --include="*.py" -l

# C3: Retry patterns
grep -rn "retry\|backoff\|max_retries\|tenacity" C:/Users/user/Documents/GitHub/mcp-servers/ --include="*.py" -l

# C4: Rate limiting
grep -rn "rate.limit\|throttle\|ratelimit" C:/Users/user/Documents/GitHub/mcp-servers/ --include="*.py" -l

# C5: OPA authorization
grep -rn "opa\|policy\|authorize\|decision" C:/Users/user/Documents/GitHub/mcp-servers/ --include="*.py" -l
```

**Step 3: Write ground truth document**

Record all results in `exp-008-ground-truth.md` with:
- Query ID, query text, expected results (file:function pairs), total count
- For conceptual queries: mark which results are "primary" (directly relevant) vs "secondary" (tangentially relevant)

**Step 4: Commit**

```bash
git add docs/plans/exp-008-ground-truth.md
git commit -m "docs: EXP-008 ground truth for semantic search experiment"
```

---

### Task 2: Install and Test Candidate 1 - cocoindex-code

**Rationale:** Lowest installation barrier. `pipx install` + one command. 520+ GitHub stars. Claims 70% token savings.

**Files:**
- Create: `C:/Users/user/Documents/GitHub/codebase-memory-mcp/docs/plans/exp-008-results-cocoindex.md`

**Step 1: Install cocoindex-code**

```bash
pip install cocoindex-code
```

If pip fails (dependency conflicts with existing packages), try:
```bash
pipx install cocoindex-code
```

**Step 2: Verify the binary is available**

```bash
cocoindex-code --help
# or
which cocoindex-code
```

**Step 3: Register as MCP server**

Add to `~/.claude.json` via Python script (don't use `claude mcp add` inside a session):

```python
import json
config_path = "C:/Users/user/.claude.json"
with open(config_path, encoding="utf-8") as f:
    config = json.load(f)

# Find the cocoindex-code binary path
import shutil
binary = shutil.which("cocoindex-code")
if not binary:
    # Try pipx location
    binary = "C:/Users/user/.local/bin/cocoindex-code"

config.setdefault("mcpServers", {})["cocoindex-code"] = {
    "type": "stdio",
    "command": binary,
    "args": [],
    "env": {}
}

with open(config_path, "w", encoding="utf-8") as f:
    json.dump(config, f, indent=2)

print(f"Registered cocoindex-code at {binary}")
```

**Step 4: Restart Claude Code and verify the tool loads**

After restart, check that cocoindex-code tools appear in available tools.

**Step 5: Build the index on mcp-servers**

```
# MCP tool call:
# cocoindex-code build_index or equivalent indexing command
# Target: C:/Users/user/Documents/GitHub/mcp-servers
```

If cocoindex-code doesn't have an explicit index command, it auto-indexes on first search. Run a test query to trigger indexing.

**Step 6: Run all 10 queries and record results**

For each query (S1-S5, C1-C5):
1. Run the query through cocoindex-code's search tool
2. Record: results returned, latency, relevance to ground truth
3. Score: precision (% of returned results that are relevant) and recall (% of ground truth results found)

Record in `exp-008-results-cocoindex.md`:

```markdown
# cocoindex-code Results

## Installation
- Method: pip/pipx
- Version: X.X.X
- Index time: Xs
- Index size: X MB

## Structural Queries

### S1: "What calls _build_oauth?"
- Results: [list]
- Precision: X/Y relevant
- Recall: X/Y ground truth found
- Latency: Xs
- Notes:

[repeat for S2-S5]

## Conceptual Queries

### C1: "Find authentication logic"
- Results: [list]
- Precision: X/Y relevant
- Recall: X/Y ground truth found
- Latency: Xs
- Notes:

[repeat for C2-C5]

## Summary
- Structural accuracy: X%
- Conceptual accuracy: X%
- Average latency: Xs
- Meets threshold (>60% conceptual, <5s latency): YES/NO
```

**Step 7: Commit results**

```bash
git add docs/plans/exp-008-results-cocoindex.md
git commit -m "docs: EXP-008 cocoindex-code test results"
```

---

### Task 3: Install and Test Candidate 2 - mcp-code-search

**Rationale:** Most feature-complete hybrid (AST + call graphs + embeddings). SQLite-vec for local storage. No cloud dependencies. 1.2GB model download on first run.

**Files:**
- Create: `C:/Users/user/Documents/GitHub/codebase-memory-mcp/docs/plans/exp-008-results-mcp-code-search.md`

**Step 1: Install mcp-code-search**

```bash
pip install mcp-code-search
```

Or via uv:
```bash
uv tool install mcp-code-search
```

**Step 2: Register as MCP server**

Add to `~/.claude.json`:

```python
import json
config_path = "C:/Users/user/.claude.json"
with open(config_path, encoding="utf-8") as f:
    config = json.load(f)

config.setdefault("mcpServers", {})["mcp-code-search"] = {
    "type": "stdio",
    "command": "C:/Users/user/AppData/Local/Programs/Python/Python312/pythonw.exe",
    "args": ["-m", "mcp_code_search"],
    "env": {
        "MCP_CS_PROJECT_ROOT": "C:/Users/user/Documents/GitHub/mcp-servers"
    }
}

with open(config_path, "w", encoding="utf-8") as f:
    json.dump(config, f, indent=2)
```

Note: Use `pythonw.exe` for stdio MCP servers on Windows (per platform-constraints.md). The `MCP_CS_PROJECT_ROOT` env var must be set explicitly since pythonw doesn't inherit user env vars.

**Step 3: Restart Claude Code, verify tool loads, wait for model download**

First run downloads ~1.2GB embedding model (intfloat/multilingual-e5-large-instruct). This may take several minutes. Check that indexing completes.

**Step 4: Run all 10 queries and record results**

Same format as Task 2. Record in `exp-008-results-mcp-code-search.md`.

**Step 5: Commit results**

```bash
git add docs/plans/exp-008-results-mcp-code-search.md
git commit -m "docs: EXP-008 mcp-code-search test results"
```

---

### Task 4: Combined Mode Testing

**Rationale:** The research consensus is that graph + embeddings + grep outperforms any single approach. Test whether combining codebase-memory-mcp with the best-performing candidate improves results.

**Files:**
- Create: `C:/Users/user/Documents/GitHub/codebase-memory-mcp/docs/plans/exp-008-results-combined.md`

**Step 1: For each conceptual query (C1-C5), run combined retrieval**

Pattern:
1. First query codebase-memory-mcp's `search_graph` and `search_code` for structural matches
2. Then query the best semantic candidate for conceptual matches
3. Merge and deduplicate results
4. Score combined precision/recall against ground truth

**Step 2: For each structural query (S1-S5), test if semantic search adds value**

Run the semantic candidate on S1-S5. Check if it finds anything codebase-memory-mcp missed (unlikely but worth checking).

**Step 3: Record combined results**

In `exp-008-results-combined.md`:

```markdown
# Combined Mode Results

## Best semantic candidate: [name]

## Conceptual Queries - Graph Only vs Combined

| Query | Graph-only precision | Graph-only recall | Combined precision | Combined recall | Delta |
|-------|---------------------|-------------------|-------------------|-----------------|-------|
| C1 | X% | X% | X% | X% | +X% |
| ... |

## Structural Queries - Graph Only vs Combined

| Query | Graph-only precision | Semantic adds value? |
|-------|---------------------|---------------------|
| S1 | X% | YES/NO (what was added) |
| ... |

## Verdict
- Does semantic search meet the >60% conceptual accuracy threshold? YES/NO
- Does combining improve over graph-only? YES/NO by how much?
- Is the latency acceptable (<5s)? YES/NO
- Recommendation: ADOPT / SKIP / MONITOR
```

**Step 4: Commit**

```bash
git add docs/plans/exp-008-results-combined.md
git commit -m "docs: EXP-008 combined mode results and verdict"
```

---

### Task 5: Decision and Cleanup

**Step 1: Write the experiment summary**

Based on all results, write a one-page summary in `docs/plans/exp-008-summary.md`:
- Which candidate performed best
- Whether the >60% threshold was met
- Whether to adopt, and if so, which tool
- Configuration needed for permanent installation
- Any caveats or limitations discovered

**Step 2: If adopting - update codebase-graph.md rule**

Add the semantic search tool to the routing table in `~/.claude/rules/codebase-graph.md`:

```markdown
| "Find code that does X" / conceptual search | `cocoindex_search(query='X')` | Grep for keywords |
```

**Step 3: If not adopting - document why**

Update the experiment backlog in the community intelligence report with the negative result and reasoning.

**Step 4: Clean up**

- Remove any candidate that didn't meet the threshold from `~/.claude.json`
- Delete test indexes if they consume significant disk space
- Commit all results and push
