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

### Code Localization & Ranking Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `rank_by_query` | `internal/tools/rank.go` | Bidirectional weighted PageRank seeded on query-matched nodes; returns top-K most relevant entities. Best for SPECIFIC SYMBOL queries. (Aider repo-map pattern) |
| `code_localize` | `internal/tools/localize.go` | LocAgent BFS-style graph-guided localization: seed-match + bidirectional BFS over CALLS/DEFINES/IMPORTS/CONTAINS edges. Best for SPECIFIC SYMBOL queries. Primitives-only, deterministic, ~50ms. (LocAgent ACL 2025) |
| `code_localize_agent` | `internal/tools/localize_agent.go` | LLM-driven LocAgent variant: wraps the primitives in a multi-turn agent loop. Best for VERBOSE natural-language issues where the issue talks about A but the fix is in B. ~30-60s wall, ~$0.04-0.05/query at Haiku 4.5. Requires `ANTHROPIC_API_KEY`. |

> **Pick by query shape**: short symbol names → `rank_by_query` / `code_localize`. Multi-paragraph natural-language issue → `code_localize_agent`. Both primitive tools accept `seed_strategy`: `substring` (legacy), `embedding` (Voyage cosine), or `hybrid` (default; substring + embedding deduped, falls back to substring if no `VOYAGE_API_KEY`).

#### Measured Loc-Bench accuracy (n=16, structured scorer, 2026-04-25)

| Mode | File | Class | Func |
|------|------|-------|------|
| substring-primitives | 38% | 6% | 12% |
| hybrid-primitives | 44% | 6% | 19% |
| `code_localize_agent` (default config) | **94%** | **50%** | **88%** |

For comparison: LocAgent (ACL 2025, arXiv 2503.09089) reports 92.7% file-level on the full 560-instance Loc-Bench V1. We exceed that on this subset; full-benchmark validation is separate work.

#### Agent loop env vars (all opt-out — defaults are the measured-best config)

| Env var | Default | Purpose |
|---------|---------|---------|
| `LOCAGENT_PROMPT_VARIANT` | `open` | `open` encourages `read_file` for verification; `aggressive` reverts to a tighter 5-turn budget |
| `LOCAGENT_BFS_DEPTH` | `4` | BFS depth for `code_localize` inside the agent loop |
| `LOCAGENT_MAX_TURNS` | `20` | Hard cap; `open` prompt soft-targets 8 turns |
| `LOCAGENT_REWRITE` | unset | Set to `1` to enable a Haiku pre-step that extracts focused search terms. Measured to **regress** results on n=16; available for further experimentation |
| `EMBEDDING_SEED_MIN_COSINE` | `0.0` | Minimum cosine similarity for embedding seeds. PR #84 set this to 0.65 based on n=1; PR #91 reverted after n=16 showed the threshold filtered useful seeds |
| `ANTHROPIC_MODEL` | `claude-haiku-4-5-20251001` | Override to opt into Opus 4.7. Head-to-head (PR #90) showed Opus matches Haiku at ~8x cost — opt-in only |

### Pipeline Additions
- OPA policy linker (`POLICY_GATES` edges connecting policy to enforced code)
- Terraform env var cross-referencing (`EnvVar` graph nodes)
- Lockfile parsing (dependency graph from package managers)
- Security tagging pass (labels nodes as auth/crypto/input/hardware_io)
- LRU query cache for `search_graph` and `query_graph`

## Test Conventions

### Zero-value filter activation

When adding a filter field to `SearchParams` (or any struct where callers use `{}` literal construction), the filter MUST activate only on explicitly-set values, not the zero value. Use `> 0` / `!= ""` / `len(x) > 0` as the gate — never `>= 0` on an int field whose zero means "off".

Every new filter field needs a test with `SearchParams{}` default-constructed that asserts the filter is inactive. One line, catches the class of bug that PR #61 hit (MinComplexity=0 from zero-value activated the filter and broke snippet tests).

```go
func TestSearchParamsZeroValue_<field>_Inactive(t *testing.T) {
    // With SearchParams{}, <field> is zero; filter must be OFF.
    results, err := store.Search(ctx, SearchParams{Query: "x"})
    // ... assert results match baseline (filter not applied)
}
```

## Protected Repo

PR required to merge to main. Use `--repo redacted-org/code-graph` with `gh` CLI.
