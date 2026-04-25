# Community Intelligence Query — Code-Graph Production Wisdom

This file is the persisted input for `/gather-intel`. Invoke as:

```
/gather-intel <paste this file's content>
```

Or equivalently, the skill can read this file directly as its focus-area argument.

---

Focus area: code knowledge graph engines, accuracy improvements, and
documentation pipelines (community/practitioner perspective)

Target audience: r/Compilers, r/programming, r/rust, r/golang,
HackerNews, Lobsters, dev blogs, conference talks (FOSDEM, GopherCon,
StrangeLoop, Compiler Construction conferences). Specifically interested
in production-system experience reports, not academic-only theoretical
papers.

Topics to scout:

## Fundamentals (production wisdom)

- What are practitioners' experience reports on Joern, Glean,
  Stack Graphs, Sourcegraph SCIP/Cody, CodeQL accuracy and dev-loop
  pain in production?
- Tree-sitter accuracy in production: which languages have notably
  poor grammars, what workarounds do teams use, when do people give
  up and write a custom parser?
- LSP-as-extractor patterns: who's using gopls / rust-analyzer /
  pyright as a graph oracle vs as an editor? Pitfalls?
- Accuracy harnesses for code analysis tools: what testing patterns
  do production tools (Semgrep, GitHub Advanced Security, Snyk
  Code, Veracode, Fortify) use to validate extraction correctness?
- Hand-curated ground truth vs synthetic vs real-repo benchmarks:
  practitioner consensus on the right mix?

## Frontier (newly emerging patterns 2025-2026)

- LLM-validated code graphs: anyone shipping production tools that
  use LLMs to verify, score, or enrich static-analyzer output?
  Cursor's codebase intelligence, GitHub Copilot Workspace, JetBrains
  AI Assistant, Cody — public details on their graph-or-no-graph
  architecture?
- GraphRAG for code: real production deployments (not demos), what
  works and what doesn't?
- Code documentation generators using structural graphs: who's
  actually doing this end-to-end (graph → LLM → docs) and what
  graph features matter?
- Cross-language graph integration: production reports on
  handling polyglot codebases (Rust + TS + Python + Nix is
  genuinely uncommon — find anyone with similar shape)
- Service-to-binary linkage: who else has a "Service runs Module"
  edge concept in production graph tools? How do they extract it?
- Confidence-tiered graph edges: who else exposes per-edge confidence
  in production?
- Incremental indexing for monorepos: production reports of
  successful change-coupling-aware reindex strategies (real
  numbers on indexing-time-after-commit)
- SQLite vs RocksDB vs custom: practitioner migration stories
  from one to another for graph storage at scale

## Adversarial / failure modes (find honest limitations)

- Which patterns systematically defeat current code graphs:
  macro systems (Rust macros, Lisp macros, C preprocessor), dynamic
  metaprogramming (Python decorators, Ruby method_missing,
  Smalltalk doesNotUnderstand), code generation (protobuf,
  Cargo build scripts, Hygen), type erasure, virtual dispatch?
- Where do practitioners say static analysis hits a wall, and
  what do they do about it (give up, fall back to LLM, fall back
  to runtime tracing, hybrid)?

Output: practitioner-grade insights with citations to specific
posts/talks/repos. Specifically interested in:
- "Here's what we tried and it failed" reports
- Tools that did NOT make the cut and why
- Migration regrets ("we wish we had used X instead of Y")
- Reproducible numbers (NOT marketing claims)

Filter heavily:
- Skip projects with < 50 GitHub stars unless they have substantive
  technical content
- Skip Medium articles that are essentially press releases
- Skip "AI-generated content overview" listicles
- Prefer authored deep-dives, conference talks, podcast transcripts
- Anything older than 18 months gets a flag for staleness

Goal: identify 5-10 production techniques our team should evaluate,
each with a concrete "if we adopt this, we expect X improvement on
metric Y" claim that we can probe-test before implementing.
