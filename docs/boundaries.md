# Boundaries and tradeoffs

- The normal graph is heuristic. Compiler accuracy is established only for
  declared measured cells and SCIP-covered non-drifted documents.
- `trace_data_flow` is graph connectivity, not variable-level taint, path
  feasibility, sanitizer modeling, or vulnerability proof.
- Graph-only conceptual localization is materially weaker than semantic
  search. Use `code-search` for "where is the code that does X?".
- The SQLite-per-project architecture works on a very large repository but is
  not a distributed organization indexing fleet.
- Cross-project discovery and comparison have no organization ACL layer or
  globally calibrated score space.
- Sourcegraph has a broader search/history/ACL/operations surface. Language
  servers provide editor-native, compiler-aware navigation. CodeQL is stronger
  for vulnerability-grade data flow. code-graph complements them by giving an
  agent one persistent, evidence-carrying structural view.
- Runtime trace ingestion confirms observed HTTP relationships; lack of an
  observation does not prove a relationship cannot occur.
- Generated reports and visualizations are writes, but by default they land
  under `<cache>/reports/<project>/`, never in the checkout. Only an explicit
  `report_path`/`output_path` inside the repository, or `manage_adr` (which
  exists to write ADRs into the repository), modifies the checkout.
