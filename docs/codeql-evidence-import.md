# Offline CodeQL evidence import

`import-codeql` converts an already-produced CodeQL SARIF 2.1.0 path result
into immutable analysis evidence. It is an operator-only CLI boundary, not an
MCP tool: it does not launch CodeQL, build a database, mutate a graph index, or
persist its JSON output.

Use it only after `trace_data_flow` has returned a CodeQL handoff for
`required_assurance="variable_level_taint"`.

## Trust boundary

The importer independently captures the clean Git checkout identity, hashes
the SARIF and receipt bytes, validates every selected source coordinate against
the live checkout, and derives stable evidence IDs. The operator-owned receipt
attests the CodeQL database, extractor, query pack, query classification, and
database-quality measurements.

The importer validates the receipt's shape and bindings, but it does not
recompute database or query-pack digests from absent external artifacts. The
result therefore proves linkage to a specific operator attestation; it does not
replace the procedure or authority that produced that attestation.

Unattested legacy CodeQL paths remain usable as ordinary source evidence, but
they do not satisfy the `variable_level_taint` capability gate.

## Preconditions

- The repository is a Git worktree with no tracked or untracked changes.
- The receipt's `repository_id`, `source_revision`, and `index_generation`
  exactly match the live checkout. A coherent `index_status` response exposes
  these values under `index_identity`.
- SARIF contains exactly one CodeQL 2.1.0 run. Its driver version exactly
  matches `analyzer_version` in the receipt.
- Every imported query is attested as `variable_level_taint` and its SARIF rule
  declares `properties.kind` as `path-problem`.
- Each path has at least two locations. Locations resolve through an inline URI
  or the run artifact table to a repository-relative `%SRCROOT%` URI. For
  line/column regions, `startLine` and `endColumn` are explicit and positive;
  standard SARIF defaults are materialized for omitted `startColumn` and
  `endLine`. Character-offset regions are not accepted by this importer.
- Referenced files are regular UTF-8 files inside the repository. Absolute
  paths, non-`%SRCROOT%` URI bases, missing/mismatched artifact indexes,
  traversal, symlinks, and out-of-range coordinates fail closed.

CodeQL emits path explanations in SARIF `codeFlows` for path-problem queries;
the format and coordinate rules come from the
[CodeQL SARIF output reference](https://docs.github.com/en/code-security/reference/code-scanning/codeql/codeql-cli/sarif-output)
and the [OASIS SARIF 2.1.0 specification](https://docs.oasis-open.org/sarif/sarif/v2.1.0/os/sarif-v2.1.0-os.html).

## Receipt schema

Create a strict JSON document with no unknown or trailing values:

```json
{
  "schema_version": 1,
  "repository_id": "<live index_identity.repository_id>",
  "source_revision": "<live index_identity.source_revision>",
  "index_generation": "<live index_identity.index_generation>",
  "analyzer_version": "<exact SARIF tool.driver.version>",
  "extractor_version": "<operator-recorded extractor version>",
  "language": "<database language>",
  "database_manifest_sha256": "<64 lowercase hex characters>",
  "database_content_sha256": "<64 lowercase hex characters>",
  "database_quality": {
    "status": "pass",
    "source_files": 1,
    "baseline_lines": 1,
    "extractor_errors": 0
  },
  "query_pack_manifest_sha256": "<64 lowercase hex characters>",
  "queries": [
    {
      "query_id": "<SARIF ruleId>",
      "analysis_kind": "variable_level_taint"
    }
  ]
}
```

`source_files` and `baseline_lines` must be positive; `extractor_errors` must
be non-negative. Query IDs must be non-empty and unique.

## Run

```bash
code-graph import-codeql \
  --repository /absolute/path/to/clean/repository \
  --sarif /absolute/path/to/codeql.sarif \
  --receipt /absolute/path/to/query-attestation.json \
  > codeql-evidence.json
```

The output contains the checkout identity, computed SARIF and receipt SHA-256
digests, and one `analysis_ref` evidence item per imported SARIF thread flow.
Each path step has a stable position, `source` / `intermediate` / `sink` role,
repository-relative path, and exact coordinates.

Treat stdout redirection as an operator decision. The importer itself creates
no output file, consumption marker, graph edge, or index state.
