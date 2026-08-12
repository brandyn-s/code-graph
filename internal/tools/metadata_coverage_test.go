package tools

// Plan 3 Phase C: metadata schema coverage map.
//
// This test pins the set of MCP tools that emit a `_metadata` block in
// their response. It serves three purposes:
//
//  1. Regression detection: a future PR that accidentally drops the
//     `_metadata` field from an instrumented tool will fail this test.
//  2. Documentation: the test itself enumerates which tools currently
//     opt into the schema and which are pending instrumentation.
//  3. Allowlist for non-applicable tools: tools whose response shape
//     doesn't fit a map-with-_metadata pattern (e.g., list_projects
//     returns a bare array) are documented here as "skip with reason"
//     rather than failed.
//
// Updating this list is a deliberate act: when you instrument a new
// tool, move it from `pendingTools` to `instrumentedTools`. When you
// remove a tool, remove it from both lists.

// instrumentedTools lists MCP tools that are expected to include a
// `_metadata` key in their response. As of Plan 3 Phase C (2026-05-06),
// these tools have been wired up.
//
// This is an enumeration, not a runtime assertion harness — running the
// tools requires an indexed project, which is environment-dependent.
// The test below verifies the list is non-empty (sanity) and that no
// tool appears in both lists (coherence).
var instrumentedTools = []string{
	// Plan 1 A1 reference implementations:
	"trace_call_path",
	"search_graph",
	"query_security_surfaces",
	"index_health",

	// Plan 3 Phase C — read-graph tools:
	"search_code",
	"query_graph",
	"trace_data_flow",
	"get_code_snippet",
	"get_affected_tests",
	"get_change_coupling",
	"detect_cycles",
	"detect_changes",
	"diff_services",
	"service_map",
	"visualize",
	"explain_symbol",
	"explain_service",
	"query_stig_evidence",

	// Plan 3 Phase C — status + write tools:
	"index_status",
	"localize_across_projects", // Read-only project-balanced discovery across isolated indexes
	"delete_project",

	// Plan 5 Phase B — D1 matrix mechanical rollout:
	"search_code_semantic",    // Voyage embeddings; freshness + provenance + model
	"rank_by_query",           // Read-graph; std helper
	"code_localize",           // Read-graph; std helper
	"code_localize_agent",     // LLM-using; freshness + model + stop_reason->confidence band
	"compare_project_indexes", // Immutable read-only file and declaration snapshot delta
	"diff_graph",              // Read-graph; struct field added (HARD per matrix, mechanical here)
	"find_rationale",          // Read-graph; std helper
	"find_similar_functions",  // Read-graph; std helper
	"generate_report",         // Write tool; std helper, action_outcome=created
	"get_architecture",        // Read-graph; std helper on response map
	"get_graph_schema",        // Status tool; std helper
	"get_relevant_context",    // Read-graph; std helper
	"index_repository",        // Write tool; outcome=created (first index) or updated (re-index)
	"ingest_traces",           // Write tool; outcome=updated (edges enriched)
	"manage_adr",              // Multi-mode: get->status, store/update/delete->write helper

	// Wave 2 relationship provenance:
	"get_relationship_evidence", // Resolver + runtime-confirmed edge evidence
}

// pendingTools lists MCP tools that should ultimately emit `_metadata`
// but haven't been wired yet. Each entry includes a short rationale for
// why instrumentation was deferred.
//
// Plan 5 Phase B (2026-05-06): the 14 tools listed by D1_METADATA_MATRIX.md
// have been instrumented. The list is now empty.
//
// Adding a new tool to this list (rather than instrumenting it
// immediately) is acceptable — the schema is opt-in by design — but the
// rationale should be honest. "Forgot" is not an acceptable reason.
var pendingTools = map[string]string{}

// excludedTools lists MCP tools whose response shape is intentionally
// not a map-with-_metadata. Adding to this list requires justifying
// the architectural reason — the schema is opt-in, not mandatory, but
// "this tool returns a bare array" should be a deliberate choice.
var excludedTools = map[string]string{
	"list_projects":      "Returns a bare slice of project info structs; wrapping would break clients. Metadata is implicit (this is a status snapshot of all projects).",
	"get_review_context": "Returns plain markdown text (~200-500 token summary), not JSON. Metadata block doesn't fit the contract — embedding it as markdown noise would clutter the LLM-consumed output. Plan 5 Phase B: re-evaluated and excluded after D1 matrix-driven rollout missed the markdown-vs-JSON distinction.",
}

// allMCPToolNames is the union of registered MCP tool names. Sourced
// from a one-time enumeration of `s.addTool(&mcp.Tool{Name: "..."}` in
// internal/tools/. If new tools are added to the codebase, this list
// must be extended (and the new tool must appear in exactly one of:
// instrumented / pending / excluded).
var allMCPToolNames = []string{
	"code_localize",
	"code_localize_agent",
	"compare_project_indexes",
	"delete_project",
	"detect_changes",
	"detect_cycles",
	"diff_graph",
	"diff_services",
	"explain_service",
	"explain_symbol",
	"find_rationale",
	"find_similar_functions",
	"generate_report",
	"get_affected_tests",
	"get_architecture",
	"get_change_coupling",
	"get_code_snippet",
	"get_graph_schema",
	"get_relationship_evidence",
	"get_relevant_context",
	"get_review_context",
	"index_health",
	"index_repository",
	"index_status",
	"ingest_traces",
	"list_projects",
	"localize_across_projects",
	"manage_adr",
	"query_graph",
	"query_security_surfaces",
	"query_stig_evidence",
	"rank_by_query",
	"search_code",
	"search_code_semantic",
	"search_graph",
	"service_map",
	"trace_call_path",
	"trace_data_flow",
	"visualize",
}
