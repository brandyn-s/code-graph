---
name: code-graph-reference
description: >
  This skill should be used when the user asks about "code-graph tools",
  "graph query syntax", "Cypher query examples", "edge types",
  "how to use search_graph", "query_graph examples", or needs reference
  documentation for the codebase knowledge graph tools.
---

# code-graph MCP — Tool Reference

## Tools

The exposed set depends on the configured toolset: the `core` toolset
registers 26 tools and `full` registers more. Ask the live surface
rather than trusting a count here — the authoritative list is whatever
`mcp__code-graph__*` tools your session actually has.

The table below documents the most-used subset, not the whole toolset.

| Tool | Purpose |
|------|---------|
| `index_repository` | Parse and ingest repo into graph (only once — auto-sync keeps it fresh) |
| `index_status` | Check indexing status (ready/indexing/not found) |
| `list_projects` | List all indexed projects with timestamps and counts |
| `delete_project` | Remove a project from the graph |
| `search_graph` | Structured search with filters (name, label, degree, file pattern) |
| `search_code` | Grep-like text search within indexed project files |
| `trace_call_path` | BFS call chain traversal (exact name match required). Supports `risk_labels=true` for impact classification. |
| `detect_changes` | Map git diff to affected symbols + blast radius with risk scoring |
| `query_graph` | Cypher-like graph queries (200-row cap) |
| `get_graph_schema` | Node/edge counts, relationship patterns |
| `get_code_snippet` | Read source code by qualified name |
| `ingest_traces` | Ingest OpenTelemetry traces to validate HTTP_CALLS edges |

For file reads and directory listings use the host's native `Read` and
`Glob`. `read_file` and `list_directory` are NOT MCP tools — they exist
only as a CLI subcommand and inside the localization agent's own loop.

## Edge Types

| Type | Meaning |
|------|---------|
| `CALLS` | Direct function call within same service |
| `HTTP_CALLS` | Synchronous cross-service HTTP request |
| `ASYNC_CALLS` | Async dispatch (Cloud Tasks, Pub/Sub, SQS, Kafka) |
| `IMPORTS` | Module/package import |
| `DEFINES` / `DEFINES_METHOD` | Module/class defines a function/method |
| `HANDLES` | Route node handled by a function |
| `IMPLEMENTS` | Type implements an interface |
| `OVERRIDE` | Struct method overrides an interface method |
| `USAGE` | Read reference (callback, variable assignment) |
| `FILE_CHANGES_WITH` | Git history change coupling |
| `CONTAINS_FILE` / `CONTAINS_FOLDER` / `CONTAINS_PACKAGE` | Structural containment |

## Node Labels

`Project`, `Package`, `Folder`, `File`, `Module`, `Class`, `Function`, `Method`, `Interface`, `Enum`, `Type`, `Route`

## Qualified Name Format

`<project>.<path_parts>.<name>` — file path with `/` replaced by `.`, extension removed.

Examples:
- `myproject.cmd.server.main.HandleRequest` (Go)
- `myproject.services.orders.ProcessOrder` (Python)
- `myproject.src.components.App.App` (TypeScript)

Use `search_graph` to discover qualified names, then pass them to `get_code_snippet`.

## Cypher Subset (for query_graph)

**Supported:**
- `MATCH` with node labels and relationship types
- Variable-length paths: `-[:CALLS*1..3]->`
- `WHERE` with `=`, `<>`, `>`, `<`, `>=`, `<=`, `=~` (regex), `CONTAINS`, `STARTS WITH`
- `WHERE` with `AND`, `OR`, `NOT`
- `RETURN` with property access, `COUNT(x)`, `DISTINCT`
- `ORDER BY` with `ASC`/`DESC`
- `LIMIT`
- Edge property access: `r.confidence`, `r.url_path`, `r.coupling_score`

**Not supported:** `WITH`, `COLLECT`, `SUM`, `CREATE/DELETE/SET`, `OPTIONAL MATCH`, `UNION`

## Common Cypher Patterns

```
# Cross-service HTTP calls with confidence
MATCH (a)-[r:HTTP_CALLS]->(b) RETURN a.name, b.name, r.url_path, r.confidence LIMIT 20

# Filter by URL path
MATCH (a)-[r:HTTP_CALLS]->(b) WHERE r.url_path CONTAINS '/orders' RETURN a.name, b.name

# Interface implementations
MATCH (s)-[r:OVERRIDE]->(i) RETURN s.name, i.name LIMIT 20

# Change coupling
MATCH (a)-[r:FILE_CHANGES_WITH]->(b) WHERE r.coupling_score >= 0.5 RETURN a.name, b.name, r.coupling_score

# Functions calling a specific function
MATCH (f:Function)-[:CALLS]->(g:Function) WHERE g.name = 'ProcessOrder' RETURN f.name LIMIT 20
```

## Regex-Powered Search (No Full-Text Index Needed)

`search_graph` and `search_code` support full Go regex, making full-text search indexes unnecessary. Regex patterns provide precise, composable queries that cover all common discovery scenarios:

### search_graph — name_pattern / qn_pattern

| Pattern | Matches | Use case |
|---------|---------|----------|
| `.*Handler$` | names ending in Handler | Find all handlers |
| `(?i)auth` | case-insensitive "auth" | Find auth-related symbols |
| `get\|fetch\|load` | any of three words | Find data-loading functions |
| `^on[A-Z]` | names starting with on + uppercase | Find event handlers |
| `.*Service.*Impl` | Service...Impl pattern | Find service implementations |
| `^(Get\|Set\|Delete)` | CRUD prefixes | Find CRUD operations |
| `.*_test$` | names ending in _test | Find test functions |
| `.*\\.controllers\\..*` | qn_pattern for directory scoping | Scope to controllers dir |

### search_code — regex=true

| Pattern | Matches | Use case |
|---------|---------|----------|
| `TODO\|FIXME\|HACK` | multi-pattern scan | Find tech debt markers |
| `(?i)password\|secret\|token` | case-insensitive secrets | Security scan |
| `func\\s+Test` | Go test functions | Find test entry points |
| `api[._/]v[0-9]` | API version references | Find versioned API usage |
| `import.*from ['"]@` | scoped npm imports | Find package imports |

### Combining Filters for Surgical Queries

```
# Find unused auth handlers
search_graph(name_pattern="(?i).*auth.*handler.*", max_degree=0, exclude_entry_points=true)

# Find high fan-out functions in the services directory
search_graph(qn_pattern=".*\\.services\\..*", min_degree=10, relationship="CALLS", direction="outbound")

# Find all route handlers matching a URL pattern
search_code(pattern="(?i)(POST|PUT).*\\/api\\/v[0-9]\\/orders", regex=true)
```

## Critical Pitfalls

1. **`search_graph(relationship="HTTP_CALLS")` does NOT return edges** — it filters nodes by degree. Use `query_graph` with Cypher to see actual edges.
2. **`query_graph` has a 200-row cap** before aggregation — COUNT queries silently undercount on large codebases. Use `search_graph` with `min_degree`/`max_degree` for counting.
3. **`trace_call_path` needs exact names** — use `search_graph(name_pattern=".*Partial.*")` first to discover names.
4. **`direction="outbound"` misses cross-service callers** — use `direction="both"` for full context.

## Decision Matrix

| Question | Use |
|----------|-----|
| Who calls X? | `trace_call_path(direction="inbound")` |
| What does X call? | `trace_call_path(direction="outbound")` |
| Full call context | `trace_call_path(direction="both")` |
| Find by name pattern | `search_graph(name_pattern="...")` |
| Dead code | `search_graph(max_degree=0, exclude_entry_points=true)` |
| Cross-service edges | `query_graph` with Cypher |
| Impact of local changes | `detect_changes()` |
| Risk-classified trace | `trace_call_path(risk_labels=true)` |
| Text search | `search_code` or Grep |

## Examples

**Example 1: picking the right tool for an edge question**
User says: "Show me every cross-service HTTP call with its URL."
Actions:
1. Check the Decision Matrix: this needs *edges with properties*, not a node
   count.
2. Reject `search_graph(relationship="HTTP_CALLS")` — it filters nodes by
   degree and returns no edges (Critical Pitfalls).
3. Use `query_graph` with Cypher over `HTTP_CALLS`, returning the URL property.
Result: the right tool on the first call, because the pitfall is documented.

**Example 2: a qualified name that will not resolve**
User says: `get_code_snippet` returned `status='ambiguous'`.
Actions:
1. Re-read Qualified Name Format — a short name matches many nodes.
2. Recover the full QN from the prior `search_graph` result.
3. Re-call with the exact QN, or pass `auto_resolve` when 2 or fewer
   candidates and the best match is acceptable.
Result: an exact read instead of a guess between candidates.

**Example 3: a tool that is not on this surface**
User says: "Use `list_directory` to show me src/."
Actions:
1. Note that `read_file` and `list_directory` are not MCP tools here.
2. Probe the live surface if unsure, rather than trusting any list.
3. Use the host's `Glob` and `Read` instead.
Result: no call against a tool that does not exist, which would return an
error that reads like an empty directory.

## Success Criteria

- Tool choice is made from the Decision Matrix, and no node-degree filter is
  used to answer a question about edges.
- Cypher usage stays inside the documented subset; unsupported clauses are not
  attempted.
- Qualified names come from a prior search result; an `ambiguous` status is
  resolved by supplying the full QN rather than retrying the short name.
- Tool availability is confirmed against the live `mcp__code-graph__*` surface,
  not against any count or table in this file.
- Documented pitfalls are checked before a query is called broken.
- This table is treated as a most-used subset; a tool missing from it is not
  concluded to be absent from the server.
