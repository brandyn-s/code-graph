# Security

## Reporting a vulnerability

Report vulnerabilities privately through GitHub's private vulnerability
reporting: <https://github.com/brandyn-s/code-graph/security/advisories/new>.
Do not open a public issue for anything that could be exploited before a fix
ships.

You will get an acknowledgement within 7 days. We aim to publish a fix and an
advisory within 90 days of the report, sooner for anything that exposes source
code or credentials. Credit is given in the advisory unless you ask otherwise.

## Threat model

code-graph reads the source trees you point it at, including git metadata
(commit, dirty state, tracked file list) used to bind an index to an exact
checkout. Indexing is a local operation: it parses files with vendored
tree-sitter grammars and hand-written C extractors, optionally runs a compiler
indexer (SCIP) you already have installed, and writes one SQLite database per
project under `~/.cache/code-graph` (or `CODE_GRAPH_CACHE_DIR`). Nothing leaves
the machine by default. If you configure an embedding provider (`VOYAGE_API_KEY`
for Voyage AI, or `CODE_GRAPH_EMBED_BASE_URL` for an OpenAI-compatible endpoint
you choose, including self-hosted ones), node names, signatures, and query
text are sent to that endpoint for embeddings; the optional research
batteries under `bench/` call Anthropic only when you run them with
`ANTHROPIC_API_KEY`. Query results include file paths and source line excerpts
from the indexed tree, so treat the MCP client that receives them as trusted
with that code.

The supported transport is MCP over stdio: the client spawns `code-graph` and
talks to it on its own pipes, so there is no network listener and no
authentication surface. code-graph does not ship an HTTP or SSE listener. The
parsers run on untrusted input, so the C extractors are covered by native Go
fuzz targets in `internal/cbm` (run nightly in `.github/workflows/fuzz.yml`)
and an AddressSanitizer lane (`.github/workflows/asan.yml`). Release binaries
carry GitHub build provenance; verify them with `gh attestation verify` as
described in the README before installing.
