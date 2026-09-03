package tools

// Get-well plan Phase 2.2 (2026-05-06): runtime metadata coverage.
//
// The pre-existing TestMetadataCoverage_* tests verify the static
// categorization (instrumentedTools / pendingTools / excludedTools is
// disjoint and complete). They do NOT verify that each tool actually
// emits a `_metadata` field at runtime when invoked.
//
// This file closes that gap for the subset of instrumented tools whose
// handlers can be invoked against the in-memory security fixture
// (setupSecurityGraph from testutil_test.go). For each, we:
//   1. Construct a CallToolRequest with valid arguments.
//   2. Invoke the handler.
//   3. Parse the response JSON.
//   4. Assert `_metadata` is present and well-formed.
//
// Tools NOT exercised here (out of scope for this round):
//   - LLM-using tools (code_localize_agent — needs ANTHROPIC_API_KEY)
//   - Embedding-dependent tools (search_code_semantic, find_similar_functions
//     — need Voyage embeddings populated)
//   - Tools with heavy filesystem requirements (visualize, generate_report,
//     index_repository, ingest_traces, get_relevant_context with
//     include_content=true)
//   - get_review_context (excluded — returns markdown, not JSON)
//
// What this catches: a future PR that removes the `_metadata` field
// from one of these handlers' response paths. Pre-Phase-2 such a
// regression would only surface when an operator runs the tool
// against a real index; the static coverage test would still pass.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// seedSecurityGraphInto seeds the same nodes/edges as setupSecurityGraph
// into a caller-supplied router-managed Store. Differs from
// setupSecurityGraph (testutil_test.go) only in that it doesn't create
// its own store — the caller controls store lifecycle so the router
// can find the project later.
func seedSecurityGraphInto(t *testing.T, st *store.Store, projectName string) {
	t.Helper()
	nodes := []*store.Node{
		{Project: projectName, Label: "Function", Name: "handle_request",
			QualifiedName: projectName + ".handler.handle_request", FilePath: "svc-api/src/handler.rs",
			StartLine: 1, EndLine: 30,
			Properties: map[string]any{"security_role": "input_entry_point", "security_subtype": "http_handler"},
		},
		{Project: projectName, Label: "Function", Name: "authenticate",
			QualifiedName: projectName + ".auth.authenticate", FilePath: "svc-api/src/auth.rs",
			StartLine: 10, EndLine: 40,
			Properties: map[string]any{"security_role": "auth_boundary"},
		},
		{Project: projectName, Label: "Function", Name: "get_user",
			QualifiedName: projectName + ".db.get_user", FilePath: "svc-api/src/db.rs",
			StartLine: 1, EndLine: 25,
			Properties: map[string]any{"security_role": "sensitive_sink", "security_subtype": "database"},
		},
		{Project: projectName, Label: "Function", Name: "main",
			QualifiedName: projectName + ".main.main", FilePath: "svc-api/src/main.rs",
			StartLine: 1, EndLine: 20,
			Properties: map[string]any{"security_role": "input_entry_point"},
		},
	}
	ids := make(map[string]int64, len(nodes))
	for _, n := range nodes {
		id, err := st.UpsertNode(n)
		if err != nil {
			t.Fatalf("UpsertNode %s: %v", n.Name, err)
		}
		ids[n.Name] = id
	}
	edges := []struct {
		src, tgt, typ string
	}{
		{"handle_request", "authenticate", "CALLS"},
		{"authenticate", "get_user", "CALLS"},
		{"main", "handle_request", "CALLS"},
	}
	for _, e := range edges {
		_, err := st.InsertEdge(&store.Edge{
			Project:  projectName,
			SourceID: ids[e.src],
			TargetID: ids[e.tgt],
			Type:     e.typ,
		})
		if err != nil {
			t.Fatalf("InsertEdge %s->%s: %v", e.src, e.tgt, err)
		}
	}
}

// metadataResponseFromHandler invokes a handler and extracts the JSON
// response text + parses it as a map. Used by each per-tool subtest to
// check `_metadata` presence.
func metadataResponseFromHandler(
	t *testing.T,
	handler func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error),
	toolName string,
	args map[string]any,
) map[string]any {
	t.Helper()
	argBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      toolName,
			Arguments: argBytes,
		},
	}
	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("%s handler error: %v", toolName, err)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("%s returned nil/empty result", toolName)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s response Content[0] is not TextContent: %T", toolName, res.Content[0])
	}
	if strings.HasPrefix(strings.TrimSpace(tc.Text), "{") == false {
		t.Fatalf("%s response is not JSON-shaped: %.100s", toolName, tc.Text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("%s response not valid JSON: %v\nbody: %.300s", toolName, err, tc.Text)
	}
	return out
}

// assertMetadataPresent verifies the response has a non-nil `_metadata`
// key whose value is a populated map. Reports the full response on
// failure so debugging is one read away.
func assertMetadataPresent(t *testing.T, toolName string, response map[string]any) {
	t.Helper()
	mdRaw, ok := response["_metadata"]
	if !ok {
		t.Errorf("%s response missing required `_metadata` field; keys=%v",
			toolName, mapKeys(response))
		return
	}
	md, ok := mdRaw.(map[string]any)
	if !ok {
		t.Errorf("%s `_metadata` is not a map: %T", toolName, mdRaw)
		return
	}
	if len(md) == 0 {
		t.Errorf("%s `_metadata` is an empty map; expected provenance / freshness / etc.", toolName)
		return
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// newServerWithSeededProject builds a Server with a real router-backed
// project and seeds it with the security graph fixture. Mirrors
// setupSecurityGraph but routed through StoreRouter so handlers can
// resolve the project via s.resolveStore / s.router.ForProject.
func newServerWithSeededProject(t *testing.T) *Server {
	t.Helper()
	s, router := newServerWithRouter(t)
	const projectName = "test"
	upsertTestProject(t, router, projectName, "/tmp/test")
	st, err := router.ForProject(projectName)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	seedSecurityGraphInto(t, st, projectName)
	s.sessionProject = projectName
	// Phase 2.2 fix: handlers like search_graph dereference
	// s.queryCache. The minimal Server fixture must initialize the
	// cache to avoid nil-pointer panics during runtime invocation.
	s.queryCache = store.NewQueryCache(8, 1*time.Minute)
	return s
}

// TestMetadata_Runtime_SearchGraph: search_graph (instrumented since
// Plan 1 A1) must emit `_metadata` at runtime.
func TestMetadata_Runtime_SearchGraph(t *testing.T) {
	s := newServerWithSeededProject(t)
	resp := metadataResponseFromHandler(
		t,
		s.handleSearchGraph,
		"search_graph",
		map[string]any{
			"query":   "handle_request",
			"project": "test",
		},
	)
	assertMetadataPresent(t, "search_graph", resp)
}

// TestMetadata_Runtime_QueryGraph: query_graph must emit `_metadata`.
func TestMetadata_Runtime_QueryGraph(t *testing.T) {
	s := newServerWithSeededProject(t)
	resp := metadataResponseFromHandler(
		t,
		s.handleQueryGraph,
		"query_graph",
		map[string]any{
			"query":   "MATCH (n:Function) RETURN n.name LIMIT 5",
			"project": "test",
		},
	)
	assertMetadataPresent(t, "query_graph", resp)
}

// TestMetadata_Runtime_QuerySecuritySurfaces: instrumented since Plan 1 A1.
func TestMetadata_Runtime_QuerySecuritySurfaces(t *testing.T) {
	s := newServerWithSeededProject(t)
	resp := metadataResponseFromHandler(
		t,
		s.handleQuerySecuritySurfaces,
		"query_security_surfaces",
		map[string]any{
			"project": "test",
			"role":    "auth_boundary",
		},
	)
	assertMetadataPresent(t, "query_security_surfaces", resp)
}

// TestMetadata_Runtime_GetGraphSchema: status-tool helper (Plan 5 Phase B).
func TestMetadata_Runtime_GetGraphSchema(t *testing.T) {
	s := newServerWithSeededProject(t)
	resp := metadataResponseFromHandler(
		t,
		s.handleGetGraphSchema,
		"get_graph_schema",
		map[string]any{"project": "test"},
	)
	assertMetadataPresent(t, "get_graph_schema", resp)
}

// TestMetadata_Runtime_FindRationale: read-graph helper (Plan 5 Phase B).
func TestMetadata_Runtime_FindRationale(t *testing.T) {
	s := newServerWithSeededProject(t)
	resp := metadataResponseFromHandler(
		t,
		s.handleFindRationale,
		"find_rationale",
		map[string]any{"project": "test"},
	)
	assertMetadataPresent(t, "find_rationale", resp)
}

// TestMetadata_Runtime_TraceCallPath: instrumented since Plan 1 A1.
func TestMetadata_Runtime_TraceCallPath(t *testing.T) {
	s := newServerWithSeededProject(t)
	resp := metadataResponseFromHandler(
		t,
		s.handleTraceCallPath,
		"trace_call_path",
		map[string]any{
			"function_name": "handle_request",
			"project":       "test",
			"depth":         1,
		},
	)
	assertMetadataPresent(t, "trace_call_path", resp)
}
