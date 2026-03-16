# code-graph

Structural code knowledge graph MCP server. Indexes codebases into a persistent graph (functions, classes, call relationships, HTTP links) queryable via 14 MCP tools. Sub-millisecond queries, 99.2% fewer tokens than file-by-file exploration.

Originally forked from [DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp). Single Go binary, tree-sitter parsing, SQLite WAL storage.

## redacted Additions

- **Nix flake.lock parser** - extracts flake inputs as InfraFile nodes with source metadata, creates DEPENDS_ON edges for supply chain visibility
- **Security role tagging** - post-index enrichment tags nodes as `auth_boundary`, `input_entry_point`, `sensitive_sink`, or `crypto_operation` via regex pattern matching
- **`query_security_surfaces` tool** - returns security-tagged nodes with caller/callee counts and STIG control mapping (AC-3, SI-10, SC-13)

## Setup

Binary: `~/bin/codebase-memory-mcp.exe` (downloaded from [redacted releases](https://github.com/redacted-org/code-graph/releases))

MCP config in `~/.claude.json`:

```json
{
  "mcpServers": {
    "code-graph": {
      "type": "stdio",
      "command": "C:/Users/user/bin/codebase-memory-mcp.exe"
    }
  }
}
```

Binary replacement after new release: download zip, swap `~/bin/codebase-memory-mcp.exe`, restart Claude Code.

## MCP Tools

### Indexing

| Tool | Description |
|------|-------------|
| `index_repository` | Index a repo into the graph. Auto-sync keeps it fresh after initial index. |
| `list_projects` | List all indexed projects with node/edge counts. |
| `delete_project` | Remove a project and all graph data. Irreversible. |

### Querying

| Tool | Description |
|------|-------------|
| `search_graph` | Structured search with filters (label, name pattern, file pattern, degree, relationships). Case-insensitive by default. |
| `query_graph` | Cypher-like graph queries (read-only). `MATCH (f:Function)-[:CALLS]->(g) WHERE f.name = 'main' RETURN g.name` |
| `trace_call_path` | BFS traversal from/to a function. Supports `risk_labels=true` for impact classification. |
| `detect_changes` | Map git diff to affected graph symbols + blast radius with risk classification. |
| `get_architecture` | Codebase overview: languages, packages, entry points, routes, hotspots, layers, Louvain clusters. |
| `manage_adr` | CRUD for Architecture Decision Records persisted across sessions. |
| `get_code_snippet` | Read source code by qualified name (reads from disk). |
| `get_graph_schema` | Node/edge counts, relationship patterns, sample names. |
| `search_code` | Grep-like text search within indexed project files. |
| `query_security_surfaces` | Security-tagged nodes with STIG control mapping (redacted addition). |
| `ingest_traces` | OpenTelemetry trace ingestion for HTTP_CALLS validation. |

### Companion: code-search (semantic)

`code-graph` handles structural queries ("what calls X?", "blast radius", "dead code"). For conceptual queries ("find auth logic", "where do we handle errors"), use the `code-search` MCP server (Voyage AI embeddings + BM25 hybrid). The `/code-explore` skill routes between them automatically.

## Common Queries

```
# Find functions matching a pattern
search_graph(label="Function", name_pattern=".*handler")

# Trace callers of a function
trace_call_path(function_name="ProcessOrder", direction="inbound", depth=3)

# Dead code detection
search_graph(label="Function", relationship="CALLS", direction="inbound", max_degree=0, exclude_entry_points=true)

# Cypher query
query_graph(query="MATCH (f:Function)-[:CALLS]->(g) WHERE f.name = 'main' RETURN g.name LIMIT 20")

# Git diff impact analysis
detect_changes(scope="staged", depth=3)

# Architecture overview
get_architecture(aspects=["all"])

# Security surfaces (redacted)
query_security_surfaces(project="psm")
```

## Graph Data Model

### Node Labels

`Project`, `Package`, `Folder`, `File`, `Module`, `Class`, `Function`, `Method`, `Interface`, `Enum`, `Type`, `Route`

### Edge Types

`CONTAINS_PACKAGE`, `CONTAINS_FOLDER`, `CONTAINS_FILE`, `DEFINES`, `DEFINES_METHOD`, `IMPORTS`, `CALLS`, `HTTP_CALLS`, `ASYNC_CALLS`, `IMPLEMENTS`, `HANDLES`, `USAGE`, `CONFIGURES`, `WRITES`, `MEMBER_OF`, `TESTS`, `USES_TYPE`, `FILE_CHANGES_WITH`

### Supported Cypher Subset

Supported: `MATCH` with labels and relationship types, variable-length paths (`*1..3`), `WHERE` with `=`, `<>`, `>`, `<`, `=~` (regex), `CONTAINS`, `STARTS WITH`, `AND`/`OR`/`NOT`, `RETURN` with `COUNT`/`DISTINCT`, `ORDER BY`, `LIMIT`. Results capped at 200 rows.

Not supported: `WITH`, `COLLECT`, `SUM`, `CREATE`/`DELETE`/`SET` (read-only), `OPTIONAL MATCH`, `UNION`.

## Performance

| Operation | Time |
|-----------|------|
| Fresh index (49K nodes) | ~6s |
| Incremental reindex | ~1.2s |
| Cypher query | <1ms |
| Name search (regex) | <10ms |
| Dead code detection | ~150ms |
| Trace call path (depth=5) | <10ms |

64 languages supported. Average 76% MCP score across all languages in standardized benchmarks. See [`BENCHMARK.md`](BENCHMARK.md) for details.

## Storage

SQLite database at `~/.cache/codebase-memory-mcp/codebase-memory.db`. Persists across restarts (WAL mode). Reset: `rm -rf ~/.cache/codebase-memory-mcp/`.

## Development

```bash
make build    # Build binary to bin/
make test     # Run all tests
make lint     # Run golangci-lint
```

Requires Go 1.26+ and a C compiler (tree-sitter uses CGO).

## License

MIT
