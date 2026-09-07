---
name: code-graph-quality
description: >
  This skill should be used when the user asks about "dead code",
  "find dead code", "detect dead code", "show dead code", "dead code analysis",
  "unused functions", "find unused functions", "unreachable code",
  "identify high fan-out functions", "find complex functions",
  "code quality audit", "find functions nobody calls",
  "reduce codebase size", "refactor candidates", "cleanup candidates",
  or needs code quality analysis.
---

# Code Quality Analysis via Knowledge Graph

Use graph degree filtering to find dead code, high-complexity functions, and refactor candidates — all in single tool calls.

## Workflow

### Dead Code Detection

Find functions with zero inbound CALLS edges, excluding entry points:

```
search_graph(
  label="Function",
  relationship="CALLS",
  direction="inbound",
  max_degree=0,
  exclude_entry_points=true
)
```

`exclude_entry_points=true` removes route handlers, `main()`, and framework-registered functions that have zero callers by design.

### Verify Dead Code Candidates

Before deleting, verify each candidate truly has no callers:

```
trace_call_path(function_name="SuspectFunction", direction="inbound", depth=1)
```

Also check for read references (callbacks, stored in variables):

```
query_graph(query="MATCH (a)-[r:USAGE]->(b) WHERE b.name = 'SuspectFunction' RETURN a.name, a.file_path LIMIT 10")
```

### High Fan-Out Functions (calling 10+ others)

These are often doing too much and are refactor candidates:

```
search_graph(
  label="Function",
  relationship="CALLS",
  direction="outbound",
  min_degree=10
)
```

### High Fan-In Functions (called by 10+ others)

These are critical functions — changes have wide impact:

```
search_graph(
  label="Function",
  relationship="CALLS",
  direction="inbound",
  min_degree=10
)
```

### Files That Change Together (Hidden Coupling)

Find files with high git change coupling:

```
query_graph(query="MATCH (a)-[r:FILE_CHANGES_WITH]->(b) WHERE r.coupling_score >= 0.5 RETURN a.name, b.name, r.coupling_score, r.co_change_count ORDER BY r.coupling_score DESC LIMIT 20")
```

High coupling between unrelated files suggests hidden dependencies.

### Unused Imports

```
search_graph(
  relationship="IMPORTS",
  direction="outbound",
  max_degree=0,
  label="Module"
)
```

## Key Tips

- `search_graph` with degree filters has no row cap (unlike `query_graph` which caps at 200).
- Use `file_pattern` to scope analysis to specific directories: `file_pattern="**/services/**"`.
- Dead code detection works best after a full index — run `index_repository` if the project was recently set up.
- Paginate results with `limit` and `offset` — check `has_more` in the response.

## Examples

**Example 1: dead code, then verified before deletion**
User says: "Find dead code we can delete."
Actions:
1. `search_graph(label="Function", relationship="CALLS", direction="inbound",
   max_degree=0, exclude_entry_points=true)` — 23 candidates.
2. For each, `trace_call_path(function_name=..., direction="inbound",
   depth=1)` to confirm no callers.
3. `query_graph` for `USAGE` edges to catch callbacks and functions stored in
   variables, which have no CALLS edge.
Result: 23 candidates became 9 safe deletions. The 14 that survived were
reachable through a callback or a route the CALLS view does not show.

**Example 2: choosing a refactor target**
User says: "What should we break up first?"
Actions:
1. `search_graph(label="Function", relationship="CALLS",
   direction="outbound", min_degree=10)` for fan-out.
2. Cross-check fan-in (`direction="inbound"`) — a high-fan-out function that
   is also high-fan-in is riskier to change than one nothing calls.
3. `get_code_snippet` on the top candidate to size the work.
Result: the target is chosen from measured connectivity, not from a hunch
about which file feels messy.

**Example 3: a hidden dependency**
User says: "Why do these two folders always change together?"
Actions:
1. Query change coupling to get file pairs that co-occur in commits.
2. Confirm with `search_graph`/`query_graph` whether a structural edge exists.
3. Coupling with no edge is the finding: an implicit contract, not an import.
Result: a shared serialization format nothing declared, which is why no
import graph showed it.

## Success Criteria

- `exclude_entry_points=true` is set on every dead-code query, so route
  handlers, `main()`, and framework-registered functions are not reported as
  unreachable.
- No deletion is recommended from a `max_degree=0` result alone; each candidate
  is verified with `trace_call_path` AND a `USAGE`-edge check for callbacks and
  variable references.
- Fan-out findings are paired with fan-in before a refactor is proposed, so
  blast radius is part of the recommendation.
- Change-coupling findings state whether a structural edge also exists; a
  coupling with no edge is reported as an implicit contract.
- Counts name what they measured — inbound vs outbound, and which labels were
  excluded — rather than being presented as a bare total.
