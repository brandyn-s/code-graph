# Team-shared graph artifacts

Indexing a large repository takes minutes and needs the native extractors.
`export-artifact` packages an indexed project's graph database into one
compressed file that teammates (or CI) can `import-artifact` instead of
indexing again. The artifact carries the index identity so an import can
tell whether it matches the local checkout.

```bash
# On the machine that indexed the repository
code-graph export-artifact --repo ~/src/service --out service.cgraph.zst

# On a teammate's machine, same repository cloned somewhere else
code-graph import-artifact service.cgraph.zst --repo ~/work/service
```

## Format

`<name>.cgraph.zst` is: a 16-byte magic, a 4-byte header length, a JSON
header, then one zstd frame holding a `VACUUM INTO` image of the project's
SQLite database. The header records:

| Field | Meaning |
|---|---|
| `format`, `schema_version`, `code_graph_version` | Container version, identity schema version, and the binary that produced the artifact. Imports refuse a different `format` or `schema_version`. |
| `project`, `root_path`, `indexed_at` | The exporting machine's project name and checkout path. |
| `identity` | The index identity captured at indexing time: `repository_id` (from the origin remote), `checkout_id` (from the path), `source_revision`, `dirty_fingerprint`, `index_generation`. `identity_status`/`identity_reason` explain a missing envelope. |
| `node_count`, `edge_count`, `file_count` | Sanity numbers shown on import. |
| `payload_sha256`, `payload_bytes` | Verified after decompression; a mismatch aborts the import. |

`code-graph export-artifact --json` prints the header; the file is readable
without decompressing it.

## Identity check on import

Project names embed the absolute checkout path, so an import renames the
project to the local path's name: the projects row, every table's `project`
column, the project prefix of qualified names, and qualified names embedded
in node/edge properties are rewritten. The local checkout's identity is then
captured and compared with the header:

- Different `repository_id` (another origin): the import is refused.
- Same revision and both trees clean: imported as a coherent index; the
  identity keeps the artifact's revision and takes the local `checkout_id`.
- Different revision, or either tree dirty, or an artifact without identity:
  refused with the reason, unless `--allow-stale` is given. A stale import is
  installed with `identity_status = stale_source`, which `index_health`,
  `list_projects` and evidence tools already surface, and `index_repository`
  replaces it with a fresh index (incremental mode reuses the file hashes).
- An existing local index for the project is kept unless `--force`.

Note on the identity of exported indexes: a default `index_repository` run
does not write anything into the checkout (reports go to
`<cache>/reports/<project>/`), so its identity is coherent and the artifact
imports without `--allow-stale`. If you ask for a report inside the checkout
(`write_report: true` with a `report_path` under the repository), it is
written after the identity capture; the checkout then differs from the indexed
state until you commit or ignore that file, and an artifact exported in that
window imports only with `--allow-stale`.

The artifact contains everything in the project's database, including
`.env`-derived environment variable names and code snippets in docstrings.
Share it through the same channels you would use for the repository itself.
