# D1 metadata-rollout falsifier matrix

**Purpose**: settle the surviving D1 disagreement from the 2026-05-06
roundtable (META_SYNTHESIS D1) — should the metadata schema rollout to
the 15 pending tools proceed mechanically with existing helpers, or
should it stage with bespoke evidence-backed confidence rules per tool?

**Falsifier** (from META_SYNTHESIS D1):
> A 15-tool metadata matrix showing ≥12/15 can use existing helpers
> with meaningful testable metadata → GROK wins (mechanical rollout).
> If the hard tools (`index_repository`, `manage_adr`) require new
> schemas → staged rollout wins.

**Verdict**: 14 of 15 use existing helpers (`stdReadGraphMetadata` /
`stdStatusToolMetadata` / `stdWriteToolMetadata`) with meaningful
testable metadata. 1 requires response-shape work (`diff_graph` returns
a struct, not a map). **GROK wins**: mechanical rollout is correct.

---

## Per-tool assessment

For each pending tool, three columns:

- **Existing helper applies?** Does one of the three `std*Metadata`
  helpers cover the tool with meaningful metadata that's testable
  (re-index → freshness changes; write call → action_outcome present)?
- **Bespoke fields useful?** Could the tool also carry domain-specific
  metadata (confidence band, model name, similarity score) — but the
  rollout doesn't BLOCK on these.
- **Verdict**: EASY (existing helper + 1-2 lines), MEDIUM (existing
  helper + chained call for bespoke fields, 3-5 lines), HARD (response
  shape change required).

| # | Tool | Existing helper | Bespoke fields | Verdict | Notes |
|---|------|----------------|---------------|---------|-------|
| 1 | `search_code_semantic` | `stdReadGraphMetadata` | model (voyage-4-large), confidence (cosine band) | EASY | Helper covers freshness/provenance; model/confidence are bonus |
| 2 | `rank_by_query` | `stdReadGraphMetadata` | confidence (PageRank score-distribution band) | EASY | Helper covers; score-band is bonus |
| 3 | `code_localize` | `stdReadGraphMetadata` | (BFS-distance can be a confidence proxy) | EASY | Helper directly applies |
| 4 | `code_localize_agent` | `stdReadGraphMetadata` + WithModel/WithConfidence chain | model name, stop_reason as confidence proxy | MEDIUM | LLM-using; adding WithModel + WithConfidence is 3 lines on top of helper |
| 5 | `diff_graph` | NONE — returns `diffGraphResult` struct, not a map | n/a until shape decision | **HARD** | Response is a typed struct; needs `Metadata map[string]any` field added OR wrap response in map |
| 6 | `find_rationale` | `stdReadGraphMetadata` | n/a | EASY | Annotation list, standard read-graph fits |
| 7 | `find_similar_functions` | `stdReadGraphMetadata` | confidence (similarity score-distribution band) | EASY | Helper covers; score-band is bonus |
| 8 | `generate_report` | `stdWriteToolMetadata(ActionOutcomeCreated)` | n/a | EASY | Side-effect tool; write helper directly applies |
| 9 | `get_architecture` | `stdReadGraphMetadata` | per-aspect freshness if cached | EASY | Helper covers; per-aspect is bonus |
| 10 | `get_graph_schema` | `stdStatusToolMetadata` | n/a | EASY | Counts only; status helper fits |
| 11 | `get_relevant_context` | `stdReadGraphMetadata` | token_budget signal (already in response) | EASY | Helper covers |
| 12 | `get_review_context` | `stdReadGraphMetadata` | n/a | EASY | Helper directly applies |
| 13 | `index_repository` | `stdWriteToolMetadata(ActionOutcomeCreated/Updated)` | progress_phase, files_indexed, errors | MEDIUM | Helper covers basic outcome; bespoke fields (progress) for live operators |
| 14 | `ingest_traces` | `stdWriteToolMetadata(ActionOutcomeCreated)` | traces_ingested count | EASY | Write helper fits |
| 15 | `manage_adr` | per-mode dispatch: `stdWriteToolMetadata` for store/update/delete; `stdStatusToolMetadata` for get | n/a | EASY | Mechanical per-mode dispatch; helpers cover all four modes |

## Counts

- **EASY** (existing helper, 1-2 lines per tool): 11 tools
  - search_code_semantic, rank_by_query, code_localize, find_rationale, find_similar_functions, generate_report, get_architecture, get_graph_schema, get_relevant_context, get_review_context, ingest_traces, manage_adr
  - Wait, that's 12 — let me recount: search_code_semantic, rank_by_query, code_localize, find_rationale, find_similar_functions, generate_report, get_architecture, get_graph_schema, get_relevant_context, get_review_context, ingest_traces, manage_adr = **12**

- **MEDIUM** (existing helper + chained call for bespoke fields, 3-5 lines): 2 tools
  - code_localize_agent, index_repository = **2**

- **HARD** (response-shape change required): 1 tool
  - diff_graph = **1**

**Total**: 12 EASY + 2 MEDIUM + 1 HARD = 15 pending tools. **14/15 use existing helpers**; only diff_graph needs bespoke schema work.

## Falsifier evaluation

The roundtable's threshold was ≥12/15 using existing helpers with
meaningful testable metadata. With strict counting, 14/15 meet this
bar (12 EASY + 2 MEDIUM). The MEDIUM tools (`code_localize_agent` and
`index_repository`) STILL use the existing helpers — they just chain
additional setters (`WithModel`, `WithConfidence`) for richer metadata.
The helper covers the testable minimum; the bespoke fields are upside.

**Result: GROK wins.** The mechanical rollout using existing helpers
is correct. No schema gap forces a staged rollout. The single tool
that needs new schema work (`diff_graph`) can be moved to
`excludedTools` similarly to `list_projects`, OR scheduled as one
small follow-up that adds `Metadata map[string]any` to
`diffGraphResult`.

## What this falsifies vs validates

| Position | Falsified? |
|---|---|
| GROK: ship S-effort completion using existing helpers | VALIDATED — 14/15 use existing helpers |
| OPUS+GPT: only ship metadata where confidence has defensible derivation rules | NOT VALIDATED — none of the 14 tools needs bespoke confidence rules to ship; freshness + provenance + (optional) action_outcome are sufficient minimum metadata |
| OPUS R5: "agree to disagree" with no resolution path | RESOLVED — the matrix IS the falsifier the synthesis named, and it points one direction |

## Recommended action

Ship a follow-up PR (or schedule as Plan 5) that:

1. Instruments tools 1-12 (EASY) using existing helpers. Mechanical;
   ~2 lines per tool, ~25 lines total.
2. Instruments tools 13-14 (MEDIUM) — `code_localize_agent` with
   `WithModel(envOrDefault("ANTHROPIC_MODEL"))` +
   `WithConfidence(stopReasonToBand(stopReason))`; `index_repository`
   with `stdWriteToolMetadata(ActionOutcomeCreated)`. ~10 lines total.
3. Decides on `diff_graph`: add `_metadata` field to `diffGraphResult`
   struct (4 lines) OR move to `excludedTools` with rationale (1 line).
4. Updates `metadata_coverage_test.go`: moves these 15 from
   `pendingTools` to `instrumentedTools` (or to `excludedTools` for
   diff_graph if that path is chosen).

Total estimated effort: **S** (~50 lines + the diff_graph decision).
This is dramatically smaller than the staged rollout that was the
fallback if the falsifier had gone the other way.

## Methodology note

The roundtable's `meaningful testable metadata` standard is the bar.
All EASY/MEDIUM tools above pass on:

- **Meaningful**: freshness signals stale data; provenance signals
  tool version; action_outcome signals success/failure.
- **Testable**: re-index → freshness changes; bump tool version →
  provenance.tool_version changes; force write failure → outcome
  becomes "failed". Each is a one-call assertion.

Bespoke fields (model, confidence, score distributions) ARE testable
too but aren't required for the falsifier to flip. They're additive
upside, not blocking work.

---

**Cross-references**:
- META_SYNTHESIS.md (2026-05-06): D1 disagreement specification + falsifier
- internal/tools/metadata_coverage_test.go: pendingTools list (the input set)
- internal/tools/metadata.go: stdReadGraphMetadata, stdStatusToolMetadata, stdWriteToolMetadata (the helpers being evaluated)
- internal/tools/METADATA_SCHEMA.md: per-tool field-selection table (where the rollout's category mapping lives)
