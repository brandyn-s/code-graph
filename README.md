# code-graph

Persistent code knowledge graph MCP server for Claude Code. Structural analysis via tree-sitter AST parsing with Cypher-like query language.

Originally forked from [DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp). Substantially extended - security surface analysis, STIG evidence queries, sensitive data flow tracing, index health monitoring, lockfile parsing, OPA policy linking, and Terraform env var cross-referencing.

## What It Does

Indexes codebases into a persistent knowledge graph (SQLite) that survives session restarts. One graph query returns what would take dozens of grep/Glob calls - precise structural results in ~500 tokens vs ~80K tokens for file-by-file exploration.

- **64 languages**: Python, Go, JavaScript, TypeScript, Rust, Java, C/C++/C#, Nix, HCL, and 54 more via tree-sitter
- **Call graph**: Resolves function calls across files and packages (import-aware, type-inferred)
- **Cypher queries**: `MATCH (f:Function)-[:CALLS]->(g) WHERE f.name = 'main' RETURN g.name`
- **Architecture overview**: Languages, packages, entry points, routes, hotspots, boundaries, clusters in a single call
- **Git diff impact**: Maps uncommitted changes to affected symbols + blast radius with risk classification
- **Cross-service HTTP linking**: Discovers REST routes (FastAPI, Gin, Express) and matches to call sites
- **Dead code detection**: Functions with zero callers, excluding entry points and framework-decorated functions
- **Auto-sync**: Background polling detects file changes and triggers incremental re-indexing
- **Security surfaces**: Query authentication, authorization, crypto, and input validation patterns
- **STIG evidence**: Map STIG/SRG controls to code-level implementation evidence
- **Data flow tracing**: Track sensitive data paths through function call chains
- **Single binary, zero infrastructure**: No Docker, no external databases, no API keys

## MCP Tools

| Tool | Purpose |
|------|---------|
| `index_repository` | Index a repo into the graph (incremental, content-hash based) |
| `list_projects` | Show all indexed projects with node/edge counts |
| `delete_project` | Remove a project and all its graph data |
| `index_status` | Index stats and status for a project |
| `index_health` | Graph coverage report - parse failures, missing edges, stale files |
| `search_graph` | Structured search with filters (case-insensitive by default) |
| `search_code` | Grep-like text search within indexed files |
| `query_graph` | Execute Cypher-like graph queries (read-only) |
| `trace_call_path` | BFS call chain traversal with optional risk classification |
| `trace_data_flow` | Track sensitive data paths through function calls |
| `detect_changes` | Git diff to affected symbols + blast radius |
| `get_architecture` | Codebase orientation - languages, packages, hotspots, clusters, ADR |
| `manage_adr` | CRUD for Architecture Decision Records |
| `get_graph_schema` | Node/edge counts, relationship patterns, sample names |
| `get_code_snippet` | Read source code for a function by qualified name |
| `query_security_surfaces` | Find auth, crypto, and input validation patterns |
| `query_stig_evidence` | Map STIG controls to code-level evidence |
| `ingest_traces` | Import OpenTelemetry traces for HTTP_CALLS validation |

## Setup

Pre-built binary at `~/bin/codebase-memory-mcp.exe`. MCP server name: `code-graph`.

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

Releases are built via `workflow_dispatch` on `release.yml` with a `version` input (e.g. `v0.5.0-redacted.4`). Download from [redacted releases](https://github.com/redacted-org/code-graph/releases).

## Development

```bash
# Build
CGO_ENABLED=1 go build -o bin/codebase-memory-mcp.exe ./cmd/codebase-memory-mcp/

# Test
go test ./... -count=1

# Lint (golangci-lint v2.10)
golangci-lint run ./...

# Format
gofmt -w .
```

Requires Go 1.26+ and a C compiler (tree-sitter uses CGO). On Windows: MSYS2 UCRT64 shell with `mingw-w64-ucrt-x86_64-gcc`.

## Architecture

```
cmd/codebase-memory-mcp/       Entry point (MCP stdio server + CLI mode + install/update)
  assets/skills/               4 task-specific skills (exploring, tracing, quality, reference)
internal/
  store/                       SQLite graph storage (WAL mode, Louvain clustering)
  lang/                        Language specs (64 languages, tree-sitter node types)
  cbm/                         Vendored tree-sitter C grammars, AST extraction, Go LSP hybrid
  pipeline/                    Multi-pass indexing (structure -> definitions -> calls -> HTTP -> OPA -> communities)
  httplink/                    Cross-service HTTP route/call-site matching
  cypher/                      Cypher query lexer, parser, planner, executor
  tools/                       MCP tool handlers (18 tools) + CLI dispatch
  watcher/                     Background auto-sync (mtime+size polling, adaptive intervals)
  discover/                    File discovery with .gitignore, .cbmignore, symlink handling
  fqn/                         Qualified name computation
  traces/                      OpenTelemetry trace ingestion
  selfupdate/                  GitHub release checking + binary swap
```

SQLite database persists at `~/.cache/codebase-memory-mcp/`. Reset with `rm -rf ~/.cache/codebase-memory-mcp/`.

## Graph Data Model

**Node labels**: `Project`, `Package`, `Folder`, `File`, `Module`, `Class`, `Function`, `Method`, `Interface`, `Enum`, `Type`, `Route`, `EnvVar`

**Edge types**: `CONTAINS_PACKAGE`, `CONTAINS_FOLDER`, `CONTAINS_FILE`, `DEFINES`, `DEFINES_METHOD`, `IMPORTS`, `CALLS`, `HTTP_CALLS`, `ASYNC_CALLS`, `IMPLEMENTS`, `HANDLES`, `USAGE`, `CONFIGURES`, `WRITES`, `MEMBER_OF`, `TESTS`, `USES_TYPE`, `FILE_CHANGES_WITH`, `POLICY_GATES`, `READS_ENV`

**Security properties**: Nodes tagged with `security_role` (auth_boundary, input_entry_point, sensitive_sink, crypto_operation, privilege_escalation, session_management, audit_logging) and `security_subtype` (http_handler, cli_entry, sql_query, shell_exec, file_write, encryption, hashing, signing, etc.)

## Protected Repo

PR required to merge to main. Always use `--repo redacted-org/code-graph` with `gh` CLI.

## License

MIT (inherited from upstream)
