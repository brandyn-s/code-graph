# CLAUDE.md

redacted fork of codebase-memory-mcp. Persistent code knowledge graph MCP server with security extensions.

## Key Commands

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

## Architecture

- **Graph storage**: SQLite WAL mode at `~/.cache/codebase-memory-mcp/`. Louvain community detection for clustering.
- **Parsing**: tree-sitter AST for 64 languages via vendored C grammars (CGO). Go gets enhanced LSP-style type resolution.
- **Pipeline**: Multi-pass indexing (structure -> definitions -> calls -> HTTP links -> OPA policy -> communities -> tests)
- **Cypher engine**: Custom lexer/parser/planner/executor. Read-only subset with variable-length paths.
- **Auto-sync**: Background watcher polls mtime+size, triggers incremental reindex. Adaptive polling intervals.
- **Security tools**: `query_security_surfaces` (auth/crypto/input patterns), `query_stig_evidence` (control -> code mapping), `trace_data_flow` (sensitive data paths)
- **Skills**: 4 embedded skills (exploring, tracing, quality, reference) installed via `codebase-memory-mcp install`

## Testing

```bash
# All tests
go test ./... -count=1

# Language parity (125+ cases)
go test ./internal/pipeline/ -run TestLangParity -v

# AST structure (90+ cases)
go test ./internal/pipeline/ -run TestASTDump -v

# Integration
go test ./internal/pipeline/ -run TestPipeline -v
```

## redacted Additions (beyond upstream)

### Security & Compliance Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `query_security_surfaces` | `internal/tools/security.go` | Auth, crypto, input validation patterns with taint analysis (source subtypes, sanitizer nodes) |
| `trace_data_flow` | `internal/tools/dataflow.go` | Sensitive data path analysis with env var propagation tracking |
| `query_stig_evidence` | `internal/tools/stig_evidence.go` | STIG control → code evidence mapping |
| `index_health` | `internal/tools/health.go` | Graph coverage and quality report |

### Service Understanding Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `explain_service` | `internal/tools/explain_service.go` | Service-level architecture summary (dependencies, endpoints, config) |
| `service_map` | `internal/tools/service_map.go` | Cross-service dependency map with noise filtering |
| `diff_services` | `internal/tools/diff_services.go` | Compare two services structurally |

> **Scope note**: These tools were built for Corsair's microservice architecture. They work best on repos with clear service boundaries (separate crates/packages, HTTP endpoints, config modules). Single-service or monolith repos get limited value.

### Developer Productivity Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `get_affected_tests` | `internal/tools/affected_tests.go` | Find tests impacted by a code change |
| `detect_cycles` | `internal/tools/cycles.go` | Circular dependency detection with noise filtering |
| `explain_symbol` | `internal/tools/explain.go` | Explain what a symbol does with callers/callees/context |
| `get_change_coupling` | `internal/tools/change_coupling.go` | Files that historically co-change (from git history) |

### Review & Context Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `get_review_context` | `internal/tools/review_context.go` | PR review context: what a change touches and what depends on it |
| `get_relevant_context` | `internal/tools/relevant_context.go` | Graph-based file context selection for LLM agents (callers, callees, tests, coupled files) |
| `visualize` | `internal/tools/visualize.go` | HTML graph visualization of node neighborhoods |

### Pipeline Additions
- OPA policy linker (`POLICY_GATES` edges connecting policy to enforced code)
- Terraform env var cross-referencing (`EnvVar` graph nodes)
- Lockfile parsing (dependency graph from package managers)
- Security tagging pass (labels nodes as auth/crypto/input/hardware_io)
- LRU query cache for `search_graph` and `query_graph`

## Protected Repo

PR required to merge to main. Use `--repo redacted-org/code-graph` with `gh` CLI.
