# Contributing to code-graph

Fork of [DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp), renamed `code-graph`. Contributions go through PRs to `main`.

## Build from Source

**Prerequisites**: Go 1.26+, a C compiler (gcc or clang - needed for tree-sitter CGO bindings), Git.

```bash
git clone https://github.com/brandyn-s/code-graph.git
cd code-graph
make build   # or: CGO_ENABLED=1 go build -o bin/code-graph ./cmd/code-graph/
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

CI lints only code newer than the reviewed baseline, with golangci-lint 2.10.1
on Go 1.26. To reproduce that exact result locally (a newer Go toolchain can
change typecheck output):

```bash
GOTOOLCHAIN=go1.26.1 golangci-lint run --timeout=10m \
  --new-from-rev=32fc4dd857497addff22115d6858dde2289e8e04
```

Run it from the repository's physical path. golangci-lint's `--new-from-rev`
filter matches issue paths against `git diff` output, and when the working
directory is reached through a symlink (macOS `/tmp` is one) every finding is
silently dropped and the run prints `0 issues` for code CI will reject. Use
`cd "$(pwd -P)"` first; the same applies to a checkout under `/tmp`.

See docs/ci.md for the sanitizer lanes (`make test-asan`, `make test-tsan`,
`make soak`).

## Project Structure

```
cmd/code-graph/       Entry point (MCP server + CLI + install/update)
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

## Extending

[docs/extending.md](docs/extending.md) has step-by-step recipes for the four
common additions: an MCP tool, a language, an edge type, and an embedding
provider. Each names the template file to copy and the test that must change.
[docs/upstream.md](docs/upstream.md) explains what is shared with the
upstream project and how to port a fix from it.
[docs/ci.md](docs/ci.md) lists every workflow, its trigger, and which secrets
it needs; only the `research:` workflows call paid providers.

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

## Measuring an Accuracy Change

Extractor and resolver changes are gated on relationship accuracy, not just
on unit tests. CI runs three checks from `bench/accuracy/`; run the same ones
locally before opening a PR that touches `internal/cbm`, `internal/pipeline`,
or `internal/cypher`:

```bash
make build

# 1. Phantom-edge gate: negative fixtures assert that bare-name suffix matches
#    never produce CALLS edges to internal targets. Fails if any fixture's
#    phantom count exceeds bench/accuracy/negative_baselines.json.
python3 bench/accuracy/check_negative_fixtures.py --regression-gate

# 2. Cypher semantics gate: pins CONTAINS element-of semantics on array
#    properties so a query-executor refactor cannot silently regress it.
python3 bench/accuracy/check_cypher_semantics.py

# 3. Adversarial F1 gate: precision/recall of CALLS edges against a
#    ground-truth oracle on a pinned public fixture. Check out the fixture at
#    the SHA recorded in bench/accuracy/fixtures.json, then:
CODE_GRAPH_FIXTURE_PATH_FLASK_ADVERSARIAL=/path/to/flask \
  python3 bench/accuracy/check_adversarial_f1.py --fixture flask-adversarial --min-f1 0.55
```

For a broader before/after comparison on any fixture listed in
`fixtures.json`, run `python3 bench/accuracy/compare.py <fixture-id>` on
`main` and on your branch and put both F1 numbers in the PR description. The
synthetic fixtures under `bench/accuracy/synthetic/` carry hand-written
`ground_truth.json` files and are the fastest way to pin a regression for a
specific extraction bug: add the smallest source that reproduces it, the
expected edges, and a baseline entry.

## Pull Request Guidelines

- One logical change per PR
- Include tests for new functionality
- Run `gofmt -w .`, `go test ./... -count=1`, and `golangci-lint run` before submitting
- Keep PRs focused - avoid unrelated reformatting or refactoring

## Release Process

Releases are triggered via `workflow_dispatch` on `release.yml` with a
monotonically increasing `version` input (for example,
`v0.9.1`). The workflow accepts only the exact default-branch HEAD,
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
