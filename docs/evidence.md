# Verifiable evidence

code-graph shares a canonical reference schema with `code-search`:

| Reference | Binds |
|---|---|
| `symbol_ref` | Repository, revision, path, kind, qualified name, source range |
| `evidence_ref` | Symbol/source/relationship/analysis evidence in one index generation |
| `observation_ref` | Engine, derivation, stance, and confidence over evidence |
| `relationship_ref` | Edge type, both symbols, resolver, confidence, runtime observations, optional compiler artifact |
| `analysis_ref` | Attested CodeQL database/query/SARIF identities and exact path steps |
| `claim_ref` | Stable repository-scoped claim identity |

## Where evidence surfaces

- `search_graph` emits `symbol_ref`, `evidence_ref`, and `observation_ref`
  when its live checkout and persisted index identity agree.
- `get_relationship_evidence` emits source/target identities, resolver
  provenance, confidence band, runtime observation count, and immutable
  relationship/evidence/observation references.
- `trace_data_flow(required_assurance="variable_level_taint")` does not return
  a graph path under a taint label. It returns a structured CodeQL handoff.
- `code-graph import-codeql` converts one already-produced, attested SARIF
  2.1.0 path run into immutable `analysis_ref` evidence. It launches no
  analyzer and writes no graph state. See
  [codeql-evidence-import.md](codeql-evidence-import.md).

## Index identity

`index_repository` captures repository, checkout, source, and index identity
at the start of a run and rechecks it at the end. If the working tree changed
underneath the indexer, the index is marked incoherent and evidence-capable
reads fail closed until the next successful run. Clients should require a
captured, live-matching identity from `index_status` before treating emitted
references as evidence.

## Assurance lattice

`internal/evidence/proof.go` contains a deterministic evaluator for evidence
bundles. It can require capabilities such as source coordinates, semantic or
lexical retrieval, structural relationships, compiler resolution, runtime
observation, or variable-level taint; it also represents contradiction search,
coverage, invariants, blockers, and caveats.

That evaluator is an internal Go contract and test surface, not a registered
MCP tool. The user-facing product surfaces are the evidence-producing tools
and the offline importer above. A client or host may assemble and evaluate
proof bundles without changing the stable IDs.
