package tools

// Response metadata schema — see METADATA_SCHEMA.md for the full design.
//
// Every tool that opts in appends a "_metadata" key to its response map.
// This file provides:
//   - Type definitions for the metadata block (used internally, not
//     exposed at the wire format — the wire format is JSON via map[string]any).
//   - Constructor helpers that produce the correctly-shaped map for
//     embedding in tool responses.
//
// The wire format is a `map[string]any` (matching how every tool already
// builds its response) so consumers see a JSON object with exactly the
// fields documented in METADATA_SCHEMA.md.

import (
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// MetadataBuilder accumulates metadata fields for a single tool response.
// Use NewMetadataBuilder() to start; chain field setters; call Build()
// to produce the map for embedding.
//
// Zero-valued fields are omitted from the output map so consumers don't
// see misleading "null" when the tool simply didn't compute that signal.
type MetadataBuilder struct {
	freshnessState      string
	indexedAt           string
	stalenessSeconds    int64
	hasStaleness        bool
	toolVersion         string
	dataSource          string
	model               string
	grammarVersions     map[string]string
	confidenceBand      string
	confidenceRationale string
	fallbackReason      string
	actionOutcome       string
}

// NewMetadataBuilder returns a fresh builder. All fields default to
// zero (omitted from output).
func NewMetadataBuilder() *MetadataBuilder {
	return &MetadataBuilder{}
}

// WithFreshness records when the underlying data was indexed. The state
// label is one of "current", "stale", or "unknown" (see schema).
//
// If indexedAt is empty (project not found, or tool doesn't track index
// time), the freshness block is omitted from the output.
func (b *MetadataBuilder) WithFreshness(state, indexedAt string) *MetadataBuilder {
	b.freshnessState = state
	b.indexedAt = indexedAt
	return b
}

// WithStaleness records the staleness in seconds explicitly. Use this
// when the tool has a more precise signal than just indexed_at (e.g.,
// computed against an mtime-based check).
func (b *MetadataBuilder) WithStaleness(seconds int64) *MetadataBuilder {
	b.stalenessSeconds = seconds
	b.hasStaleness = true
	return b
}

// WithProvenance records the tool version and data source. tool_version
// is typically the build's git SHA or semver; data_source is one of
// "index", "live-graph", or "external-api".
func (b *MetadataBuilder) WithProvenance(toolVersion, dataSource string) *MetadataBuilder {
	b.toolVersion = toolVersion
	b.dataSource = dataSource
	return b
}

// WithModel records the LLM model used by the tool, if any. Omit for
// non-LLM tools.
func (b *MetadataBuilder) WithModel(model string) *MetadataBuilder {
	b.model = model
	return b
}

// WithGrammarVersions records the tree-sitter grammar SHAs that
// contributed to this result. Omit for tools that don't depend on
// tree-sitter parsing.
func (b *MetadataBuilder) WithGrammarVersions(versions map[string]string) *MetadataBuilder {
	if len(versions) == 0 {
		return b
	}
	b.grammarVersions = make(map[string]string, len(versions))
	for k, v := range versions {
		b.grammarVersions[k] = v
	}
	return b
}

// WithConfidence records the confidence band ("high"/"medium"/"low"/
// "speculative"/"unknown") and an optional rationale string.
func (b *MetadataBuilder) WithConfidence(band, rationale string) *MetadataBuilder {
	b.confidenceBand = band
	b.confidenceRationale = rationale
	return b
}

// WithFallback records the reason a graceful-fallback path fired.
// Pass empty string to omit (no fallback occurred).
func (b *MetadataBuilder) WithFallback(reason string) *MetadataBuilder {
	b.fallbackReason = reason
	return b
}

// Action outcome values for write-tool responses.
const (
	ActionOutcomeCreated = "created"
	ActionOutcomeUpdated = "updated"
	ActionOutcomeDeleted = "deleted"
	ActionOutcomeNoOp    = "no_op"
	ActionOutcomeFailed  = "failed"
)

// WithActionOutcome records the outcome of a write-tool invocation.
// Use one of the ActionOutcome* constants. Empty string omits the field.
//
// Plan 3 Phase C addition for write-tool metadata coverage
// (delete_project, index_repository, manage_adr write modes, ingest_traces).
func (b *MetadataBuilder) WithActionOutcome(outcome string) *MetadataBuilder {
	b.actionOutcome = outcome
	return b
}

// Build produces the metadata map for embedding under "_metadata" in
// a tool's response. Only non-zero fields are included; the consumer
// sees exactly the signals the tool authentically produced.
func (b *MetadataBuilder) Build() map[string]any {
	out := map[string]any{}

	if b.freshnessState != "" || b.indexedAt != "" || b.hasStaleness {
		freshness := map[string]any{}
		if b.freshnessState != "" {
			freshness["state"] = b.freshnessState
		}
		if b.indexedAt != "" {
			freshness["indexed_at"] = b.indexedAt
		}
		if b.hasStaleness {
			freshness["staleness_seconds"] = b.stalenessSeconds
		}
		out["freshness"] = freshness
	}

	if b.toolVersion != "" || b.dataSource != "" || b.model != "" || len(b.grammarVersions) > 0 {
		provenance := map[string]any{}
		if b.toolVersion != "" {
			provenance["tool_version"] = b.toolVersion
		}
		if b.dataSource != "" {
			provenance["data_source"] = b.dataSource
		}
		if b.model != "" {
			provenance["model"] = b.model
		}
		if len(b.grammarVersions) > 0 {
			provenance["grammar_versions"] = b.grammarVersions
		}
		out["provenance"] = provenance
	}

	if b.confidenceBand != "" || b.confidenceRationale != "" {
		conf := map[string]any{}
		if b.confidenceBand != "" {
			conf["band"] = b.confidenceBand
		}
		if b.confidenceRationale != "" {
			conf["rationale"] = b.confidenceRationale
		}
		out["confidence"] = conf
	}

	if b.fallbackReason != "" {
		out["fallback_reason"] = b.fallbackReason
	}

	if b.actionOutcome != "" {
		out["action_outcome"] = b.actionOutcome
	}

	return out
}

// FreshnessFromProject reads the project's IndexedAt and returns the
// (state, indexedAt) pair suitable for WithFreshness. Returns
// ("unknown", "") if the project is nil.
//
// "current" is reported when the project record exists; "stale" is not
// distinguished here (would require comparing IndexedAt against the
// source-tree mtime, which the tool layer doesn't have access to from
// this call site). Tools that need finer-grained staleness should
// compute it themselves and call WithStaleness directly.
func FreshnessFromProject(p *store.Project) (string, string) {
	if p == nil {
		return "unknown", ""
	}
	return "current", p.IndexedAt
}

// stdReadGraphMetadata is the standard metadata block for read-graph tools
// (search_*, query_*, get_*, find_*, detect_*, trace_*, explain_*, etc.).
// It records freshness from the project's IndexedAt + provenance pointing
// at the graph DB.
//
// Plan 3 Phase C: introduced to reduce per-tool boilerplate from ~6 lines
// to 1. Tools that need additional fields (confidence band, model, action
// outcome) should call NewMetadataBuilder directly rather than this helper.
func (s *Server) stdReadGraphMetadata(projName string) map[string]any {
	indexedAt := ""
	if st, err := s.resolveStore(projName); err == nil && st != nil {
		if proj, _ := st.GetProject(projName); proj != nil {
			indexedAt = proj.IndexedAt
		}
	}
	return NewMetadataBuilder().
		WithFreshness(freshnessStateFromIndexedAt(indexedAt), indexedAt).
		WithProvenance("", "graph_db").
		Build()
}

// stdWriteToolMetadata is the standard metadata block for write-tool
// responses (delete_project, index_repository, manage_adr write modes,
// ingest_traces). Records provenance + action outcome.
//
// outcome should be one of the ActionOutcome* constants.
func (s *Server) stdWriteToolMetadata(outcome string) map[string]any {
	return NewMetadataBuilder().
		WithProvenance("", "graph_db").
		WithActionOutcome(outcome).
		Build()
}

// stdStatusToolMetadata is the standard metadata block for pure-status
// tools (index_status, list_projects, get_graph_schema). Records
// provenance only — these don't have a single "project" with freshness.
func (s *Server) stdStatusToolMetadata() map[string]any {
	return NewMetadataBuilder().
		WithProvenance("", "graph_db").
		Build()
}
