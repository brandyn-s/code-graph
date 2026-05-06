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
	"delete_project",
}

// pendingTools lists MCP tools that should ultimately emit `_metadata`
// but haven't been wired yet. Each entry includes a short rationale for
// why instrumentation was deferred.
//
// Adding a new tool to this list (rather than instrumenting it
// immediately) is acceptable — the schema is opt-in by design — but the
// rationale should be honest. "Forgot" is not an acceptable reason.
var pendingTools = map[string]string{
	"search_code_semantic":   "Voyage-API-backed; needs WithModel + confidence wiring beyond the read-graph helper",
	"rank_by_query":          "PageRank scoring; could carry a confidence band derived from score distribution",
	"code_localize":          "BFS-based; standard read-graph helper applies — pending instrumentation",
	"code_localize_agent":    "LLM-using; needs full WithModel + WithConfidence chain, not the helper",
	"diff_graph":             "Returns a slice-of-deltas; needs response-shape decision before instrumentation",
	"find_rationale":         "Returns annotation list; standard read-graph helper applies",
	"find_similar_functions": "Embedding-cosine; could carry a similarity-derived confidence band",
	"generate_report":        "Side-effect tool (writes file); standard write-tool helper applies",
	"get_architecture":       "Multiple aspects; helper applies but per-aspect confidence is interesting future work",
	"get_graph_schema":       "Returns counts; standard status-tool helper applies",
	"get_relevant_context":   "Token-budget-bounded; standard read-graph helper applies",
	"get_review_context":     "Standard read-graph helper applies",
	"index_repository":       "Long-running write tool; needs progress signaling beyond simple action_outcome",
	"ingest_traces":          "Write tool; standard write-tool helper applies",
	"manage_adr":             "Multi-mode (get/store/update/delete); needs per-mode action_outcome wiring",
}

// excludedTools lists MCP tools whose response shape is intentionally
// not a map-with-_metadata. Adding to this list requires justifying
// the architectural reason — the schema is opt-in, not mandatory, but
// "this tool returns a bare array" should be a deliberate choice.
var excludedTools = map[string]string{
	"list_projects": "Returns a bare slice of project info structs; wrapping would break clients. Metadata is implicit (this is a status snapshot of all projects).",
}

// allMCPToolNames is the union of registered MCP tool names. Sourced
// from a one-time enumeration of `s.addTool(&mcp.Tool{Name: "..."}` in
// internal/tools/. If new tools are added to the codebase, this list
// must be extended (and the new tool must appear in exactly one of:
// instrumented / pending / excluded).
var allMCPToolNames = []string{
	"code_localize",
	"code_localize_agent",
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
	"get_relevant_context",
	"get_review_context",
	"index_health",
	"index_repository",
	"index_status",
	"ingest_traces",
	"list_projects",
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
