# Precision tiers

## Heuristic (default)

The default graph combines tree-sitter extraction with import-aware and
type-informed static resolution. It is broad, fast, and useful, but it is not
compiler-grade. Reflection, dependency injection, virtual dispatch, external
code, generated code, and higher-order calls can be missing or ambiguous.

Every consequential result should be treated as evidence to verify, not as a
documentation claim merely because an edge exists. Prefer high-confidence
edges, inspect the exact source, and search for counterexamples.

## SCIP (optional, per project)

```json
{
  "repo_path": "/absolute/repository",
  "precision_tier": "scip",
  "scip_index_path": "index.scip",
  "skip_report": true
}
```

The preference persists. `index_repository` and `index_status` report the
requested and effective tier, SCIP digest, covered documents/functions,
drifted documents, replaced heuristic edges, and inserted compiler-resolved
calls.

SCIP is a partial precision layer, not a project-wide certification. Only
covered, non-drifted documents receive SCIP-derived call replacement.
Uncovered files remain heuristic. If the requested artifact is missing or
invalid, the effective tier stays heuristic and status is visibly degraded.

`get_relationship_evidence` binds the exact SCIP SHA-256 into each derived
`relationship_ref`. A downstream evaluator may count that individual edge as
compiler-resolved only when its `resolution_source` includes `scip-ingest` and
the artifact digest is present.

## Choosing an operation

| Need | Use |
|---|---|
| Find a concept when names are unknown | `code-search` first |
| Find a known symbol or graph node | `search_graph` |
| Exact text, error string, or TODO | `search_code` or `rg` |
| Callers/callees | `trace_call_path` |
| One attributable edge with provenance | `get_relationship_evidence` |
| Arbitrary graph pattern | `query_graph` |
| Change blast radius | `detect_changes` or `get_review_context` |
| Dead code or high-fan-in nodes | `degree_filter` |
| Broad issue localization | `code-search`; graph localization is secondary |
| CALLS/READS/WRITES/USAGE connectivity | `trace_data_flow` |
| Variable-level source-to-sink taint | CodeQL, then the offline `import-codeql` boundary |
| Discovery across local indexes | `localize_across_projects`, then project-bound evidence |

## Recommended workflow

1. Use `code-search` to discover the smallest candidate set for a conceptual
   question.
2. Read the candidate's backend-issued atomic source evidence.
3. Use `search_graph` to resolve the canonical symbol.
4. Run exactly the relationship query the claim needs; avoid broad graph
   wandering.
5. For a consequential edge, select `get_relationship_evidence` output and
   report the effective precision tier.
6. Search for a contradiction or missing expected edge. An empty result is not
   proof of absence without defined coverage.
7. Escalate to source reading, compiler evidence, runtime traces, or CodeQL when
   the requested assurance exceeds the graph's capability.

Composition is client-owned. `get_relevant_context` uses graph state only; it
does not call `code-search` behind the scenes.
