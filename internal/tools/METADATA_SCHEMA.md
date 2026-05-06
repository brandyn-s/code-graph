# Response Metadata Schema

Cross-tool standard for surfacing measurement, provenance, and fallback signals
to MCP-tool consumers (LLM agents).

## Why this schema exists

Before this schema, only `trace_call_path` (PR #194) returned a confidence
signal (`confidence_band`). The other ~30 MCP tools returned bare results
with no freshness, parse-error rate, or fallback metadata. An LLM consumer
calling `search_graph` could not distinguish a fresh response from one
served against a stale index; calling `query_security_surfaces` could not
distinguish a rule-based hit from a probabilistic one.

This schema standardizes the four signals an LLM consumer needs to act
appropriately on a result:

- **freshness** — is the underlying data stale relative to the source of
  truth (git HEAD, source files on disk)?
- **provenance** — which model/grammar/extractor produced this result, at
  which version?
- **confidence** — how certain is the underlying computation? (For tools
  with a numeric confidence, this maps to a tier label.)
- **fallback_reason** — when the tool's graceful-fallback path fired (e.g.,
  Sonnet reranker timed out, embedding provider missing key), what was
  the reason?

## Shape

Every tool that opts into this schema appends a `_metadata` key to its
response map:

```json
{
  "<tool-specific top-level fields>": ...,
  "_metadata": {
    "freshness": {
      "state": "current" | "stale" | "unknown",
      "indexed_at": "<ISO 8601>",
      "staleness_seconds": <int> | null
    },
    "provenance": {
      "tool_version": "<semver or commit SHA>",
      "data_source": "<index | live-graph | external-api>",
      "model": "<optional, only when LLM-backed>",
      "grammar_versions": {"<lang>": "<sha>"}  // only when tree-sitter-backed
    },
    "confidence": {
      "band": "high" | "medium" | "low" | "speculative" | "unknown",
      "rationale": "<short string>"
    },
    "fallback_reason": null | "<short reason string>"
  }
}
```

All fields are **optional**. A tool only includes the signals it can produce
authentically. `null`-valued or missing fields signal "not applicable to this
tool" — consumers must not infer meaning from absence.

## Backwards compatibility

This schema is **strictly additive**. Existing fields at the top level of
each tool's response are preserved. A consumer that ignores `_metadata`
sees no behavior change. The leading underscore signals "metadata, not
load-bearing data" by convention.

For tools that already had ad-hoc metadata fields at the top level
(`trace_call_path` has `confidence_band` and `unresolved_call_count` at
top-level, shipped in PR #194), those fields **remain at top level** for
backwards compatibility. The `_metadata` block adds a structured copy in
the schema-compliant location. Top-level fields are NOT deprecated — they
remain part of the contract.

## Per-field semantics

### `freshness.state`

- `current` — index reflects the source-of-truth as of `indexed_at`; no
  known divergence.
- `stale` — known divergence exists. `staleness_seconds` is the duration
  since last index pass.
- `unknown` — the tool can't determine freshness (e.g., serves from a
  derived artifact whose ancestry isn't tracked). Always safe to default
  here.

The "source of truth" is tool-specific. For `search_graph` and most
graph-query tools, it's the indexed graph snapshot. For tools that
recompute on-demand (`code_localize_agent`, `trace_data_flow` with live
edges), it's the data they consumed at compute time.

### `provenance.tool_version`

Either a semver string or a short git SHA. The roundtable identified
"tool drift across sessions" as a class of consumer-visible failure;
surfacing the version helps diagnose "is this the new behavior or the
old one?"

### `provenance.data_source`

- `index` — served from the persistent SQLite store
- `live-graph` — computed against the graph in-memory at request time
- `external-api` — required an external call (Anthropic, Voyage, etc.)

Distinguishing these matters because consumer error semantics differ:
index-served results don't depend on network; external-api results may
have transient failures.

### `provenance.grammar_versions`

Map of language to vendored grammar SHA. Populated only by tools whose
output depends on tree-sitter parsing. Other tools omit this field.
The values come from the per-grammar SHAs tracked in
`internal/cbm/GRAMMARS.md` (created by Phase A2).

### `confidence.band`

Four-tier label, matching `trace_call_path`'s existing `confidence_band`
semantics where applicable:

- `high` — the tool is confident in the result; consumer can act on it
- `medium` — the tool produced this result with known partial coverage
  (e.g., extractor missed dispatch sites but resolved many calls)
- `low` — the tool produced this result with most signal unresolved;
  consumer should combine with grep/manual verification
- `speculative` — the tool returned a placeholder result because the
  underlying computation produced no usable signal
- `unknown` — the tool can't characterize confidence (acceptable when
  the tool is rule-based and the rule fired exactly)

`confidence.rationale` is a short human-readable string explaining why
the band is what it is. For `trace_call_path`, this would be e.g.
`"432 of 480 calls resolved (90%)"`. For tools where rationale isn't
informative, omit the field.

### `fallback_reason`

Non-null when the tool's graceful-fallback path fired AND the consumer
should know about it. Examples:

- `"sonnet_reranker_timeout"` — Sonnet reranker exceeded its 8s window
  and the tool returned hybrid order
- `"voyage_api_key_missing"` — embedding-backed lookup degraded to
  text-only because no Voyage key
- `"index_not_yet_built"` — tool returned partial result because the
  index was still being built

When non-null, the consumer should treat the result with appropriate
caution; the `confidence` band may not have been computed against the
intended pipeline.

## Per-tool implementation guide

A tool implementing this schema follows three steps:

1. Compute its primary result (top-level response fields) as before.
2. Compute the metadata block by combining:
   - **Freshness**: from the project record (`store.GetProject(name)`
     returns `IndexedAt`).
   - **Provenance**: hard-coded for the build (tool_version), plus the
     data_source label that matches the tool's compute path.
   - **Confidence**: tool-specific. For `trace_call_path`, the existing
     `confidence_band` value. For tools with no native confidence
     signal, omit.
   - **Fallback reason**: tool-specific. For tools without a fallback
     path, always `null`.
3. Append `"_metadata": <block>` to the response map.

The helper `tools.NewMetadata(...)` centralizes the construction so each
tool can build the block consistently.

## Reference implementations

Three tools are migrated to this schema in the initial PR:

- `trace_call_path` — generalizes the existing `confidence_band` /
  `unresolved_call_count` fields into the `_metadata` block while
  preserving the top-level fields.
- `search_graph` — adds freshness + provenance metadata. No native
  confidence signal; omits the band.
- `query_security_surfaces` — adds freshness + provenance + per-result
  confidence signals (rule-based hits get `high`, taxonomy-classified
  hits get `medium`).

Other tools may opt in incrementally. The schema is designed for
gradual rollout, not a single bang-bang migration.

## What this schema does NOT cover

- **Cost** (LLM tokens, API spend): out of scope. Tools that incur cost
  may emit cost in their primary response; this schema doesn't
  standardize it.
- **Trace-level provenance**: not captured. A consumer who needs to
  audit "which graph passes contributed to this answer" needs a
  different mechanism (probably graph-level, not response-level).
- **Per-result freshness**: this schema treats the entire response as a
  single unit. If a response combines stale and fresh data (e.g., a
  cached subset plus live-computed delta), the tool reports the
  *worst* freshness signal.

## Future extensions (not in this PR)

- `audit_trail`: which extractor passes contributed (Phase A1 → cross-pass).
- `resource_usage`: latency, memory, cache hit/miss (deferred to
  observability work).
- `quality_caveats`: free-form list of known limitations the tool wants
  to surface for this specific response (e.g., "Go interface dispatch
  not modeled — see GitHub issue #N").

## Cross-references

- `internal/tools/metadata.go` — Go types + constructors
- `internal/tools/trace.go` — first generalization
- `internal/tools/search.go` — second adoption
- `internal/tools/security.go` — third adoption
- Plan: `~/Documents/knowledge-base/plans/2026-05-05-codegraph-and-cross-tool-recommendations.md` (Phase A1)
- Schema reuse: `~/Documents/knowledge-base/plans/2026-05-05-codesearch-recommendations.md` (Plan 2 A1)
