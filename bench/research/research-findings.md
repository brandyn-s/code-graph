# Research Frontier — Code-Graph Findings

**Run date**: 2026-04-25
**Skill**: `/gather-research` (community-first run — gather-intel ran earlier in session)
**Tavily credits**: ~20 (Wave 1: 10, Wave 2: 10) + arxiv-mcp queries (rate-limited, used as supplement)
**Waves**: 2 (convergence reached)
**Cross-reference target**: `bench/research/intel-findings.md` (B1 output)
**Prior baseline**: `knowledge-base/research/2026-03-23-code-graph-improvements.md` (skipped re-audit; baseline already integrated in B1 currency check)

---

## Phase A: Currency cross-reference vs intel findings

The community intel run (B1) surfaced 8 findings + 4 community threads. This research run validates or contradicts each from the academic side:

| Intel finding | Research validation status | Notes |
|---|---|---|
| I-1 (codegraph-rust/synapps/stakgraph competitors) | **CONFIRMED** — academic literature (CodexGraph, RepoGraph, RepoUnderstander, OrcaLoca) shows graph+LLM is consensus pattern |
| I-2 (arXiv 2603.27277 upstream paper) | **CONFIRMED** — paper exists in literature corpus; cited by recent SE work |
| I-3 (LLM SAST FP filter 92→6.3%) | **EXPANDED** — academic frontier extends beyond SAST: LocAgent shows 86% cost reduction at 92.7% accuracy on code localization (different task) |
| I-4 (Aider PageRank repo-map) | **CONFIRMED** — referenced as foundational prior work in LocAgent (ACL 2025) and CodeFuse-CGM (NeurIPS 2025) |
| I-5 (Cursor benchmark numbers) | **N/A** — vendor-specific, no academic counterpart |
| I-6 (macros/decorators unsolved) | **CONFIRMED** — no academic paper claims to solve this; treated as fundamental limitation in CPG literature (Qwiet white paper, FSE 2025 MiSum) |
| I-7 (GraphRAG patterns for docs pipeline) | **EXPANDED** — academic GraphRAG variants for code: GraphCodeAgent dual-graph, CGM graph-aware attention, Prometheus unified graph |
| I-8 (Codebase Memory MCP positioning) | **CONFIRMED** — fits the "structured graph + LLM agent" academic category |

**No contradictions.** Academic frontier extends and validates the community findings rather than contradicting them.

---

## Phase B: New academic findings

### Finding R-1: LocAgent (ACL 2025, arXiv 2503.09089) — graph-guided code localization with measured F1

**Claim**: heterogeneous code graph + multi-hop reasoning enables 92.7% file-level code localization accuracy. Fine-tuned Qwen2.5-Coder-32B-CL matches SOTA proprietary models at **86% cost reduction**. Pass@10 GitHub issue resolution improved by 12%.

**Evidence quality**: T1 (peer-reviewed at ACL 2025 main conference) + published F1 + open-source code + open-weight models + Loc-Bench dataset.

**Architecture**:
- Directed heterogeneous graph: files, classes, functions as nodes; imports, invocations, inheritance as edges
- "Lightweight" indexing — seconds per repository
- Multi-hop reasoning via agent loop (similar to ReAct)

**Implication for code-graph**:
- Our existing graph schema is rich enough to support LocAgent-style queries (we have files, classes, functions, imports, calls, inheritance — all of LocAgent's nodes/edges)
- Missing: **multi-hop agent loop with graph-traversal tools**. Currently our MCP exposes node/edge primitives; LocAgent shows that wrapping these in a "search by issue description, traverse multi-hop" agent loop produces 92.7% accuracy.
- Loc-Bench is a usable F1 benchmark for our extractors (worth running our graph against it).

**Filter pre-classification**: Tier B (probe-gated). R7 trigger: probe = run our graph against Loc-Bench, measure file-level localization accuracy. R2 fits 5-day budget. R8 LLM bar — accuracy is published at 92.7% which exceeds our 90% threshold.

### Finding R-2: GraphCodeAgent (arXiv 2504.10046) — dual-graph code generation with semantic-similarity edges

**Claim**: Up to **94.3% gain in cross-file dependency tasks** via dual-graph (Requirement Graph + Structural-Semantic Code Graph) and ReAct multi-hop reasoning.

**Evidence quality**: T2 (preprint, no venue confirmation yet; methodology documented; numbers benchmark-specific so should be verified independently).

**Key architectural pattern**: SSCG includes **`semantic_similarity` edges** added via code-embedding cosine similarity, alongside structural edges (import/containment/inheritance/method invocation).

**Implication for code-graph**: We already have the structural edges. The new pattern is **adding `SEMANTIC_SIMILARITY` as a first-class edge type** populated by our existing Voyage-4 embeddings. Currently our embeddings are surfaced via separate code-search project; a unified edge-typed approach lets queries traverse both structural and semantic paths in one pass.

**Filter pre-classification**: Tier B (probe-gated). The probe is: add SEMANTIC_SIMILARITY edges to one project (cosine threshold to be calibrated); measure agent retrieval accuracy improvement. R7 trigger satisfied. R3 storage — fits SQLite WAL.

### Finding R-3: Prometheus (ICLR 2026 submission) — unified KG for multilingual codebases + long-term memory

**Claim**: Multilingual code repositories transformed into "unified knowledge graph" with files + ASTs + natural-language text encoded as typed nodes, **5 general edge types** for cross-language support. Multi-agent system with long-term memory ("Athena"). Specifically targets "real-world issues beyond benchmark settings" (i.e., beyond Python-only SWE-bench).

**Evidence quality**: T1 (ICLR 2026 submission, OpenReview accessible).

**Direct relevance**: This is the **closest academic match for our use case** — Rust+TS+Python+NixOS multilingual graph feeding an Opus-coordinated docs pipeline. The "5 general edge types" approach mirrors our schema-less string edge design.

**Implication for code-graph**: 
- Validates our multilingual cross-edge approach (Topic, Service, RUNS_BINARY edges shipped this session)
- The "Athena" long-term memory pattern (persisting agent reasoning over the graph) is novel — not currently in code-graph but maps cleanly to MCP-exposed memory

**Filter pre-classification**: Tier A (validation). Cite in our architecture docs alongside arXiv 2603.27277 as positioning. The Athena memory pattern could become a Tier B probe later.

### Finding R-4: Code Graph Model / CGM (NeurIPS 2025, arXiv 2505.16901, codefuse-ai/CodeFuse-CGM) — graph-aware attention in LLM

**Claim**: CGM-72B-V1.2 achieves **44.00% resolution rate on SWE-Bench-Lite** using open-weight Qwen2.5-72B + LoRA fine-tuning + graph-aware attention mask derived from code-graph adjacency matrix. **Ranks #1 among open-weight models**. Outperforms agent-based methods.

**Evidence quality**: T1 (NeurIPS 2025 accepted poster) + open-source code + open-weight model + leaderboard ranking.

**Key innovation**: graph structure integrated **directly into LLM attention mechanism** (not just via prompting). Replace causal attention mask between node tokens with adjacency-matrix-derived mask. Mimics GNN message passing.

**Implication for code-graph**:
- This is **not directly applicable** to our extraction layer — it's a downstream consumer pattern (LLM with graph-aware attention).
- However, it demonstrates that **graph quality matters all the way to LLM inference**: CGM authors showed 12.33pp lift over the previous SOTA (Moatless+DeepSeek-V3) primarily by feeding richer graph context.
- Directly motivates our docs pipeline: Opus + our graph + agentless RAG framework should outperform agent-based approaches if the graph is rich enough.

**Filter pre-classification**: Tier C (awareness for code-graph itself, but Tier A for docs-pipeline planning). Not a code-graph code change.

### Finding R-5: FlowLog (VLDB 2026, arXiv 2511.00865) — Datalog via Differential Dataflow for incremental code analysis

**Claim**: Beats Soufflé/DDlog/RecStep/DuckDB/Umbra on the most comprehensive Datalog benchmark suite. Cascading semijoins for early prefilter. Worst-case-aware optimizer. **Built on Differential Dataflow** — natively supports incremental queries and recursive aggregates.

**Evidence quality**: T1 (VLDB 2026 accepted) + open code + reproducible benchmarks.

**Implication for code-graph**:
- We currently don't use Datalog — our query engine is a custom Cypher subset. **FlowLog isn't a drop-in replacement.**
- However: the Differential Dataflow primitive (Frank McSherry's work) underpins Materialize and shows that incremental graph queries are a tractable engineering problem.
- For our incremental indexing layer, Differential Dataflow could replace our content-hash + mtime polling **only if we needed declarative incremental query maintenance**. We currently don't — incrementality is at the file level (re-extract changed files, re-resolve affected edges) which is simpler.
- Awareness item: when our docs pipeline starts running expensive aggregate queries that need to update on every code change, FlowLog or Differential Dataflow becomes the right answer.

**Filter pre-classification**: Tier C (awareness). Doesn't pass R3 (would require Datalog backend). Not Tier B because no concrete current friction.

### Finding R-6: LogicLoc (arXiv 2604.16021) — Neurosymbolic code localization with Datalog + LLM

**Claim**: Static analysis extracts program facts (Datalog representation) → LLM agent synthesizes Datalog query from natural-language issue → Soufflé executes precise lookup. Reports specific examples (e.g., "find functions with >15 parameters" → exact 2-function match).

**Evidence quality**: T2 (preprint; no venue yet).

**Implication for code-graph**:
- Pattern: **expose graph as queryable IR; let LLM compose precise queries**. We already do this via MCP tools and Cypher queries. The novelty in LogicLoc is letting the LLM compose Datalog queries with formal soundness guarantees.
- For our use case (Opus-coordinated docs pipeline), the equivalent is: expose Cypher subset to Opus and let it compose complex queries. Our Cypher already supports MATCH/WHERE/RETURN/variable-length paths. Adding **OPTIONAL MATCH** (the Tier-A item from 2026-03-23 baseline) is the key prerequisite — soundness of "show me all X, plus Y if it exists" requires it.

**Filter pre-classification**: Tier A (validates 2026-03-23 OPTIONAL MATCH recommendation). LogicLoc citation strengthens the case.

### Finding R-7: CodexGraph (Liu et al., 2024) + RepoGraph (ICLR 2025) — graph-RAG production patterns

**Claim**: 
- CodexGraph: indexes repository into Neo4j; LLM agents query precisely via Cypher
- RepoGraph: subgraph retrieval via ego-graphs (extracts subgraph centered on search term)

**Evidence quality**: T2 (CodexGraph preprint) + T1 (RepoGraph ICLR 2025).

**Implication for code-graph**:
- CodexGraph validates Cypher-as-LLM-interface pattern — exactly what we expose via MCP
- RepoGraph's ego-graph subgraph retrieval is a **graph-traversal pattern we don't currently have a tool for**. Currently MCP tools return individual nodes/edges; ego-graph would be: "give me all nodes within 2 hops of X with edges of type Y."

**Filter pre-classification**: Tier B (probe-gated). Add `get_subgraph(node_id, depth, edge_types)` as MCP tool. Probe = test if Opus uses it productively for docs-pipeline queries.

### Finding R-8: Hand-oracle methodology — published cost data from CLEVER (NeurIPS 2025) and Code-Debloating (arXiv 2604.17717)

**Claim**: 
- **CLEVER (NeurIPS 2025)**: 161 hand-curated formally verified problems. Average **25 minutes per problem** for spec writing + 15 minutes for review. Some problems took >1 hour.
- **Code-Debloating ground-truth (arXiv 2604.17717)**: 11 ground-truth payload applications (5K-76K LoC), two-author independent annotation, **Cohen's Kappa for inter-annotator agreement** (>0.81 = "almost perfect" benchmark).

**Evidence quality**: T1 (NeurIPS) + T2 (arXiv).

**Implication for code-graph**:
- **Validates our n=20 hand-oracle methodology** for Nix pub/sub (we hit F1=1.000 because the pattern is more constrained than CLEVER's formal specs)
- **Adds Cohen's Kappa as a methodology improvement**: when we expand hand-oracle to other languages (Rust CALLS, Python CALLS), we should have **two independent annotators** and report Kappa. Currently single-annotator hand-oracles run a self-bias risk.

**Filter pre-classification**: Tier A (validation + small methodology improvement). Adopting Cohen's Kappa for future hand-oracle expansion is zero-cost — just add a second annotator on critical fixtures.

### Finding R-9: TransRepo-Bench (EMNLP 2025) — repository translation benchmark with measured fix times

**Claim**: 13 repository-level translation tasks; per-repo skeleton fix times **25-270 minutes**, design-pattern complexity tiers, multi-model comparison (GPT/Claude/Qwen/DeepSeek).

**Evidence quality**: T1 (EMNLP 2025).

**Implication for code-graph**: 
- Not directly a code-graph task, but the benchmark methodology (measured fix times across complexity tiers) is the right model for our **Adversarial fixture taxonomy (R15)**.
- We could build "code-graph stress tests" with similar tiering: simple modules / framework-heavy / decorator-heavy / macro-heavy / type-erasure-heavy.

**Filter pre-classification**: Tier C (methodology awareness). Could become Tier B if we want to formalize an adversarial fixture suite.

### Finding R-10: Retrieval-Augmented Code Generation Survey (arXiv 2510.04905) — venue corpus map

**Claim**: 110 papers reviewed across ICSE/FSE/ASE/ACL/EMNLP/NeurIPS/ICLR/ICML. Venue concentration documented.

**Evidence quality**: T1 (recent survey).

**Implication for code-graph**: **Reading list anchor**. The dominant venues for code-graph + LLM work are FSE/ICSE/ASE on the SE side, ACL/EMNLP/NAACL on the NLP side, NeurIPS/ICLR/ICML on the ML side. Our prior baseline focused on JETIR/ResearchGate; this survey re-anchors us to top-tier venues going forward.

**Filter pre-classification**: Tier C (awareness; bibliographic anchor).

---

## Research Threads (3+ converging papers)

### Thread A: Graph + LLM agent integration is the dominant 2024-2026 academic frontier
**Sources**: LocAgent (ACL 2025), GraphCodeAgent (preprint), Prometheus (ICLR 2026 sub), CGM (NeurIPS 2025), CodexGraph, RepoGraph (ICLR 2025), RepoUnderstander, OrcaLoca, MiSum (FSE 2025), arXiv 2603.27277.
**Convergence**: 8+ independent papers. Strong consensus.
**Validates**: B1 Thread A (tree-sitter + LSP is production reference); academic side confirms graph extraction is upstream of nearly all code-LLM work.

### Thread B: Graph-aware attention in LLM (vs prompt-based RAG) is emerging
**Sources**: CGM (NeurIPS 2025) graph-aware attention mask; Program Structure-aware Language Models (arXiv 2604.17715) tightly integrated structure into model.
**Convergence**: 2 papers — emerging, not consensus.
**Implication**: For our docs pipeline: prompting Opus with graph context is current SOTA; fine-tuning Opus with graph-aware attention is frontier.

### Thread C: Datalog / Differential Dataflow for incremental code analysis is plausible but not necessary
**Sources**: FlowLog (VLDB 2026), LogicLoc (preprint), classic Soufflé.
**Convergence**: 3 papers, but for code-graph use case the gap is theoretical, not practical.
**Implication**: Awareness only; our current incrementality (file-level re-extraction) is fit for purpose.

### Thread D: Hand-oracle methodology has measured cost benchmarks
**Sources**: CLEVER (NeurIPS 2025), Code-Debloating (arXiv 2604.17717), TRANSREPO-BENCH (EMNLP 2025).
**Convergence**: 3 papers with concrete cost numbers (25 min/spec, Kappa >0.81 standard, 25-270 min/repo skeleton).
**Implication**: Our hand-oracle approach is methodologically sound; adding Cohen's Kappa for multi-annotator is the cheap improvement.

---

## Adversarial / Counter-evidence

| Finding | Counter-evidence | Status |
|---|---|---|
| R-1 (LocAgent 92.7%) | LocAgent uses Loc-Bench, which they constructed themselves; cross-benchmark generalization unverified | **Plausible bias**: probe their tools on independent benchmark before adopting wholesale |
| R-2 (GraphCodeAgent 94.3%) | Single preprint; numbers benchmark-specific; semantic similarity edges add storage cost not quantified | **Verify by probe** before committing |
| R-3 (Prometheus multilingual KG) | ICLR 2026 SUBMISSION not accepted; OpenReview status unclear | Watch for acceptance/revision; treat as preprint-tier |
| R-4 (CGM 44% on SWE-Bench-Lite) | SWE-Bench-Lite is Python-only; multilingual generalization unproven | True for code-graph use case (multilingual) — discount |
| R-5/R-6 (Datalog + LLM) | LogicLoc preprint examples cherry-picked; no large-scale F1 vs alternatives | Legitimate concern; awareness only |

---

## Pre-classification for filter pass

| # | Finding | Likely tier | Filter rule that fires |
|---|---|---|---|
| R-1 | LocAgent multi-hop agent loop with graph traversal tools | **B (probe)** | R1 cleared (92.7% published F1); R2 fits 5-day probe (run our graph against Loc-Bench); R5 cleared; R7 satisfied (probe = Loc-Bench accuracy) |
| R-2 | GraphCodeAgent SEMANTIC_SIMILARITY first-class edge | **B (probe)** | R1 mixed (preprint); R2 fits 2-day probe (add edges, measure retrieval); R5 cleared; R7 satisfied |
| R-3 | Prometheus multilingual KG — direct architecture validation | **A (validation)** | Cite alongside arXiv 2603.27277 in code-graph docs. Zero implementation. |
| R-4 | CGM graph-aware attention | **C (awareness)** | Belongs in docs-pipeline planning, not code-graph. Out-of-scope per filter exclusion (would require model fine-tuning). |
| R-5 | FlowLog Differential Dataflow | **C (awareness)** | R3 fail (would require Datalog backend); current incrementality is sufficient |
| R-6 | LogicLoc — validates OPTIONAL MATCH gap | **A (validation)** | Strengthens 2026-03-23 OPTIONAL MATCH recommendation; cite as additional evidence |
| R-7 | CodexGraph + RepoGraph — ego-graph subgraph retrieval | **B (probe)** | R1 cleared (CodexGraph + RepoGraph published); R2 fits 2-day probe (add `get_subgraph` MCP tool); R5 cleared |
| R-8 | Cohen's Kappa for hand-oracle | **A (implement, low cost)** | R1 cleared (CLEVER + Cohen's Kappa published methodology); R2 fits trivial cost (add second annotator on next fixture); R5 cleared (gap = single-annotator self-bias risk) |
| R-9 | TransRepo-Bench fixture taxonomy | **C (awareness)** | Methodology only; not code-graph code change |
| R-10 | RAG survey venue corpus | **C (awareness)** | Bibliographic anchor; no implementation |

**Tentative shape**: 3 Tier-A (R-3 validation, R-6 validation, R-8 implement-cheap), 3 Tier-B (R-1 LocAgent probe, R-2 SEMANTIC_SIMILARITY probe, R-7 ego-graph subgraph), 4 Tier-C.

---

## Sources (unique URLs)

| # | URL | Tier | Key contribution |
|---|---|---|---|
| 1 | https://arxiv.org/abs/2503.09089 | T1 | LocAgent ACL 2025 — 92.7% file-level localization, 86% cost reduction |
| 2 | https://github.com/gersteinlab/LocAgent | T1 | LocAgent open code + Loc-Bench dataset |
| 3 | https://aclanthology.org/2025.acl-long.426.pdf | T1 | LocAgent peer-reviewed paper |
| 4 | https://arxiv.org/abs/2504.10046 | T2 | GraphCodeAgent dual-graph, 94.3% cross-file |
| 5 | https://www.emergentmind.com/topics/graphcodeagent | T3 | GraphCodeAgent technical summary |
| 6 | https://openreview.net/forum?id=bPGZi7X5vH | T1 | Prometheus ICLR 2026 submission |
| 7 | https://github.com/EuniAI/Prometheus | T1 | Prometheus open-source platform |
| 8 | https://arxiv.org/pdf/2505.16901 | T1 | CGM NeurIPS 2025 — graph-aware attention, 44% SWE-Bench-Lite |
| 9 | https://github.com/codefuse-ai/CodeFuse-CGM | T1 | CGM open code + leaderboard |
| 10 | https://neurips.cc/virtual/2025/poster/117200 | T1 | CGM NeurIPS 2025 poster page |
| 11 | https://arxiv.org/abs/2511.00865 | T1 | FlowLog VLDB 2026 — Datalog + Differential Dataflow |
| 12 | https://arxiv.org/html/2604.16021v2 | T2 | LogicLoc — neurosymbolic code localization |
| 13 | https://arxiv.org/html/2604.17717v1 | T1 | Code Debloating ground-truth — Cohen's Kappa, 11 hand-curated |
| 14 | https://aclanthology.org/2025.findings-emnlp.986.pdf | T1 | TRANSREPO-BENCH EMNLP 2025 — 25-270 min skeleton fix times |
| 15 | https://neurips.cc/virtual/2025/poster/121730 | T1 | CLEVER NeurIPS 2025 — 25 min/spec hand-oracle cost |
| 16 | https://arxiv.org/html/2505.13938v1 | T1 | CLEVER paper |
| 17 | https://arxiv.org/html/2510.04905v1 | T1 | RAG code generation survey — 110 papers, venue distribution |
| 18 | https://arxiv.org/html/2504.10499v2 | T1 | Graph-based RAG survey |
| 19 | https://arxiv.org/html/2604.17715v1 | T2 | Program Structure-aware LM — graph integrated into model |
| 20 | https://github.com/tirth8205/code-review-graph | T4 | code-review-graph: 6.8x token reduction, <2s incremental |
| 21 | https://www.linkedin.com/posts/harshkedia17_*/Axon | T4 | Axon: Leiden + framework-aware dead code, 11 analysis phases |

---

## Recommendation summary

The academic frontier confirms the community findings (intel B1) and adds three concrete directions:

**Immediate wins (Tier A)**:
1. **R-8 Cohen's Kappa** on hand-oracle expansion — multi-annotator on next fixture, near-zero cost
2. **R-3 + R-6** academic citations (Prometheus, LogicLoc) to strengthen positioning in code-graph docs and the OPTIONAL MATCH recommendation
3. (already in B1) **PageRank ranking layer** for query-relevance over our graph

**Probe-gated (Tier B)**:
1. **R-1** — Run our extracted graph against LocAgent's Loc-Bench. Compare our F1 against their 92.7%. If we're competitive, add a "code-localization" MCP tool that mirrors their multi-hop interface.
2. **R-2** — Add `SEMANTIC_SIMILARITY` first-class edge from our existing Voyage-4 embeddings. Probe = retrieval accuracy on docs-pipeline test queries.
3. **R-7** — Add `get_subgraph(node, depth, edge_types)` MCP tool (ego-graph pattern). Probe = does Opus use it productively in docs-pipeline?

**Awareness (Tier C)**:
- CGM graph-aware attention (docs-pipeline planning, not code-graph itself)
- FlowLog/Differential Dataflow (revisit only if expensive aggregate queries become a bottleneck)
- TransRepo-Bench fixture taxonomy (methodology reference)

The biggest novel buildable: **the multi-hop agent loop with graph traversal tools (R-1)**. Our existing graph + MCP tools cover the data layer; what we lack is the agent-loop interface that LocAgent published F1 numbers for. This is a 5-day probe to attempt.

Combined with the B1 PageRank ranking layer recommendation, the action set converges on the same theme: **the graph extraction layer is competitive; the missing layer is the agent-traversal/ranking interface that transforms graph data into LLM-usable context**.

**Ready for**: Phase C filter pass (merge intel + research findings, apply rules R1-R8, run probe-first gates on Tier-A items).
