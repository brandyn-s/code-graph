# Community Intelligence — Code-Graph Findings

**Run date**: 2026-04-25
**Skill**: `/gather-intel` (community-first run)
**Tavily credits**: ~25 (Wave 1: 16, Wave 2: 9)
**Waves**: 2 (convergence reached)
**Prior baseline**: `knowledge-base/research/2026-03-23-code-graph-improvements.md` (T1 reference for what's already known)

---

## Phase A: Currency check on prior research

Prior baseline (2026-03-23) covered: optave/codegraph dataflow, Leiden vs Louvain, OPTIONAL MATCH / IS NOT NULL, OTel ingestion, complexity metrics, narsil-mcp type inference, function-signature extraction.

| Prior recommendation | Currency status (2026-04-25) | Notes |
|---|---|---|
| optave/codegraph dataflow extraction (Finding 1) | **CURRENT** — codegraph-rust (Jakedismo) confirms the pattern with explicit `defines/uses/flows_to/returns/mutates` edges. Validated. |
| Confidence-weighted edges + blocklist (Finding 2) | **CURRENT** — already shipped (PR #30); no new contradicting evidence |
| Leiden over Louvain (Finding 3) | **EVOLVED** — Microsoft GraphRAG production uses Leiden by default. Increased weight; still not implemented in code-graph |
| Function signature extraction (Finding 4) | **CURRENT** — synapps and codegraph-rust both populate parameters via LSP, not tree-sitter alone. Slight pivot: LSP-based may be cheaper than per-language tree-sitter queries |
| 3-tier incremental strategy (Finding 5) | **CURRENT** — no new pattern emerged |
| OPTIONAL MATCH / IS NOT NULL (Finding 6) | **CURRENT** — still a gap |
| OTel trace ingestion (Finding 7) | **CURRENT** — still underutilized |
| Complexity metrics (Finding 8) | **CURRENT** — still gap |
| narsil-mcp type inference (Finding 9) | **STALE-ish** — codegraph-rust now has full LSP integration covering 6 languages with type info; cheaper than implementing inference in Go |

---

## Phase B: New findings from Wave 1+2

### Finding I-1: Three direct production competitors discovered

Three new code-graph implementations active in 2025-2026, each with patterns we should evaluate:

**A. codegraph-rust (Jakedismo)** — Rust + surrealDB + LSP per language
- **Indexing tiers** (fast / balanced / full): `fast` skips LSP+dataflow+architecture; `balanced` adds LSP symbols + module linking; `full` adds dataflow + architecture analyzers. Tier-based config controls speed/richness tradeoff.
- **LSP per language**: rust-analyzer, typescript-language-server, pyright-langserver, gopls, jdtls, clangd. Indexing fails fast if LSP missing.
- **Edge types we don't have**: `defines`, `uses`, `flows_to`, `returns`, `mutates` (intraprocedural dataflow), `Module → Module` import/containment.
- **Configurable LSP timeout**: `CODEGRAPH_LSP_REQUEST_TIMEOUT_SECS` (default 600s, min 5s).
- **Backend**: surrealDB (multi-model, NOT SQLite). Different storage choice.

**B. stakwork/stakgraph** — Go + tree-sitter + LSP + neo4j
- **Edge types**: `Request` (HTTP client to external), `DataModel` (structs/interfaces/types/enums/schemas), `Class`, `Trait`, `Test` (classified into unit/integration/E2E), `Import`.
- **Active development**: commits as recent as Apr 23-24 2026 (sub-agent session analytics, typescript+react test updates).
- **Backend**: neo4j (NOT SQLite).

**C. SynappsCodeComprehension/synapps** — Python + LSP + Memgraph (1 star, 2 contributors)
- **Architecture**: tree-sitter finds call sites, LSP resolves what each points to. "Not string matches, but semantically resolved references" — explicit value-prop on call resolution accuracy via LSP.
- **Tools**: `get_architecture` (packages, hotspot methods, HTTP service map), `find_dead_code` (experimental, "false positives filtered out").
- **Backend**: Memgraph.

**Evidence quality** (filter rule R1): T2-T4 (open-source repos with feature documentation, no published F1 numbers).
**Compare-by-need status** (filter rule R5): all three reinforce that "tree-sitter + LSP" is the production architecture; we already have it for Go and the pattern extends to other languages.

### Finding I-2: Upstream paper exists — arXiv 2603.27277

**"Tree-Sitter-Based Knowledge Graphs for LLM Code Exploration"** — published paper about Codebase-Memory v0.5.5 (the upstream we forked). Documents architecture, evaluation methodology, related work catalog (LocAgent, GraphCodeAgent, RANGER, Prometheus, SemanticForge, Code Graph Model, Repository Intelligence Graph).

**Implication**: this is the **authoritative literature anchor** for our work. Should be referenced in our own architecture docs. Saves us the work of writing one. Read in C2 dedup.

**Evidence quality**: T1 — academic paper about the exact upstream we forked.

### Finding I-3: LLM post-filter for SAST FP reduction is production-validated

**arXiv 2601.22952 ("Sifting the Noise")** — tested CodeQL/Semgrep/SonarQube/Joern + LLM agent post-filter (Aider, OpenHands, SWE-agent × Claude Sonnet 4 / DeepSeek / GPT-5) on OWASP Benchmark v1.2.

| Metric | Best config result |
|---|---|
| OWASP baseline FP rate | >92% |
| Best post-filter FP rate | **6.3%** (Claude Sonnet 4 + SWE-agent) |
| TP retention | **77.7%** (miss rate 22.25%) |
| Completion rate on 1,415 vulnerable cases | 99.7% |

**Production analog**: Semgrep Assistant AI Triage (2026 release) ships with LLM-based filtering claiming "nearly 60% of SAST noise" filtered.

**Implication for code-graph**: this is direct evidence for **R8 LLM asymmetric bar**. The 92→6.3% number is real, but TP retention of 77.7% means **22.25% of true findings are silently dropped**. Validates the filter rule R8 of "auto-apply only if accuracy ≥ threshold; otherwise propose-not-decide."

**For our use case**: not directly applicable (we're not doing SAST FP filtering), but the pattern transfers — LLM-augmented edge validation has the same asymmetric profile.

**Evidence quality**: T1-T2 — peer-reviewed paper + production deployment confirmation.

### Finding I-4: aider repo-map is the established reference for tree-sitter + PageRank repo summarization

Aider (43k stars) uses tree-sitter tags + PageRank ranking + token-budget-bounded repo map as its agent-context primitive. Confirmed by:
- Aider's own docs (`aider.chat/2023/10/22/repomap.html` and `/docs/repomap.html`)
- HN discussions in 2026 noting "harnesses leaving low-hanging fruit on the table" — the pattern is widely admired but not adopted broadly
- MotleyCoder explicitly built on the same model with weight diffusion instead of PageRank
- arXiv 2603.27277 references aider as foundational prior work

**Status vs code-graph**: code-graph already has community detection (Louvain), but **does not have query-relevance-weighted ranking** for surfacing context to agents. This is a missing layer between our graph and the docs pipeline.

**Evidence quality**: T1 (large adoption + documented architecture) but no published accuracy numbers vs alternatives.

### Finding I-5: Cursor codebase indexing real numbers (production)

**Cursor 2.0** (Feb 2026): proprietary "Composer" model, 4x faster than peers, 8 parallel agents per task. Background agents on isolated VMs with video/log/screenshot recording.

**Cursor indexing benchmarks** (vsavkin/large-monorepo, 79K files):
| Scope | Files | Index time | Prompt response |
|---|---|---|---|
| Full codebase | ~79,600 | ~38 min | ~55s |
| Single module | ~15,000 | ~6.5 min | ~30s |

**Cursor explicit cap**: auto-index disabled at ~50K files. Practitioners use `.cursorignore` for selective indexing.

**Augment Code claim**: "Cursor doesn't build semantic dependency graphs across services" — used to justify Augment's traceability advantage. Marketing claim, but suggests cross-service is genuinely Cursor's gap.

**Implication for code-graph**: our PSM index (~80K nodes, 212K edges, 6 min) is in Cursor's neighborhood. We have cross-service via Service↔Topic↔Function. **We are not behind**.

**Evidence quality**: T2 (vendor-published numbers + independent benchmark).

### Finding I-6: Adversarial — Rust proc-macros and Python decorators remain unsolved

Multiple sources confirm proc-macros + decorator-based dispatch are still hard limits:
- HN: dependency analysis can't see code paths chosen by dynamic args
- Cargo Book: `-Zbuild-analysis` exists for build timings but not for macro expansion graphs
- arXiv ccs25-PanicKiller: Rust runtime safety checks defeat fuzzers (180-210 false-positive crashes per true bug); separate problem but illustrates static-analysis blind spots
- Practitioners use `cargo expand` for human inspection but no production graph tool ingests it

**No new technique surfaced**. Status quo: tree-sitter sees the macro invocation node, not the expansion. Same for Python decorators.

**Implication for code-graph**: confirmed our AMBIGUOUS confidence tier is the right approach for these. **Don't try to solve macro expansion**; mark macro-derived call sites as AMBIGUOUS and let the docs pipeline handle uncertainty.

**Evidence quality**: T2 (HN + academic papers); represents practitioner consensus.

### Finding I-7: GraphRAG / LazyGraphRAG patterns — applicable to docs-pipeline use case but not graph extraction

GraphRAG (Microsoft, open-sourced July 2024; LazyGraphRAG announced for 10-90% indexing cost reduction) is the reference architecture for graph + LLM. **Not directly applicable** for code-graph extraction itself, but **directly applicable** for the docs pipeline that consumes code-graph output.

**Pattern**: graph traversal → community summary (LLM) → query-time retrieval against summaries instead of raw edges.

**Implication**: when the docs pipeline is built, plan to use GraphRAG patterns over code-graph output. Not a code-graph change.

**Evidence quality**: T1 (Microsoft Research production system).

### Finding I-8: codebase-to-AI tool comparison surfaced direct competitor named "Codebase Memory MCP"

DEV.to comparison of 4 codebase-to-AI tools (FastAPI 108K-line benchmark) lists:
- Repomix (23K stars)
- Aider repo-map
- **"Codebase Memory MCP (1.4k stars)"** — described as "builds a SQLite knowledge graph from tree-sitter ASTs. 66 languages. Queryable through 14 MCP tools."
- Stacklit (new, blog author's project)

**This is OUR upstream**. The published assessment positions us as "the power user approach" for "large codebases where you need deep semantic queries." External validation of our positioning.

**Evidence quality**: T3 (third-party blog assessment with token-cost benchmarks).

---

## Community Threads (3+ converging sources)

### Thread A: tree-sitter + LSP is the production reference architecture
Sources: codegraph-rust, stakgraph, synapps, MotleyCoder, arXiv 2603.27277.
Convergence: 5+ independent implementations. Strong consensus.

### Thread B: LLM as accuracy-improver for static analysis (filter, validate, enrich) — but with measured TP-loss
Sources: Sifting the Noise (arXiv 2601.22952), Semgrep Assistant AI Triage (production), ZeroPath (commercial), Snyk Code (commercial).
Convergence: 4+ sources. Strong consensus that LLM filter helps; measured TP loss is real.

### Thread C: Indexing tiers (fast/balanced/full) as a UX pattern
Sources: codegraph-rust (explicit fast/balanced/full), Cursor (`.cursorignore` selective indexing), Augment Code (architectural focus modes).
Convergence: 3 sources. Pattern is real and absent from code-graph.

### Thread D: Aider PageRank repo-map remains the leading "graph-to-agent-context" pattern
Sources: aider docs, MotleyCoder blog, HN 2026 thread, arXiv 2603.27277.
Convergence: 4+ sources. Code-graph lacks the query-relevance ranking layer.

---

## Adversarial / Counter-evidence

| Finding | Counter-evidence | Status |
|---|---|---|
| I-3 (LLM post-filter excellent) | TP retention 77.7% = 22.25% miss rate; SonarQube/Veracode practitioners report LLM triage still requires human review | **CONTESTED-but-mitigable**: the pattern works only for "propose, human/pipeline decides" framing |
| Leiden over Louvain | Implementation cost in Go is significant; refine workaround already handles worst cases | Confirmed with caveats (per prior research) |
| Adopt LSP-per-language broadly | Fail-fast-on-missing-LSP UX is brittle; long timeouts (600s default in codegraph-rust) suggest LSP can hang | **CONTESTED**: high-value but requires careful timeout/fallback engineering |

---

## Pre-classification for filter pass (per filter-pass-decision-rules.md)

These are tentative classifications; the formal C2 filter pass will apply rules R1-R8 and tier rubric.

| # | Finding | Likely tier | Filter rule that fires |
|---|---|---|---|
| I-1A | codegraph-rust LSP per language + indexing tiers | **B (probe)** | R1 fits gap (Python F1 0.54, Go F1 0.45 floors); R2 fits 5-day budget for 1 language; R5 cleared |
| I-1A.2 | codegraph-rust dataflow edges (`flows_to`, `returns`, `mutates`) | **B (probe)** | Same as 2026-03-23 Finding 1; R7 needs probe definition |
| I-1A.3 | Indexing tiers (fast/balanced/full) | **C (awareness)** | R5 gate 3 fail — no concrete user friction with current single-tier; defer until usage signals demand it |
| I-2 | arXiv 2603.27277 reference paper | **A (validation, not implement)** | Reference our own upstream paper in code-graph docs. Zero implementation cost. |
| I-3 | LLM post-filter pattern for edge validation | **B (probe)** | R7 + R8: requires probe definition (which edges to validate, which model, what threshold). 22% TP miss rate flags asymmetric concern |
| I-4 | Aider PageRank repo-map ranking layer | **A (implement)** | R1 cleared (Aider documented); R2 fits 2-day probe (PageRank over existing edges); R5 cleared (gap is real — docs pipeline needs ranked context); R6 not triggered |
| I-5 | Cursor benchmark validates code-graph is competitive | **A (validation)** | Validates our positioning. No implementation. Cite in docs. |
| I-6 | Macros/decorators remain unsolved | **A (validation)** | Validates our AMBIGUOUS confidence tier strategy |
| I-7 | GraphRAG patterns for docs-pipeline | **C (awareness)** | Not a code-graph change. Belongs in docs-pipeline planning. |
| I-8 | External "Codebase Memory MCP" assessment | **A (validation)** | Cite in docs/positioning |

**Tentative shape**: 4 Tier-A (3 validation, 1 implement), 3 Tier-B (probe-gated), 3 Tier-C (awareness). Tier-A "implement" item is the **PageRank repo-map ranking layer** — the only net-new buildable pattern that meets all filter rules.

---

## Sources (unique URLs)

| # | URL | Tier | Key contribution |
|---|---|---|---|
| 1 | https://github.com/Jakedismo/codegraph-rust | T4 | Indexing tiers, LSP per language, dataflow edges |
| 2 | https://github.com/SynappsCodeComprehension/synapps | T4 | Tree-sitter + LSP for semantic call resolution |
| 3 | https://github.com/stakwork/stakgraph | T4 | Tree-sitter + LSP + neo4j with rich edge taxonomy (Request, DataModel, Trait, Test) |
| 4 | https://arxiv.org/pdf/2603.27277 | T1 | **Authoritative paper on Codebase-Memory v0.5.5** (our upstream) |
| 5 | https://arxiv.org/abs/2601.22952 | T1 | Sifting the Noise: LLM SAST FP filtering benchmarks |
| 6 | https://konvu.com/compare/semgrep-vs-codeql | T3 | OWASP F1 numbers; LLM filter 92%→6.3% summary |
| 7 | https://aider.chat/docs/repomap.html | T1 | Aider repo-map architecture |
| 8 | https://aider.chat/2023/10/22/repomap.html | T1 | Aider PageRank ranking architecture |
| 9 | https://medium.com/motleycrew-ai/building-and-using-a-code-graph-in-motleycoder-e24a599f0970 | T4 | MotleyCoder weight-diffusion alternative to PageRank |
| 10 | https://dev.to/thegdsks/i-tested-4-codebase-to-ai-tools-on-fastapi-108k-lines-here-are-the-token-costs-4bmc | T3 | Codebase-to-AI tool comparison; positions Codebase Memory MCP |
| 11 | https://bitpeak.com/resources/ | T4 | Cursor indexing benchmark numbers (79K files) |
| 12 | https://news.ycombinator.com/item?id=46988596 | T2 | HN discussion: tree-sitter + harnesses, aider pattern |
| 13 | https://www.kloia.com/blog/knowledge-base-vs-knowledge-graph-llm | T3 | GraphRAG/LazyGraphRAG production reference |
| 14 | https://getsecureslate.com/blog/the-7-best-sast-solutions-for-2026 | T3 | Semgrep AI Triage 60% noise filter (production) |
| 15 | https://www.augmentcode.com/tools/8-top-ai-coding-assistants-and-their-best-use-cases | T4 | Augment claim: Cursor lacks semantic dependency graphs |
| 16 | https://news.ycombinator.com/item?id=43935067 | T2 | HN: static analysis cannot see dynamic dispatch |
| 17 | https://www.cse.cuhk.edu.hk/~wei/papers/ccs25-PanicKiller.pdf | T1 | Rust runtime safety checks defeat fuzzers |

---

## Recommendation summary

The intel run validates code-graph's current architecture (tree-sitter + LSP + SQLite WAL is the production pattern), **affirms the prior 2026-03-23 dataflow-edge recommendation**, and surfaces **one new clear Tier-A buildable**: a query-relevance ranking layer (Aider-style PageRank) over our existing graph to feed the docs pipeline.

The LLM-augmented edge validation is real but Tier-B-with-probe — the 22% TP miss rate is a genuine constraint that argues for "propose, validate" framing per our R8 rule.

Three competitor projects (codegraph-rust, stakgraph, synapps) confirm we're in a real space with documented patterns; none of them are obviously ahead of code-graph on the dimensions we care about (cross-language Nix/systemd extraction is unique).

**Ready for**: Phase B2 (gather-research) to cross-reference these findings against academic frontier; then Phase C filter pass.
