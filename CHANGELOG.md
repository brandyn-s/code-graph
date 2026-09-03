# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed
- **Renamed the binary and module to `code-graph`.** The command directory is
  `cmd/code-graph`, the Go module path is `github.com/brandyn-s/code-graph`,
  release archives are `code-graph-<os>-<arch>.tar.gz` / `.zip`, and the MCP
  server registers as `code-graph`. `code-graph install` removes the old
  `codebase-memory-mcp` registration from client configs, and an existing
  `~/.cache/codebase-memory-mcp` index directory keeps being used until a
  `~/.cache/code-graph` directory exists.
- **Plain semantic-version tags.** Releases are now `vMAJOR.MINOR.PATCH`. The
  release validator, self-updater, and installers compare the legacy internal
  `vX.Y.Z-redacted.N` tags as older than any plain release of the same base.
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
  public release. Historical `v0.8.0-redacted.N` releases were internal builds
  and are not available from this repository.

## Earlier history

Releases `v0.7.0-redacted.1` through `v0.8.0-redacted.11` were internal
pre-public builds of this fork. Upstream history before the fork is recorded
in [DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp).
