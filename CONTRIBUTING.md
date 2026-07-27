# Contributing to code-graph

redacted fork of [DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp). Contributions go through PRs to `main`.

## Build from Source

**Prerequisites**: Go 1.26+, a C compiler (gcc or clang - needed for tree-sitter CGO bindings), Git.

```bash
git clone https://github.com/redacted-org/code-graph.git
cd code-graph
CGO_ENABLED=1 go build -o bin/codebase-memory-mcp.exe ./cmd/codebase-memory-mcp/
```

macOS: `xcode-select --install` provides clang.
Linux: `sudo apt install build-essential` (Debian/Ubuntu) or `sudo dnf install gcc` (Fedora).
Windows: Install [MSYS2](https://www.msys2.org/), then `pacman -S mingw-w64-ucrt-x86_64-gcc`. Build from UCRT64 shell.

## Run Tests

```bash
go test ./... -count=1
```

Key test files:
- `internal/pipeline/langparity_test.go` - 125+ language parity cases
- `internal/pipeline/astdump_test.go` - 90+ AST structure cases
- `internal/pipeline/pipeline_test.go` - integration tests

## Run Linter

```bash
# golangci-lint v2.10 required
golangci-lint run ./...

# Format first
gofmt -w .
```

## Project Structure

```
cmd/codebase-memory-mcp/       Entry point (MCP server + CLI + install/update)
  assets/skills/               4 task-specific skills
internal/
  store/                       SQLite graph storage (WAL mode, Louvain clustering)
  lang/                        Language specs (27 languages, tree-sitter node types)
  cbm/                         Vendored tree-sitter C grammars, AST extraction
  pipeline/                    Multi-pass indexing pipeline
  httplink/                    Cross-service HTTP route matching
  cypher/                      Cypher query engine
  tools/                       MCP tool handlers (18 tools) + CLI dispatch
  watcher/                     Background auto-sync
  discover/                    File discovery with .gitignore/.cbmignore
  fqn/                         Qualified name computation
  traces/                      OpenTelemetry trace ingestion
  selfupdate/                  GitHub release checking
```

## Adding or Fixing Language Support

Most language issues are in `internal/lang/<name>.go` (node type configuration) or `internal/pipeline/` (extraction logic).

1. Find the relevant language spec in `internal/lang/`
2. Use AST dump tests to see actual tree-sitter node types:
   ```bash
   go test ./internal/pipeline/ -run TestASTDump -v
   ```
3. Compare configured node types vs actual AST output
4. Update the language spec and add/fix parity test cases
5. Verify with a real open-source repo

## Pull Request Guidelines

- One logical change per PR
- Include tests for new functionality
- Run `gofmt -w .`, `go test ./... -count=1`, and `golangci-lint run` before submitting
- Keep PRs focused - avoid unrelated reformatting or refactoring

## Release Process

Releases are triggered via `workflow_dispatch` on `release.yml` with a
monotonically increasing `version` input (for example,
`v0.7.0-redacted.3`). The workflow accepts only the exact default-branch HEAD,
tests and packages all supported platforms, generates build-provenance
attestations, creates an immutable tag, uploads every asset to a draft, and
publishes only after those gates pass.

Repository-level release immutability must remain enabled. It applies only to
future releases by default. An existing release may be republished in place
only when its original tag and every asset digest have been independently
verified; the resulting release attestation still must not be described as
retroactive build provenance. Never reuse a version or move a release tag; the
workflow rejects both.

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
