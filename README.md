# code-graph

A persistent code knowledge graph MCP server: ask what calls a symbol, what it
calls, what implements or overrides it, and what breaks if it changes, and get
back answers tied to exact source locations.

`code-graph` indexes a repository with tree-sitter into a per-project SQLite
graph and exposes it to any MCP client (Claude Code, Codex, Cursor, Windsurf,
Zed, and more). What sets it apart is the evidence contract: every relationship
carries its resolver, confidence, and source range; the index is bound to one
exact checkout and refuses to emit evidence when the working tree has drifted;
and an optional compiler-index tier (SCIP) upgrades heuristic edges to
compiler-resolved ones per document. It is the structural half of a pair with
[code-search](https://github.com/brandyn-s/code-search), which handles
"where is the code that does X?" discovery.

Originally forked from
[DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp)
(MIT). This fork adds security and compliance analysis, service and
change-impact tools, compiler-index ingestion, coherent source identity,
relationship evidence, and bounded cross-project operations.

## Install

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/brandyn-s/code-graph/main/install.sh | bash
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/brandyn-s/code-graph/main/install.ps1 | iex
```

With Go 1.26+ and a C compiler:

```bash
go install github.com/brandyn-s/code-graph/cmd/code-graph@latest
```

Homebrew tap and Nix flake instructions are in [docs/install.md](docs/install.md).

The installer downloads the release archive for your platform, verifies its
SHA-256 against the release's `checksums.txt`, installs `code-graph` into
`~/.local/bin`, and prints the MCP registration line. If the GitHub CLI is
installed and logged in it also verifies build provenance; for the fully
verified path see [`scripts/setup.sh`](scripts/setup.sh). Nothing else is
required: no database service, no API key.

To verify a downloaded archive by hand against the
[releases page](https://github.com/brandyn-s/code-graph/releases):
`gh release verify-asset TAG PATH -R brandyn-s/code-graph` checks immutable
release membership and `gh attestation verify PATH -R brandyn-s/code-graph`
checks the SLSA build provenance. Every public release from `v0.9.0` on ships
both.

## Why this and not grep or the upstream

Every relationship comes back with how it was resolved, how sure the resolver
is, and which exact checkout it was observed in. This is a real
`get_relationship_evidence` result from the repository's own Go fixture,
trimmed to the fields that matter:

```json
{
  "relation_type": "calls",
  "source_symbol_ref": {"qualified_name": "demo.main.entry", "relative_path": "main.go", "start_line": 9, "end_line": 12},
  "target_symbol_ref": {"qualified_name": "demo.main.leaf", "relative_path": "main.go", "start_line": 14, "end_line": 16},
  "resolution_source": "lsp_direct",
  "confidence_tier": "extracted",
  "confidence_band": "high",
  "numeric_confidence": 0.95,
  "source_revision": "7173d7d839b4cd62e886c51b121e39131b2b0adf",
  "index_generation": "03c21a84d85b3d041b49e6f345eef90e795e7b3afb6b76315a3654900895c3f3",
  "runtime_observed": false,
  "relationship_ref": {"id": "rel:v1:577634562c66c9c5f6cc6fb45dbbfdc9910080a857d1ea2477cde646e875f180"}
}
```

Edit one line of `main.go` and ask again, and the same tool refuses instead of
answering from a graph that no longer matches the tree:

```json
{
  "identity_status": "stale_source",
  "identity_reason": "source_changed_since_index: dirty_fingerprint(indexed=\"clean\", current=\"84d9ed7c…\"); re-run index_repository",
  "_metadata": {"evidence_refs": {"emitted": false, "reason": "stale_source:source_changed_since_index"}},
  "relationships": [],
  "total": 0
}
```

| Situation | code-graph | grep / ripgrep | codebase-memory-mcp (upstream) |
|---|---|---|---|
| Heuristic edge (tree-sitter resolved a call by name) | Edge with `resolution_source`, `confidence_tier: extracted`, band, and a stable `relationship_ref` you can cite | Text matches; you decide which are calls | Edge, no resolver or confidence attached |
| Compiler-index edge (SCIP tier enabled for Go/TypeScript) | Same edge upgraded to compiler-resolved, with the artifact digest that proves it | Not available | Not available |
| Working tree changed after indexing | Evidence tools fail closed with `stale_source` and the fingerprint diff | Always searches the current text, never the indexed one | Answers from the stale graph |

## Connect a client

```bash
claude mcp add code-graph --scope user -- ~/.local/bin/code-graph
```

or, for any MCP client that takes JSON:

```json
{
  "mcpServers": {
    "code-graph": { "command": "/home/you/.local/bin/code-graph" }
  }
}
```

`code-graph install` auto-configures Claude Code, Codex CLI, Cursor, Windsurf,
Gemini CLI, VS Code, and Zed in one step. Per-client snippets are in
[docs/clients.md](docs/clients.md).

## Index, verify, ask

```text
index_repository(repo_path="/absolute/repository", skip_report=true)
index_status(project="<project-name>")
trace_call_path(project="<project-name>", function_name="authenticate", direction="inbound", depth=2)
```

`list_projects` returns canonical project names. Wait for `index_status` to
report a captured, live-matching index identity before relying on results.
The same tools are available from the shell:

```bash
code-graph cli index_repository '{"repo_path":"/absolute/repository","skip_report":true}'
code-graph cli --raw list_projects | jq .
```

## Tools

| Tool | What it does |
|---|---|
| `index_repository`, `index_status`, `index_health` | Build the graph (full or incremental) and inspect identity, freshness, precision tier, coverage |
| `list_projects`, `delete_project`, `compare_project_indexes` | Manage per-project indexes and diff two of them |
| `search_graph`, `search_code`, `search_code_semantic` | Find nodes by name/label/path, grep source text, or search semantically (needs `VOYAGE_API_KEY`) |
| `query_graph` | Run a read-only Cypher subset against the graph |
| `get_code_snippet`, `explain_symbol`, `get_graph_schema` | Read a symbol's source and its callers/callees; inspect node and edge types |
| `trace_call_path`, `trace_data_flow` | Inbound/outbound call BFS with confidence filters; CALLS/READS/WRITES reachability or a CodeQL handoff |
| `get_relationship_evidence` | One edge with resolver, confidence, runtime observations, and immutable references |
| `detect_changes`, `get_affected_tests`, `get_review_context`, `get_relevant_context` | Git diff to affected symbols, tests, and a token-bounded review summary |
| `rank_by_query`, `code_localize`, `code_localize_agent`, `find_similar_functions`, `degree_filter` | Ranking and localization: PageRank seeds, deterministic or LLM-driven localization, dead code and hubs |
| `get_architecture`, `explain_service`, `service_map`, `diff_services`, `detect_cycles`, `get_change_coupling`, `diff_graph` | Architecture: packages, services and their dependencies, cycles, co-change, graph deltas between revisions |
| `query_security_surfaces`, `query_stig_evidence`, `find_rationale` | Auth/input/crypto sinks, control-to-code evidence, WHY/SAFETY/TODO annotations |
| `localize_across_projects`, `ingest_traces`, `generate_report`, `manage_adr`, `visualize` | Cross-project discovery, OpenTelemetry ingestion, reports, ADRs, HTML graph views |

By default the server advertises the **core** set of 26 tools (listed
below); set `CODE_GRAPH_TOOLSET=full` to advertise all 40. Fewer
advertised tools means less schema in every request and better tool selection
by agents. Every tool stays available to `code-graph cli` regardless.

Core: `index_repository`, `index_status`, `index_health`, `list_projects`, `delete_project`, `compare_project_indexes`, `search_graph`, `search_code`, `query_graph`, `get_graph_schema`, `get_code_snippet`, `explain_symbol`, `degree_filter`, `trace_call_path`, `trace_data_flow`, `get_relationship_evidence`, `detect_changes`, `get_review_context`, `get_architecture`, `code_localize`, `localize_across_projects`, `query_security_surfaces`, `query_stig_evidence`, `generate_report`, `ingest_traces`, `manage_adr`.

Export the exact registered schema with `go run ./cmd/export-tool-schemas`
(`--core` prints the core set).

## Configuration

All settings are environment variables read at startup. Advanced tuning
variables (`LOCAGENT_*`, `RESOLVER_*`, `CBM_*`, embeddings timeouts, heap
limits) are documented in [CLAUDE.md](CLAUDE.md#resolver-and-runtime-env-vars).

| Variable | Default | Effect |
|---|---|---|
| `VOYAGE_API_KEY` | unset | Enables Voyage embeddings for `search_code_semantic`, `find_similar_functions`, and embedding-seeded ranking. Without it code-graph runs fully offline and logs `embeddings disabled` once. |
| `CODE_GRAPH_SKIP_EMBEDDINGS` | auto | `1` forces embeddings off even with a key; `0` forces the passes on. |
| `CODE_GRAPH_CACHE_DIR` | `~/.cache/code-graph` | Where per-project SQLite databases live. Existing `~/.cache/codebase-memory-mcp` directories from earlier releases are used automatically when the new path does not exist. |
| `CODE_GRAPH_SERVICE_MAP` | `~/.config/code-graph/service_map.json` if present | JSON `{"domain": ["pattern", ...]}` table that `service_map` and `diff_services` use to group services into domains. See [docs/service-map.md](docs/service-map.md). |
| `CODE_GRAPH_NIX_SERVICE_OPTION_PREFIX` | `services` | Option-set prefix for Nix service extraction (`options.<prefix>.<name>`). Set e.g. `acme.services` for namespaced modules. |
| `CODE_GRAPH_NIX_PKGS_PREFIX` | `pkgs` | Package-set prefix for detecting the binary a Nix service runs (`${<prefix>.<pkg>}/bin/<binary>`). |
| `CODE_GRAPH_UPDATE_CHANNEL` | `stable` | `rc` lets `code-graph update` install release candidates; stable ignores them. |
| `CODE_GRAPH_TOOLSET` | `core` | `core` advertises the 26 tools listed under Tools; `full` advertises all 40 over MCP. |
| `CODE_GRAPH_LOG_FILE`, `CODE_GRAPH_LOG_FILE_ONLY` | unset | Tee or redirect structured logs to a file. |
| `ANTHROPIC_API_KEY` | unset | Only used by `code_localize_agent`. |

Persistent settings such as the memory limit are managed with
`code-graph config`. When something looks wrong, run `code-graph doctor` (or
`code-graph doctor --json`) and paste the output into your issue: it prints the
resolved configuration with secrets redacted, the cache directory and every
project database with its size and format version, the embeddings mode, and
the compiled grammar list. Per-project options such as the precision tier and
`skip_report` are passed to `index_repository` and remembered.

## Going deeper

- [Architecture and operating model](docs/ARCHITECTURE.md): pipeline, storage, identity, failure semantics.
- [Precision tiers](docs/precision-tiers.md): what the heuristic graph can and cannot resolve, and how SCIP upgrades it.
- [Verifiable evidence](docs/evidence.md): the reference schema shared with code-search and the CodeQL import boundary.
- [Measured evidence](docs/measured-evidence.md): precision/recall against oracles, large-repository resource figures, and their limits.
- [Boundaries and tradeoffs](docs/boundaries.md): where an LSP, Sourcegraph, or CodeQL is the better tool.
- [Client setup](docs/clients.md), [service map format](docs/service-map.md), [CodeQL evidence import](docs/codeql-evidence-import.md).
- [Combined HTML guide](docs/index.html) for code-search and code-graph together.

Supported languages (26 in release builds): Python, JavaScript, TypeScript, TSX, Go, Rust, Java,
C, C++, CUDA, Bash, PowerShell, Nix, HTML, CSS, SCSS, YAML, TOML, HCL, SQL,
Dockerfile, JSON, XML, Markdown, Makefile, CMake, Protobuf. Tree-sitter
supplies syntax; relationship quality varies by language and is not a
compiler-precision guarantee outside the SCIP tier. CUDA is compiled in only with `make build-all` (`-tags cbm_all`); its grammar is the largest and rarest, and default builds report `.cu` files as unsupported rather than skipping them silently.

## Development

Requires Go 1.26+ and a C compiler for the vendored tree-sitter grammars.

```bash
make build            # bin/code-graph
go test ./... -count=1
golangci-lint run ./...
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for structure, tests, and the release
process, [docs/extending.md](docs/extending.md) for adding a tool, language,
edge type, or embedding provider, [docs/upstream.md](docs/upstream.md) for the
relationship to the upstream project, [SECURITY.md](SECURITY.md) for the threat
model and how to report a vulnerability, and [CHANGELOG.md](CHANGELOG.md) for
what changed.

## License

MIT. Copyright (c) 2025 DeusData for the upstream project and (c) 2026 redacted
Security for this fork's additions; see [LICENSE](LICENSE). Vendored
tree-sitter grammar licenses are listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
