# Extending code-graph

Four things people add most often, each with the file to copy and the test
that has to change. Every recipe ends with `go test ./... -count=1` and
`golangci-lint run`; CI runs both plus the accuracy gates described in
[CONTRIBUTING.md](../CONTRIBUTING.md#measuring-an-accuracy-change).

## Add an MCP tool

Template: `internal/tools/cycles.go` (small, read-only, one handler).

1. Create `internal/tools/<name>.go` with `func (s *Server) register<Name>Tool()`
   that calls `s.addTool(&mcp.Tool{...}, s.handle<Name>)`. Give the tool a
   `Name`, a `Description` written for an agent (when to use it, what it
   returns, what it cannot do), an `InputSchema` as raw JSON, and
   `Annotations` (`ReadOnlyHint`, `DestructiveHint`, `IdempotentHint`).
2. Implement `handle<Name>(ctx, req)`; parse with `parseArgs`, read arguments
   with `getStringArg`/`getIntArg`/`getBoolArg`, resolve the project with
   `s.resolveStore`, and return `jsonResult(map)` or `errResult(msg)`. Never
   return a Go error for user mistakes; those become error results.
   Include `_metadata` via the helpers in `metadata.go` so provenance and
   index identity travel with the answer.
3. Wire it in `internal/tools/register_*.go` (pick the area) so `registerTools`
   calls it.
4. Regenerate the schema snapshot that CI compares against the runtime:
   `python3 bench/research/agent-effectiveness/generate_schemas.py`, then
   run `test_generate_schemas.py` in the same directory.
5. Tests: add the tool to `metadata_coverage_test.go` (every tool must emit
   `_metadata`) and a table row in `handlers_table_test.go` for its happy
   path and its most likely bad request. Update the README tools table.

## Add a language

Template: `internal/lang/nix.go` plus `internal/cbm/grammar_nix.c`.

1. Vendor the tree-sitter grammar under
   `internal/cbm/vendored/grammars/<lang>/` (`parser.c`, `scanner.c` if any,
   `LICENSE`). Record the upstream repo, pinned ref, and license in
   `THIRD_PARTY_NOTICES.md` and `internal/cbm/GRAMMARS.md`.
2. Add `internal/cbm/grammar_<lang>.c` that includes the vendored sources
   (one translation unit per grammar; static symbols collide otherwise).
3. Add `CBM_LANG_<LANG>` to the enum in `internal/cbm/cbm.h`, its
   `tree_sitter_<lang>()` entry in the C dispatch table, and the Go mapping
   in `internal/cbm/cbm.go`.
4. Add `internal/lang/<lang>.go` with a `LanguageSpec` naming the node types
   for functions, classes, calls, imports, and modules, and add the language
   to `AllLanguages()`. Use `go test ./internal/pipeline/ -run TestASTDump -v`
   to see the real node kinds before guessing.
5. Extraction hooks for calls, definitions, and imports live in the C
   extractors (`extract_defs.c`, `extract_calls.c`, `extract_imports.c`),
   gated on the new `CBM_LANG_*` value. Start with definitions and calls.
6. Tests: a parity case in `internal/pipeline/langparity_test.go`, an AST
   structure case in `astdump_test.go`, and a fixture under
   `bench/accuracy/synthetic/` with `ground_truth.json` if the language has
   resolution rules worth gating. Add the language to the README list and
   to the grammar-version baseline used by `index_health`.

Grammars behind the `cbm_all` build tag (see `docs/upstream.md` and the
Makefile) are excluded from default builds; put a new grammar behind the tag
if it is large and niche.

## Add an edge type

Template: `EdgeRunsBinary` in `internal/store/edge_types.go`.

1. Add the constant and an `EdgeTypeInfo` row (family, source and target
   roles, meaning) to `internal/store/edge_types.go`, and the same row to
   `docs/edge-types.md`. `TestEdgeTypeTableIsUniqueAndDocumented` fails
   otherwise.
2. Emit edges with `store.Edge<Name>`; `TestNoUndeclaredEdgeTypeLiterals`
   fails on a `Type: "LITERAL"` anywhere in production code.
3. If tools should traverse it, add it to the family filters in
   `internal/store/edges.go` (callers/callees, impact) and mention it in the
   `get_graph_schema` and `trace_*` tool descriptions.
4. Tests: a pipeline test that indexes a fixture and asserts the edge, and a
   `get_relationship_evidence` case if the edge carries resolver properties.

## Add an embedding provider

Template: `internal/embed/voyage.go`.

1. Implement `embed.Embedder` (`Model`, `EmbedBatch`, `EmbedSingle`) in
   `internal/embed/<provider>.go`. Honour `ctx` cancellation inside retries
   and chunk requests at `embed.BatchSize`.
2. Declare its environment variables in `internal/config` (key, default,
   one-line doc) and document them in CLAUDE.md; `internal/config`'s tests
   fail on undocumented or directly-read keys.
3. Add the selection branch to `embed.Default()`. Keep the rule that no
   configured provider means `embed.Disabled`, never a crash.
4. Tests: an `httptest` server exercising success, 429 retry, and
   cancellation, mirroring `internal/pipeline/pass_embeddings_test.go`.
