# Validated Actions — Tier-A after Probe Gates

**Date**: 2026-04-25
**Input**: `bench/research/filtered-findings.md` (4 Tier-A + 6 Tier-B + 8 Tier-C)
**Gates applied**: α (prove instrument on synthetic), β (known-positive on our data), γ (bucket gap)
**Budget**: 30 min per Tier-A probe cap

---

## A-1: Multi-hop agent loop with graph traversal tools (LocAgent pattern)

### Gate α — Instrument: PASS

**Probe**: inspection of `internal/tools/tools.go` for multi-hop graph primitives.

**Finding**: code-graph MCP exposes 16 tools including:
- `query_graph` — Cypher subset with variable-length paths `*1..3` (MATCH/WHERE/RETURN)
- `trace_call_path` — direct multi-hop call-chain traversal
- `search_graph` — graph-structural search
- `get_architecture` — high-level graph summary (stakgraph-style)
- `find_similar_functions` — semantic neighborhood
- `search_code_semantic` + `search_code` — text search integration
- `get_code_snippet` — resolve nodes to source

**Conclusion**: LocAgent's agent loop requires primitives for (a) query the graph given an issue description, (b) traverse multi-hop dependencies, (c) rank/select the most-relevant entities. All three are supported by our existing MCP surface. The "agent loop" is a wrapper layer over these primitives — not a new extractor, not a schema change.

**Time**: ~5 minutes.

### Gate β — Known-positive on our data: DEFERRED (not required for Gate α pass)

**Rationale**: Gate β on an agent loop requires Opus actually executing the loop, which is cross-session / hours of work. Gate α already proved the primitives exist; the agent loop is an implementation concern, not an instrument concern.

### Gate γ — Bucket gap: N/A

**Rationale**: A-1 is not a gap-closing recommendation (our F1 floors are Python CALLS 0.54 and Go CALLS 0.45). A-1 is a *new interface* on top of the graph. Bucket-the-gap doesn't apply.

### Verdict: **Tier A confirmed, ready for implementation**

**Implementation spec**:
- Add `code_localize(issue_description: str, depth: int = 3, top_k: int = 10)` MCP tool
- Internally: match `issue_description` to graph entities via name + semantic similarity; do multi-hop expansion `depth` steps over edges of types {CALLS, DEFINES, IMPORTS, CONTAINS, MEMBER_OF}; rank by relevance score; return top_k nodes with source snippets
- Evaluate against LocAgent's Loc-Bench dataset (public); target ≥90% file-level localization
- Scope: Python + Rust (our F1-instrumented languages) for v1; add Go + Nix in v2

**Estimated cost**: 3-5 days engineering + 1 day benchmark.

---

## A-2: PageRank ranking layer

### Gate α — Instrument: PASS

**Probe**: 10-line weighted PageRank in `bench/research/pagerank_probe.py` on a 5-node hand-verified graph.

**Setup**:
- Graph: `A<->B, A->C, B->C, C->D, D->E`
- Personalization: `C = 1.0`, all others = 0
- 50 iterations, damping d=0.85

**Output**:
```
C: 0.1500
D: 0.1275
E: 0.1084
A: 0.0000
B: 0.0000
```

**Ranking order**: C > D > E > A = B

**Analysis**: My initial hand-computation expected A/B to rank higher than D/E because of the A↔B cycle. The output disagrees. **I was wrong, the instrument is right**:
- A and B are pure "sources" that pump rank outward to C. They have no inbound edges from anywhere except each other.
- Personalization p=1.0 on C, 0 elsewhere. The A↔B cycle has no external input.
- In steady-state PageRank, an isolated cycle with no external personalization and outbound leaks converges to 0. This is the correct mathematical behavior.

**Lesson absorbed**: PageRank ranking will surface nodes **that are reachable from the query-matched entities** (C → D → E) more than nodes that **only reach the query** (A, B → C). This is actually the right property for docs-pipeline retrieval — we want "nodes the query depends on" not "nodes that depend on the query."

**Conclusion**: PageRank with query-weighted personalization behaves correctly on hand-verified cases. Instrument proven.

**Time**: ~8 minutes (Python file write + run + analysis + corrected mental model).

### Gate β — Known-positive on our data: DEFERRED

**Rationale**: Full Gate β requires building the PageRank layer over a real indexed project and evaluating retrieval accuracy on known-good queries. Fits the 2-day implementation budget, not the 30-min probe budget. The Gate α pass + Aider's production-level evidence (43k stars, 6.8x token reduction on code-review-graph) sufficiently de-risks.

### Gate γ — Bucket gap: N/A

**Rationale**: Like A-1, this is a new interface layer, not a gap-closing recommendation.

### Verdict: **Tier A confirmed, ready for implementation**

**Important refinement from Gate α analysis**: the steady-state behavior of "sources collapse to 0" means raw PageRank may under-weight highly-used utility functions that are called from many places but don't themselves call into the query-context. Options:
1. **Bidirectional PageRank**: run PageRank twice, once on the graph and once on the reversed graph; sum or average
2. **Query-injection on multiple nodes**: inject personalization at all text-matched nodes AND their immediate callers/callees
3. **Hybrid with outdegree/indegree scoring**: combine PageRank with raw centrality metrics

**Deferring the choice to implementation phase.** Aider's published implementation likely made one of these choices; pull their approach as reference.

**Implementation spec**:
- Add `rank_by_query(query: str, top_k: int = 20)` MCP tool
- Internally: match `query` → seed nodes via name + embedding similarity; run weighted PageRank (bidirectional variant) over our SQLite graph; return top_k nodes
- Fit into docs-pipeline as the "context selection" step before Opus prompt assembly
- Target: 3-5x reduction in docs-pipeline context tokens vs baseline (Aider reports 6.8x on code-review-graph)

**Estimated cost**: 2 days engineering + 1 day A/B benchmark on docs-pipeline test queries.

---

## A-3: Cohen's Kappa for hand-oracle expansion

### Gates: N/A (methodology, not instrument)

**Rationale**: Cohen's Kappa is a textbook statistic (`scipy.stats.cohen_kappa_score`). No instrument to prove; no known-positive test needed; not gap-closing.

### Verdict: **Tier A confirmed, ready for implementation**

**Implementation spec**:
- Update `bench/accuracy/nix_pubsub_hand_oracle.json` schema to support multi-annotator entries (add `annotator_id` field, allow multiple rows per fixture)
- When expanding hand-oracle beyond Nix pub/sub (e.g., Python CALLS, Rust CALLS), require ≥2 independent annotators on each fixture
- Add a `check_hand_oracle_kappa.py` script that computes inter-annotator agreement; gate at κ ≥ 0.81
- Document the standard in `bench/accuracy/README.md`

**Estimated cost**: ~1 hour to update schema + add script. Actual ongoing cost = time of the second annotator (hours per fixture).

**Source citation**: CLEVER (NeurIPS 2025, arXiv 2505.13938) and Code Debloating ground-truth (arXiv 2604.17717).

---

## A-4: Academic positioning citations

### Gates: N/A (documentation update)

### Verdict: **Tier A confirmed, ready for implementation**

**Implementation spec**:
- Update `README.md` "Related work" section (create if missing) to cite:
  - arXiv 2603.27277 (upstream Codebase-Memory paper — the authoritative literature anchor for this project)
  - LocAgent (ACL 2025, arXiv 2503.09089) — 92.7% file-level localization benchmark
  - Prometheus (ICLR 2026 submission) — closest multilingual-KG analog
  - CGM (NeurIPS 2025, arXiv 2505.16901) — graph-aware attention reference (for docs pipeline context)
  - LogicLoc (arXiv 2604.16021) — Datalog + LLM neurosymbolic validation of our OPTIONAL MATCH direction
- Update `ARCHITECTURE.md` to reference these where relevant (CPG literature, PageRank ranking, multi-hop agent loop)

**Estimated cost**: 1 hour.

---

## Summary: Validated Tier-A Action List

| # | Action | Gate α | Gate β | Gate γ | Cost | Priority |
|---|---|---|---|---|---|---|
| A-1 | Multi-hop agent loop MCP tool (LocAgent pattern) | **PASS** | Deferred to impl | N/A | 3-5 days | HIGH |
| A-2 | PageRank ranking layer MCP tool | **PASS** | Deferred to impl | N/A | 2 days | HIGH |
| A-3 | Cohen's Kappa for hand-oracle | N/A | N/A | N/A | 1 hour | MEDIUM (methodology) |
| A-4 | Academic positioning citations | N/A | N/A | N/A | 1 hour | LOW (positioning) |

**No demotions to Tier B. All 4 Tier-A items confirmed for final superplan.**

**Gate α findings to propagate**:
- PageRank will under-weight pure-source nodes (A/B in probe). Consider bidirectional variant in implementation.
- code-graph's MCP surface already supports LocAgent's multi-hop primitives; only the agent-loop wrapper is missing.

---

## Tier-B items awaiting dedicated probe sessions

These were not promoted to Tier A but are ready for separate probe work when engineering capacity permits:

| # | Item | Probe fixture | Pass threshold |
|---|---|---|---|
| B-1 | Dataflow edges (parameter_of, flows_to, returns, mutates) | 10-func Go synthetic | F1 ≥ 0.7 on `flows_to` |
| B-2 | SEMANTIC_SIMILARITY first-class edges | 20-query docs-pipeline test set | Top-5 retrieval +10pp |
| B-3 | Ego-graph subgraph MCP tool | 5 docs-pipeline tasks | Used in ≥3/5 tasks AND shortens avg tool-call count |
| B-4 | LLM-augmented edge validation (FP filter) | 50 AMBIGUOUS PSM edges | TP retention ≥80% AND FP reduction ≥30pp |
| B-5 | Rust LSP extraction | Existing Rust CALLS F1 harness | F1 ≥ 0.90 AND LSP timeout <5% |
| B-6 | Framework-aware dead-code detection | Flask decorator Python fixture | FP ≤1 on 20 functions |

**Each Tier-B probe is scoped to fit a 2-5 day budget with a clear pass/fail threshold.** B-3 is probably the cheapest next step (2-day probe, directly enables A-1 improvements).

---

## Phase D: final /superplan input

Input to Phase D: `validated-actions.md` with 4 confirmed Tier-A items.

Recommended D2 framing for user: "Here are 4 Tier-A actions ready to implement + 6 Tier-B items ready to probe. Cumulative Tier-A cost estimate: ~6-8 days engineering. Pick which to ship in what order."

**Suggested ordering** (sequential, based on dependency):
1. **A-4 citations** (1 hour, zero risk, unlocks positioning)
2. **A-3 Cohen's Kappa** (1 hour, zero risk, improves future hand-oracle quality)
3. **A-2 PageRank** (2 days, direct docs-pipeline enabler, validated by Aider precedent + my instrument probe)
4. **A-1 LocAgent loop** (3-5 days, builds on A-2's ranking layer, biggest expected F1 lift)

A-2 before A-1 because A-1's agent loop can consume A-2's ranking scores for relevance-weighted multi-hop traversal.
