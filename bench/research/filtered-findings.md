# Filtered Findings — Unified Action List

**Date**: 2026-04-25
**Sources**:
- `bench/research/intel-findings.md` (B1 — 8 community findings + 4 threads)
- `bench/research/research-findings.md` (B2 — 10 academic findings + 4 threads)
- `bench/research/filter-pass-decision-rules.md` (A2 — rules R1-R8, tier rubric, probe gates α/β/γ)

**Scope**: merge B1+B2, apply filter rules R1-R8, produce tier-assigned action list. Probe gates (Phase C3) run separately in `validated-actions.md`.

**Total unique findings after dedup**: 14 (intel 8 + research 10 − 4 cross-confirmations)

---

## Cross-source dedup summary

| B1 finding | B2 finding | Merged as | Why |
|---|---|---|---|
| I-1 (codegraph-rust/synapps/stakgraph) | Implicit in R-1/R-7 (academic validates graph+LLM consensus) | **M-1**: competitor landscape | Community side catalog; academic side validates pattern |
| I-2 (arXiv 2603.27277 upstream paper) | Implicit in R-3 (Prometheus cites similar corpus) | **M-2**: academic positioning anchor | Both point to same citation anchor |
| I-3 (LLM SAST FP filter 92→6.3%) | R-1 (LocAgent 92.7% localization), R-7 (CodexGraph Cypher) | **M-3**: LLM-augmented validation/retrieval | Community = SAST task; research = code localization; common pattern = LLM over graph |
| I-4 (Aider PageRank) | Explicitly cited by R-1 LocAgent + B2 Thread A | **M-4**: PageRank ranking layer | Convergent across both sides |

---

## Applied filter rules reference

**R1**: ≥1 cited F1 OR ≥1 academic benchmark OR Tier-C minimum
**R2**: 2-day probe / 5-day build-and-measure cap for Tier-A; 10-day max for Tier-B
**R3**: reject if requires SQLite-WAL replacement
**R4**: reject if replaces tree-sitter
**R5**: compare-by-need 5 gates (existing tool / current workflow / verified friction / cost vs value / delta framing)
**R6**: DEFER-challenge (3 questions) before Tier-C downgrade
**R7**: frontier techniques require probe definition for Tier B
**R8**: LLM-augmented → published accuracy OR shadow-mode ≥90% for auto-apply

**Probe gates** (applied in C3): Gate α (prove instrument on synthetic), Gate β (known-positive validation on our data), Gate γ (bucket the gap — ≥5pp).

---

## Tier A — IMPLEMENT (probe-gated commit within 2 weeks)

### A-1: Multi-hop agent loop with graph traversal tools (LocAgent pattern)

**Origin**: R-1 (LocAgent ACL 2025, arXiv 2503.09089) + validated by B1 Thread D (Aider PageRank) + B2 Thread A (8 papers converging)

**What**: expose a `code_localize(issue_description, depth=3)` MCP tool that takes a natural-language query and does multi-hop graph traversal with LLM-guided tool selection over our existing graph. Mimics LocAgent's interface.

**Published F1**: 92.7% file-level localization accuracy (LocAgent paper); 12% Pass@10 improvement on GitHub issue resolution.

**Filter-rule path**:
- R1 ✅ (92.7% published at ACL 2025)
- R2 ✅ (5-day build-and-measure budget: 2 days to wrap existing graph-query tools in agent loop, 2 days to benchmark against Loc-Bench, 1 day report)
- R3 ✅ (doesn't touch storage)
- R4 ✅ (doesn't touch tree-sitter)
- R5 ✅ (existing tool: MCP exposes primitives, not agent loops; workflow: Opus currently does ad-hoc Cypher; verified friction: the docs pipeline needs ranked multi-hop context; cost-vs-value: high — Loc-Bench accuracy is a known benchmark; delta: "LocAgent's agent loop over code-graph's richer edges = better accuracy than LocAgent alone")
- R6 N/A (not deferred)
- R7 ✅ (probe defined: run our graph against Loc-Bench, measure accuracy)
- R8 ✅ (LocAgent published at 92.7% which exceeds our 90% bar)

**Expected lift**: unclear until probe. Our graph has more edge types than LocAgent's; if their pattern maps, we meet or exceed 92.7%. If our graph has gaps that hurt multi-hop traversal, probe reveals them.

**Implementation cost**: ~3-5 days of engineering.

**Dependency**: need Loc-Bench dataset available + Python harness to run evaluation.

---

### A-2: PageRank ranking layer for query-relevance on the graph

**Origin**: B1 I-4 (Aider PageRank validates pattern) + B2 Thread A (LocAgent + GraphCodeAgent both use ranked retrieval)

**What**: implement query-weighted PageRank over our graph. Given a query (matched to files/functions via keyword or embedding), propagate weights through edges, return ranked list of most-relevant entities. Feeds the docs pipeline's token-budget-bounded context selection.

**Published evidence**: Aider uses this in production (43k stars); MotleyCoder validates weight-diffusion variant.

**Filter-rule path**:
- R1 ✅ (Aider documented architecture; MotleyCoder second source)
- R2 ✅ (2-day probe: PageRank is O(|V|+|E|) over our SQLite graph with query-weight injection; SciPy implementations trivial)
- R3/R4 ✅
- R5 ✅ (existing tool: community detection (Louvain) groups but doesn't rank; workflow: docs pipeline currently has no ranking; verified friction: without ranking, the graph feeds thousands of nodes into Opus context — token-inefficient; delta: "Aider pattern adapted to our richer graph = better docs-pipeline context selection")
- R7 N/A (not frontier-tagged)

**Expected lift**: measurable as "tokens to produce a correct docs-pipeline answer" — target 3-5x reduction based on Aider's reported numbers (6.8x reduction in code-review-graph, another validation source).

**Implementation cost**: ~2 days.

**Dependency**: none — can run as standalone Python script over SQLite.

---

### A-3: Cohen's Kappa for hand-oracle expansion

**Origin**: R-8 (CLEVER NeurIPS 2025 + Code-Debloating arXiv 2604.17717)

**What**: when expanding hand-oracle beyond Nix pub/sub (e.g., Python CALLS, Rust CALLS hand-oracle), have **two independent annotators** annotate the same fixtures and compute Cohen's Kappa. Standard bar: κ ≥ 0.81 = "almost perfect" agreement. Below 0.81 = revisit the fixture or the annotator protocol.

**Published evidence**: CLEVER used two-author independent spec-writing; Code-Debloating used two independent annotators with Kappa reporting.

**Filter-rule path**:
- R1 ✅ (NeurIPS 2025 + peer-reviewed methodology)
- R2 ✅ (essentially zero cost — just add a second annotator on next hand-oracle run)
- R3/R4 ✅
- R5 ✅ (existing tool: single-annotator hand-oracles; workflow: we validate F1 against a single-author fixture; verified friction: single-annotator hand-oracles carry self-bias risk; delta: "Cohen's Kappa surfaces annotator disagreement before it contaminates F1")
- R7 N/A

**Expected lift**: not measured as F1 — measured as confidence in hand-oracle correctness. Would have caught the 2 real bugs found in the Nix expansion via a different mechanism.

**Implementation cost**: near zero. Add a second annotator column to the hand-oracle JSON schema, compute Kappa with `scipy.stats.cohen_kappa_score`.

**Dependency**: a second annotator willing to spend 2-3 hours per language when we expand hand-oracle coverage.

---

### A-4: Academic positioning citations (Prometheus + LogicLoc + arXiv 2603.27277)

**Origin**: R-3, R-6, B1 I-2 (all converge on citing upstream/related academic work)

**What**: update code-graph's README/ARCHITECTURE.md to cite:
- **arXiv 2603.27277** (upstream Codebase-Memory paper — this project's academic anchor)
- **Prometheus (ICLR 2026)** as the closest multilingual-KG-for-docs-pipeline counterpart
- **LogicLoc** as the academic validation of our OPTIONAL MATCH recommendation (Datalog + soundness guarantees)
- **LocAgent** as the published F1 benchmark reference

**Published evidence**: all four sources are peer-reviewed or preprint-indexed.

**Filter-rule path**:
- R1 ✅
- R2 ✅ (1-hour task)
- R3/R4 ✅
- R5 ✅ (existing tool: no academic positioning; workflow: external collaborators read README first; verified friction: we've pitched code-graph without its academic grounding; delta: "academic citations = external credibility")
- R7 N/A

**Expected lift**: not measurable as F1. Positioning improvement.

**Implementation cost**: 1 hour.

**Dependency**: none.

---

## Tier B — PROBE-GATED BACKLOG (prototype before engineering commit)

### B-1: Dataflow edges (parameter_of, flows_to, returns, mutates)

**Origin**: B1 I-1A.2 (codegraph-rust) + prior 2026-03-23 baseline Finding 1 (optave/codegraph) + B2 Thread B (CGM motivates richer graph)

**Probe definition**:
- **Query**: "can we extract `flows_to(param, arg)` edges with F1 ≥ 0.7 on the Go synthetic fixture?"
- **Fixture**: small Go fixture (10 functions, 20 parameter-arg flows) with hand-verified ground truth
- **Threshold**: proceed if F1 ≥ 0.7; defer if below

**Filter-rule path**:
- R1 ✅ (optave/codegraph v3.0 documented pattern; codegraph-rust confirms)
- R2 ⚠️ (intraprocedural dataflow was 2026-03-23's HIGH-effort recommendation; building for one language = 5-day budget; multi-language = >5-day → Tier B)
- R7 ✅ (probe defined above)
- R8 N/A (not LLM-augmented)

**Estimated cost if probed and proceeded**: 5 days for Go only; 20+ days for all 4 stack languages.

---

### B-2: SEMANTIC_SIMILARITY first-class edges

**Origin**: R-2 (GraphCodeAgent SSCG includes cosine-similarity edges)

**Probe definition**:
- **Query**: "does adding SEMANTIC_SIMILARITY edges (from existing Voyage-4 embeddings) improve docs-pipeline retrieval accuracy on a 20-query test set?"
- **Fixture**: 20 docs-pipeline-style queries with hand-annotated "relevant nodes" ground truth
- **Threshold**: proceed if top-5 retrieval accuracy improves by ≥10pp; defer if below

**Filter-rule path**:
- R1 ⚠️ (GraphCodeAgent 94.3% number is preprint — lower confidence than LocAgent)
- R2 ✅ (2-day probe; embeddings already exist)
- R3 ✅
- R7 ✅

**Estimated cost**: 2-day probe; 3-day build if probe passes.

---

### B-3: Ego-graph subgraph MCP tool (`get_subgraph`)

**Origin**: R-7 (CodexGraph Cypher + RepoGraph ego-graphs)

**Probe definition**:
- **Query**: "does exposing `get_subgraph(node, depth=2, edge_types=...)` as MCP tool get used productively by Opus for docs-pipeline queries?"
- **Fixture**: 5 representative docs-pipeline tasks (e.g., "document Service X and its dependencies")
- **Threshold**: proceed if Opus uses the tool in ≥3/5 tasks AND it shortens average tool-call count; defer if unused or adds calls without saving token budget

**Filter-rule path**:
- R1 ✅ (CodexGraph + RepoGraph published)
- R2 ✅ (2-day probe: we already have `query_graph` Cypher endpoint; `get_subgraph` is an MCP-tool wrapper)
- R3/R4 ✅
- R7 ✅

**Estimated cost**: 2 days.

---

### B-4: LLM-augmented edge validation (FP filter pattern)

**Origin**: B1 I-3 (Sifting the Noise 92%→6.3% FP) — subject to R8 asymmetric bar

**Probe definition**:
- **Query**: "can an LLM validator reduce FP rate on our AMBIGUOUS-tier edges to <20% while retaining ≥80% TPs?"
- **Fixture**: 50 AMBIGUOUS edges sampled from PSM with hand-labeled ground truth
- **Threshold**: proceed only if TP retention ≥80% AND FP reduction ≥30pp (asymmetric bar per R8)

**Filter-rule path**:
- R1 ✅ (arXiv 2601.22952 published 92→6.3% FP with 77.7% TP retention)
- R2 ✅ (2-day probe: call Haiku/Sonnet on 50 edges with structured prompt; compute both sides)
- R7 ✅
- R8 ⚠️ (22.25% TP miss rate from the source paper is a legitimate concern; must measure TP retention directly, not just FP reduction)

**Estimated cost**: 2-day probe.

**Critical constraint**: per R8, use as "propose, human/pipeline validates" not "auto-apply" unless probe shows ≥90% combined accuracy.

---

### B-5: LSP-per-language extraction (cost-gated)

**Origin**: B1 I-1A (codegraph-rust demonstrates the pattern with fast/balanced/full tiers)

**Probe definition**:
- **Query**: "does adding rust-analyzer LSP for Rust CALLS extraction lift F1 from current 0.74 to ≥0.90 without breaking the existing Go-LSP path?"
- **Fixture**: existing Rust CALLS F1 harness (psm)
- **Threshold**: proceed if F1 ≥ 0.90 AND LSP timeout rate <5%; defer if LSP hangs too often or F1 lift <10pp

**Filter-rule path**:
- R1 ✅ (codegraph-rust pattern + rust-analyzer documented)
- R2 ⚠️ (5-day probe for Rust only; LSP bridge in Go is non-trivial)
- R7 ✅
- **Adversarial concern**: codegraph-rust has 600s default LSP timeout, indicating brittle UX

**Estimated cost**: 5-day probe for Rust; similar for each additional language.

---

### B-6: Framework-aware dead-code detection

**Origin**: B1 side-finding (Axon, synapps `find_dead_code`) + R-7 (RepoGraph)

**Probe definition**:
- **Query**: "can we detect dead functions with <5% FP rate on a decorator-heavy Python fixture (framework dispatch muddies 'zero callers')?"
- **Fixture**: Python fixture with Flask decorators, 20 functions, 5 known-dead + 15 live
- **Threshold**: proceed if FP ≤1 (≤5% of live flagged as dead)

**Filter-rule path**:
- R1 ⚠️ (Axon claim only; no published F1)
- R2 ✅ (2-day probe; mostly scoring existing graph data)
- R7 ✅

**Estimated cost**: 2-day probe.

---

## Tier C — AWARENESS (monitor, no action)

### C-1: Indexing tiers (fast/balanced/full)
- **Source**: B1 I-1A.3 (codegraph-rust)
- **Why Tier C**: R5 gate 3 fail — no documented user friction with current single-tier indexing. DEFER-challenge: no incident, no broader-label friction, no cost-inflation concern. Legitimate defer.

### C-2: GraphRAG/LazyGraphRAG patterns for docs pipeline
- **Source**: B1 I-7 + R-4 CGM (graph-aware attention)
- **Why Tier C**: not a code-graph code change; belongs in docs-pipeline planning doc

### C-3: FlowLog / Differential Dataflow for incremental queries
- **Source**: R-5 (VLDB 2026)
- **Why Tier C**: R3 fail (requires Datalog backend); current file-level incrementality is sufficient until query workload demands declarative incremental maintenance

### C-4: TransRepo-Bench fixture taxonomy
- **Source**: R-9 (EMNLP 2025)
- **Why Tier C**: methodology-only; could become Tier B if we decide to formalize an adversarial fixture suite

### C-5: RAG code generation survey venue map
- **Source**: R-10 (arXiv 2510.04905)
- **Why Tier C**: bibliographic anchor; no implementation

### C-6: CGM graph-aware attention
- **Source**: R-4 (NeurIPS 2025)
- **Why Tier C**: requires model fine-tuning (out of scope); applicable only to docs-pipeline, not code-graph

### C-7: Cursor / Augment / competitor benchmarks
- **Source**: B1 I-5, I-8
- **Why Tier C**: validation-only, no code change

### C-8: Macros/decorators remain unsolved (confirm AMBIGUOUS strategy)
- **Source**: B1 I-6 + confirmed across academic side
- **Why Tier C**: validation of existing confidence-tier strategy; no change needed

---

## Tier D — REJECT

None. All findings had at least some merit; nothing rose to the "vendor marketing without numbers + duplicate + compare-by-need gate 3 fail" threshold.

---

## Summary table

| Tier | Count | Items |
|---|---|---|
| A (implement) | 4 | A-1 LocAgent loop, A-2 PageRank, A-3 Cohen's Kappa, A-4 academic citations |
| B (probe-gated) | 6 | Dataflow edges, SEMANTIC_SIMILARITY, ego-graph tool, LLM edge validation, Rust LSP, framework-aware dead code |
| C (awareness) | 8 | Indexing tiers, GraphRAG for docs, FlowLog, TransRepo fixture, RAG survey, CGM attention, competitor benchmarks, macro/decorator AMBIGUOUS confirm |
| D (reject) | 0 | — |

**Tier-A cap per filter-pass-decision-rules.md = 5 items max. We're at 4 — under cap.**

---

## Carrying into Phase C3 (probe gates)

The 4 Tier-A items need probe gates α/β/γ applied inline:

| Item | Gate α (instrument) | Gate β (known-positive) | Gate γ (bucket gap) |
|---|---|---|---|
| A-1 LocAgent loop | Need Loc-Bench + harness | Need known-match case | Not directly targeting a gap bucket |
| A-2 PageRank ranking | Need small graph with known ranking | Need query with known-top-5 result | Not a gap-closing recommendation per se |
| A-3 Cohen's Kappa | Trivial (compute on 2 existing annotations if available) | N/A | N/A (methodology) |
| A-4 Academic citations | N/A (not a measurement) | N/A | N/A |

**A-3 and A-4 are near-zero-cost implementations and don't need probe gates.**

**A-1 and A-2 need explicit probes before engineering commit.** The probes themselves fit within budget:
- A-1 probe: ~3 hours to download Loc-Bench, run our graph against it, compute file-level accuracy
- A-2 probe: ~2 hours to implement weighted PageRank in Python over SQLite

Both fit within the 30-min-per-candidate cap only if scoped to "prove instrument works" not "full evaluation." For C3:
- A-1 Gate α: run on a 3-function synthetic fixture first (under 15 min)
- A-2 Gate α: run on a 5-node synthetic graph with hand-computed PageRank (under 15 min)

The full Loc-Bench/retrieval evaluation becomes the implementation step, not the probe.

---

**Next**: `validated-actions.md` runs probe gates on A-1 and A-2, auto-confirms A-3 and A-4, demotes to Tier-B if any probe fails.
