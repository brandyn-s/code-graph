---
name: code-graph-exploring
description: >
  This skill should be used when the user asks to "explore the codebase",
  "understand the architecture", "what functions exist", "show me the structure",
  "how is the code organized", "find functions matching", "search for classes",
  "list all routes", "show API endpoints", or needs codebase orientation.
---

# Codebase Exploration via Knowledge Graph

Use graph tools for structural code questions. They return precise results in ~500 tokens vs ~80K for grep-based exploration.

## Workflow

### Step 1: Check if project is indexed

```
list_projects
```

If the project is missing from the list:

```
index_repository(repo_path="/path/to/project")
```

If already indexed, skip — auto-sync keeps the graph fresh.

### Step 2: Get a structural overview

```
get_graph_schema
```

This returns node label counts (functions, classes, routes, etc.), edge type counts, and relationship patterns. Use it to understand what's in the graph before querying.

### Step 3: Find specific code elements

Find functions by name pattern:
```
search_graph(label="Function", name_pattern=".*Handler.*")
```

Find classes:
```
search_graph(label="Class", name_pattern=".*Service.*")
```

Find all REST routes:
```
search_graph(label="Route")
```

Find modules/packages:
```
search_graph(label="Module")
```

Scope to a specific directory:
```
search_graph(label="Function", qn_pattern=".*services\\.order\\..*")
```

### Step 4: Read source code

After finding a function via search, read its source:
```
get_code_snippet(qualified_name="project.path.to.FunctionName")
```

### Step 5: Understand structure

For file and directory exploration, use the host's native tools — `Glob` for
paths and `Read` for contents. The graph indexes structural elements, not the
filesystem, so there is no MCP tool for directory listing.

## When to Use Grep Instead

- Searching for **string literals** or error messages → `search_code` or Grep
- Finding a file by exact name → Glob
- The graph doesn't index text content, only structural elements

## Key Tips

- Results default to 10 per page. Check `has_more` and use `offset` to paginate.
- Use `project` parameter when multiple repos are indexed.
- Route nodes have a `properties.handler` field with the actual handler function name.
- `exclude_labels` removes noise (e.g., `exclude_labels=["Route"]` when searching by name pattern).

## Examples

**Example 1: orienting in an unfamiliar repo**
User says: "How is this codebase organized?"
Actions:
1. `list_projects` — the repo is already indexed, so skip indexing (Step 1).
2. `get_graph_schema` — returns label counts: 1,240 Function, 96 Class,
   38 Route, 210 Module (Step 2).
3. `search_graph(label="Module")` to see the package layout.
Result: a structural map in roughly 500 tokens, without reading a file.

**Example 2: finding the request handlers**
User says: "Show me the API endpoints."
Actions:
1. `search_graph(label="Route")` — returns route nodes.
2. Read `properties.handler` on each to get the handling function name.
3. `get_code_snippet(qualified_name="api.orders.create_order")` for the one
   that matters (Step 4).
Result: endpoints plus their handlers, paginated 10 at a time via `offset`.

**Example 3: a question the graph should NOT answer**
User says: "Where does the string `payment declined` come from?"
Actions:
1. Recognize this as a string-literal search, not a structural one.
2. Route to `search_code` or the host's `Grep` instead of `search_graph`.
Result: the graph indexes structure, not text content — using it here would
return nothing and read as absence.

## Success Criteria

- The project is confirmed present via `list_projects` before any query; a
  missing project is indexed rather than queried against an empty graph.
- Structural questions go to graph tools; string-literal and filename
  questions are routed to `search_code`, `Grep`, or `Glob` instead.
- `has_more` is checked and `offset` used whenever results could exceed one
  page, so a 10-result default is never mistaken for a complete answer.
- The `project` parameter is passed when more than one repo is indexed.
- File and directory reads use the host's native `Read` and `Glob`; no MCP
  directory-listing tool is assumed to exist.
- Source is read with `get_code_snippet` on a qualified name recovered from a
  search, not guessed.
