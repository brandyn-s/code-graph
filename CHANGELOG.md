# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added (0.9.1, ported from upstream codebase-memory-mcp)
- Seven small grammars vendored from upstream's pinned manifest and included
  in the default build: Lua, Vue, Svelte, GraphQL, go.mod, Erlang, Clojure
  (`scripts/vendor-grammar-from-manifest.sh` reproduces the vendoring).
- `CALL_REFERENCE` edge type, aligned with upstream: a callable referenced at
  a value site that resolves to exactly one Function or Method (import,
  same-module, or unique-name rule, single candidate). `USAGE` is now the
  unproven counterpart (non-callable targets, ambiguous or fuzzy resolutions).
  Both carry `resolver_rule`, `confidence`, `confidence_band`, and
  `candidate_count` so `get_relationship_evidence` shows provenance for them.
- Extraction supervisor: tree-sitter extraction runs in supervised worker
  processes (`code-graph cbm-extract-worker`, spawned from the running binary).
  A file that crashes or hangs the native extractor is skipped and reported
  instead of killing the indexer or the MCP server (upstream 9b9638e1,
  fb334f78, e242ce1e). `CODE_GRAPH_EXTRACT_ISOLATION` (`auto`|`on`|`off`) and
  `CODE_GRAPH_EXTRACT_FILE_TIMEOUT_S` (default 30) control it; skips are
  written to a `<project>.skips.json` sidecar and surfaced by `index_health`
  (`skipped_files`, `skipped_count`) and `code-graph doctor`.

### Fixed (0.9.1, ported from upstream codebase-memory-mcp)
- HTTP route linking takes the handler from the LAST call argument for
  Express-style and gin/chi routes, splitting the argument list by balanced
  parentheses so middleware calls (`authRequired()`) and inline arrow-function
  middlewares no longer hide the handler (upstream 592894a4). Spring
  `@RequestMapping`/`@GetMapping` paths are read from a `path =` / `value =`
  attribute anywhere in the annotation, not only as the first argument
  (upstream c36b4fbc). Upstream fd73c347 (Blazor `@page` routes) does not
  apply: this fork has no C#/Razor route extractor.
- Go struct fields are extracted as definitions (they live under
  `struct_type -> field_declaration_list`; upstream 47116b8e) and the blank
  identifier `_` is skipped (upstream cb7cb444).
- Python bare calls whose callee is a parameter of an enclosing function or
  lambda are flagged `callee_is_locally_bound` and never resolved to a
  same-named module-level symbol (upstream 95689b5c, 0b0d143c, 97517a46).
  New negative fixture `bench/accuracy/synthetic/python-param-shadow-negative`.
  Upstream 572725e6 (trailing blanks at EOF in parse coverage) does not apply:
  this fork computes no parse-coverage metric.
- Extractor memory safety: `walk_defs` is iterative with a growable heap frame
  stack (upstream 174e56b4, #668), every other recursive AST walker and the Go
  LSP resolver are bounded by `CBM_MAX_WALK_DEPTH` (default 512) and flag the
  result as depth-capped instead of overflowing the C stack (upstream
  40f2722d/4d844069), the vendored tree-sitter runtime caps GLR stack-merge
  recursion at 512 (upstream da046da5), and `cbm_arena_alloc` tolerates a NULL
  arena (upstream 9e2bb928). The pipeline logs `cbm.extract.depth_capped` for
  partially indexed files. Regression inputs: 5000 top-level definitions,
  20000-deep nested calls, 6000-deep arrays, 5000-deep Go blocks.

### Changed
- Removed all references to the originating organization from the tree;
  copyright line now names the project contributors.

### Added (embedding providers)
- OpenAI-compatible embedding provider: point `CODE_GRAPH_EMBED_BASE_URL` and
  `CODE_GRAPH_EMBED_MODEL` at OpenAI, Azure OpenAI, Gemini's OpenAI surface,
  Ollama, vLLM, LM Studio, OpenRouter, or any gateway serving
  `POST {base}/embeddings`. Credential via `CODE_GRAPH_EMBED_API_KEY` (falls
  back to `OPENAI_API_KEY`; optional for self-hosted endpoints), header style
  via `CODE_GRAPH_EMBED_AUTH_HEADER` (`bearer` or Azure's `api-key`), optional
  width check via `CODE_GRAPH_EMBED_DIMENSION`. `CODE_GRAPH_EMBED_PROVIDER`
  (`auto` default) selects between `voyage`, `openai`, and `off`; auto prefers
  Voyage when its key is set, then an OpenAI-compatible base URL.
- The startup line, `code-graph doctor` (new `provider`, `model`, `endpoint`,
  `reachability` fields; `voyage_reachability` retained), and every
  "embeddings unavailable" message now name the resolved provider or how to
  configure one. Stored embedding rows record the provider's model id.
- `docs/embeddings.md` documents each vendor's settings and what text is sent.

### Added (pre-public hardening)
- `SECURITY.md`: private vulnerability reporting, 90-day disclosure target,
  and a threat model (what is read, where indexes live, what leaves the
  machine, stdio-only transport).
- Research workflows (`agent-effectiveness`, `locbench-*`, `matched-depth`)
  are `workflow_dispatch`-only, prefixed `research:`, fail fast without their
  secrets, and no longer post PR comments. `docs/ci.md` lists every workflow.
- End-to-end MCP client test (`internal/tools/mcp_e2e_test.go`): initialize,
  tools/list against the registry, indexing round trip, evidence query, and
  the stale-source refusal through the go-sdk client.
- Core CI runs the unit suite on macOS and Windows; both lanes are required.
  until green) in addition to Linux.
- `FuzzExtractFile` seeds every compiled grammar from the synthetic fixtures;
  `fuzz.yml` runs it and the Cypher fuzzers nightly under AddressSanitizer.
- Randomized incremental-vs-clean equivalence property test over the
  go-minimal fixture (ten seeds, twelve edits each).
- Index format versioning: databases carry `user_version`; newer formats are
  refused with an upgrade hint, unsupported older formats with a rebuild hint,
  pre-versioning databases are adopted as format 1. Fixture under
  `internal/store/testdata/format-v1/`; policy in `docs/index-format.md`.

- `CODE_GRAPH_TOOLSET` (default `core`) advertises 26 tools over MCP instead
  of 40; `full` restores the whole surface. The CLI and schema snapshot keep
  every tool; the snapshot now also records `CORE_TOOLS`.

- `code-graph doctor [--json]`: resolved config (secrets redacted), cache
  directory and per-project database sizes and format versions, embeddings
  mode and Voyage reachability, compiled and excluded grammars, toolset.

- Distribution: `flake.nix`, a Homebrew formula template with
  `scripts/update-homebrew-formula.sh`, an MCP registry `server.json`
  template with `scripts/update-server-json.sh`, and `docs/install.md` /
  `docs/registry.md`.

- `bench/public/locbench/`: reproducible n=200 Loc-Bench agent run (pinned
  instances, provenance record, budget cap) and an explicit statement that the
  n=80 graph-only replay's inputs are not in the repository.

- Release candidates: `vX.Y.Z-rc.N` tags are accepted by the release
  workflow and published as GitHub prereleases; `code-graph update` ignores
  them unless `CODE_GRAPH_UPDATE_CHANNEL=rc`. `docs/RELEASE_REHEARSAL.md`
  walks through a throwaway `v0.9.0-rc.1`.

### Changed (maintainability pass)
- README opens with a real `get_relationship_evidence` result and the
  stale-index refusal, plus a comparison against grep and the upstream.
- `internal/config` is the single inventory of the 41 environment variables;
  tests fail on undocumented keys or direct `os.Getenv` reads of product
  keys. Eleven previously undocumented variables are now in CLAUDE.md.
- `pipeline.go` and `tools.go` split by pass and by area (pure moves);
  `internal/tools` coverage 49% -> 65% via table-driven handler tests.
- `internal/embed` defines the `Embedder` interface with a `Disabled`
  provider; `internal/store/edge_types.go` declares every edge kind, and
  `docs/edge-types.md` is generated from it.
- `docs/extending.md` (tool, language, edge type, provider recipes) and
  `docs/upstream.md` (hard-fork stance, what to diff against upstream).
- The CUDA grammar moved behind the `cbm_all` build tag; `make build-all`
  includes it. Release archives shrink accordingly and `.cu` files are
  reported as unsupported in default builds.

### Changed (review pass)
- Embedded Claude Code skills renamed from `codebase-memory-*` to
  `code-graph-*`; `install` removes the old skill directories and
  `uninstall` cleans both names.
- Test fixtures and comments no longer reference internal repositories or
  addresses; private planning links removed from workflows and design notes.
- `docs/ARCHITECTURE.md` state of record updated for the public primary;
  setup scripts no longer describe the project as private.
- Runtime tuning environment variables documented in `CLAUDE.md`.
- Dependabot now covers Go modules.

### Changed
- **Renamed the binary and module to `code-graph`.** The command directory is
  `cmd/code-graph`, the Go module path is `github.com/brandyn-s/code-graph`,
  release archives are `code-graph-<os>-<arch>.tar.gz` / `.zip`, and the MCP
  server registers as `code-graph`. `code-graph install` removes the old
  `codebase-memory-mcp` registration from client configs, and an existing
  `~/.cache/codebase-memory-mcp` index directory keeps being used until a
  `~/.cache/code-graph` directory exists.
- **Plain semantic-version tags.** Releases are now `vMAJOR.MINOR.PATCH`. The
  release validator, self-updater, and installers compare the legacy
  pre-public `vX.Y.Z-<word>.N` tags as older than any plain release of the
  same base.
- **Offline by default.** When `VOYAGE_API_KEY` is unset, embedding passes are
  skipped and one `embeddings disabled` line is logged at startup instead of
  per-pass warnings. `CODE_GRAPH_SKIP_EMBEDDINGS=0` forces them on.
- **Service domain classification is configurable.** `service_map` and
  `diff_services` read `{"domain": ["pattern", ...]}` from
  `CODE_GRAPH_SERVICE_MAP` or `<config dir>/code-graph/service_map.json`, with
  a small naming-convention default replacing the previous hardcoded table.
- **Nix service extraction prefixes are configurable.** Defaults are the
  standard `options.services.<name>` and `${pkgs.<pkg>}`; set
  `CODE_GRAPH_NIX_SERVICE_OPTION_PREFIX` and `CODE_GRAPH_NIX_PKGS_PREFIX` for
  namespaced module conventions.
- README rewritten around a one-command install; deep material moved to
  `docs/` (precision tiers, evidence, measured evidence, boundaries, clients,
  service map).

### Added
- `install.sh` and `install.ps1`: dependency-free installers that download the
  release archive, verify its SHA-256 against `checksums.txt`, verify build
  provenance when the GitHub CLI is available, and print the MCP registration
  line. `go install github.com/brandyn-s/code-graph/cmd/code-graph@latest`
  is documented as the alternative.
- `CODE_GRAPH_CACHE_DIR` to relocate the per-project databases.
- `THIRD_PARTY_NOTICES.md` and per-grammar `LICENSE` files for the vendored
  tree-sitter grammars.
- `CHANGELOG.md`, issue and pull request templates.

### Removed
- Internal planning documents, benchmark reports, and fixtures that referenced
  private repositories were removed from the public history before the first
  public release. Pre-public internal builds are not available from this
  repository.

## Earlier history

Releases before `v0.9.0` were pre-public internal builds of this fork. Upstream history before the fork is recorded
in [DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp).
