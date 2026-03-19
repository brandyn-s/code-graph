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

| Tool | Source File | Purpose |
|------|-----------|---------|
| `trace_data_flow` | `internal/tools/dataflow.go` | Sensitive data path analysis |
| `index_health` | `internal/tools/health.go` | Graph coverage and quality report |
| `query_security_surfaces` | `internal/tools/security.go` | Auth, crypto, input validation patterns |
| `query_stig_evidence` | `internal/tools/stig_evidence.go` | STIG control to code evidence mapping |

Pipeline additions: OPA policy linker (`POLICY_GATES` edges), Terraform env var cross-referencing, lockfile parsing.

## Protected Repo

PR required to merge to main. Use `--repo redacted-org/code-graph` with `gh` CLI.
