# R1-R15 Research Query — Code-Graph Accuracy Frontier

This file is the persisted input for `/gather-research`. Invoke as:

```
/gather-research <paste this file's content>
```

Or equivalently, the skill can read this file directly as its focus-area argument.

---

Focus area: code-graph accuracy and effectiveness improvements

Target: a Go-based code knowledge graph (tree-sitter extraction + SQLite WAL +
Cypher subset + LSP augmentation for Go) used to feed an Opus-coordinated
documentation pipeline over a Rust/TypeScript/Python/NixOS stack. Production
deployment at redacted; ~80K nodes / 212K edges per indexed repo.

Current measured accuracy (with oracle-class uncertainty band ~35-40% Jaccard):
- Python CALLS: F1 0.54-0.99 (fixture-dependent)
- Rust CALLS:   F1 0.74-1.00 (trait-form vs impl-form metric drift on one fixture)
- Go CALLS:     F1 0.45-0.99 (oracle-coverage artifacts on stdlib edges)
- Nix pub/sub:  F1 1.00 against hand-verified ground truth (n=20)

Research questions to decompose:

## Fundamentals (validate or refine our current approach)

R1. Static call-graph accuracy techniques: how do current SOTA tools
    (Joern, Glean, CodeQL, Stack Graphs, Sourcegraph SCIP, ast-grep,
    Sea-of-Graphs, GraphRAG) handle dynamic dispatch, virtual methods,
    decorator-based registration, framework dispatch (Flask, FastAPI,
    Django, Express)? What's the published accuracy ceiling for static
    analysis on framework-heavy Python code?

R2. Code property graph (CPG) literature beyond Joern: 2024-2026 papers
    on CPG construction, CPG-augmented LLMs, multi-language CPG
    unification, AST + dataflow + control-flow merge strategies. What's
    the academic consensus on CPG vs separate-edges-per-relation?

R3. Hand-oracle / ground-truth methodology in code-graph evaluation:
    published guidance on minimum hand-verified sample size for
    catching systemic bugs in graph extractors. Is n=20 enough? What
    do other projects use?

R4. Cross-language edge resolution: how do production graph tools
    handle FFI boundaries (PyO3, napi-rs, wasm-bindgen, JNI, gRPC,
    Thrift)? Are there published techniques for resolving Rust-to-
    Python or TypeScript-to-Rust call graphs?

R5. Confidence/uncertainty modeling in code graphs: who else uses
    EXTRACTED/INFERRED/AMBIGUOUS confidence tiers per edge? What's
    the alternative literature (probabilistic graph models,
    Bayesian inference over code edges, etc.)?

## Frontier (novel approaches to consider adopting)

R6. LLM-augmented graph extraction: 2024-2026 papers on LLMs validating,
    enriching, or generating code graph edges. Specifically: LLM
    verification of static-analyzer output, LLM resolution of dynamic
    dispatch targets, LLM extraction of intent-edges (security
    sensitivity, data-flow purpose) that pure static analysis can't
    reach.

R7. Incremental/streaming graph indexing: how do Glean (Meta), Kythe
    (Google), Sourcegraph maintain graph freshness on monorepos that
    change continuously? Specific techniques for change-coupling-aware
    partial reindex without full re-walk.

R8. Tree-sitter alternatives for high-accuracy extraction: is
    tree-sitter the right choice for production code graphs in 2026,
    or have ANTLR4/parsy/lezer/SitterError-based approaches converged
    on better accuracy? What about full-fidelity parsers like
    rust-analyzer, gopls, swc, ts-morph?

R9. Embedding-augmented graph queries: Claude Context, GraphRAG, code
    RAG papers — what's the SOTA hybrid of structural graph + vector
    embedding for code understanding? Specifically retrieval
    composition (graph-then-embed vs embed-then-graph), late-fusion
    ranking, and effectiveness measurements.

R10. Multi-model evaluation harnesses for graph extractors: published
     methodologies for comparing competing graph engines on
     reproducible code corpora. JetBrains Datalore, Sourcegraph
     Cody benchmarks, anyone else publishing benchmark suites with
     ground truth?

R11. Documentation generation from code graphs: papers and production
     systems that consume code graphs to generate human-facing docs.
     What graph features (edge types, node properties, traversal
     patterns) consistently drive documentation quality? Specific
     interest: how does the Lobe/Codetracer/Pieces graph-to-docs
     work consume graph state?

R12. Probabilistic / fuzzy edges with confidence propagation: Datalog,
     Soufflé, ProbLog, recent declarative analysis frameworks that
     support uncertainty at the edge level. Could replace or augment
     our regex-based confidence tiering.

R13. Schema-less graph storage at scale: SQLite WAL works for our
     ~500K LOC ceiling. What happens at 5M / 50M LOC? Specifically:
     how do TerminusDB, EdgeDB, KuzuDB compare to RocksDB for
     schema-flexible graph storage in 2026?

R14. Cross-repo / federated code graphs: papers on querying graphs
     that span multiple repositories without merging into one DB.
     Particularly: linking npm packages used in a TypeScript repo to
     their source GitHub graphs, or linking Rust crates to crates.io
     registry graphs.

R15. Adversarial fixture selection for code-graph evaluation: how do
     researchers pick adversarial test corpora that surface F1 drops?
     Is there a published taxonomy of "code graph stress tests"
     (decorator-heavy code, macro-heavy code, dynamic dispatch
     extreme, generic-rich code)?

Output: ranked recommendations on which fundamentals our current approach
gets right (validation) and which frontier techniques would meaningfully
improve accuracy or effectiveness for the docs-pipeline use case. Each
recommendation should include: source paper or production system,
estimated implementation cost, expected F1 or quality lift, and how it
interacts with our existing architecture.

Skip recommendations that:
- Apply only to language-specific edge cases not in our stack
- Require a different storage backend (we're committed to SQLite WAL
  through ~500K LOC; only Postgres/RocksDB switches gate-able by
  evidence of scale issues)
- Replace tree-sitter (we're committed to it; only augmentation suggestions
  welcome)
- Are pure benchmarking with no implementation guidance
