package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/watcher"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callHandler invokes a tool handler the way the MCP dispatcher does and
// returns the raw result so tests can assert on both success and error
// shapes without going through the transport.
func callHandler(
	t *testing.T,
	handler func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error),
	toolName string,
	args map[string]any,
) *mcp.CallToolResult {
	t.Helper()
	argBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: toolName, Arguments: argBytes},
	}
	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("%s returned a Go error instead of a tool result: %v", toolName, err)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("%s returned nil/empty result", toolName)
	}
	return res
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestHandlers_SeededGraph_TableDriven exercises every graph-backed handler
// against the seeded security fixture with (a) a valid request and (b) the
// request shape a client most often gets wrong. It pins that handlers never
// surface Go errors to the dispatcher, that missing required arguments
// produce an error result rather than a panic, and that the happy path
// returns a JSON document.
func TestHandlers_SeededGraph_TableDriven(t *testing.T) {
	s := newServerWithSeededProject(t)
	outDir := t.TempDir()

	type tc struct {
		name    string
		handler func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error)
		args    map[string]any
		// wantError: the result must be flagged IsError.
		wantError bool
		// wantJSON: the result must parse as a JSON object (happy path).
		wantJSON bool
		// contains: substrings that must appear in the result text.
		contains []string
	}
	cases := []tc{
		// Graph analysis tools on the seeded project.
		{name: "detect_cycles/ok", handler: s.handleDetectCycles, args: map[string]any{"project": "test"}, wantJSON: true},
		{name: "detect_cycles/bad_project", handler: s.handleDetectCycles, args: map[string]any{"project": "nope-" + t.Name()}, wantError: true},
		{name: "degree_filter/ok", handler: s.handleDegreeFilter, args: map[string]any{"project": "test", "label": "Function", "direction": "inbound", "op": "eq", "value": 0}, wantJSON: true},
		{name: "degree_filter/missing_required", handler: s.handleDegreeFilter, args: map[string]any{"project": "test"}, wantError: true},
		{name: "explain_symbol/ok", handler: s.handleExplainSymbol, args: map[string]any{"project": "test", "name": "handle_request"}, wantJSON: true, contains: []string{"handle_request"}},
		{name: "explain_symbol/unknown", handler: s.handleExplainSymbol, args: map[string]any{"project": "test", "name": "no_such_symbol_xyz"}, wantError: true},
		{name: "explain_symbol/missing_name", handler: s.handleExplainSymbol, args: map[string]any{"project": "test"}, wantError: true},
		{name: "find_rationale/ok", handler: s.handleFindRationale, args: map[string]any{"project": "test"}, wantJSON: true},
		{name: "find_rationale/kind", handler: s.handleFindRationale, args: map[string]any{"project": "test", "kind": "SAFETY", "limit": 5}, wantJSON: true},
		{name: "service_map/ok", handler: s.handleServiceMap, args: map[string]any{"project": "test"}, wantJSON: true},
		{name: "service_map/with_libraries", handler: s.handleServiceMap, args: map[string]any{"project": "test", "include_libraries": true}, wantJSON: true},
		{name: "explain_service/missing_required", handler: s.handleExplainService, args: map[string]any{"project": "test"}, wantError: true},
		{name: "diff_services/missing_required", handler: s.handleDiffServices, args: map[string]any{"project": "test"}, wantError: true},
		{name: "get_relevant_context/ok", handler: s.handleRelevantContext, args: map[string]any{"project": "test", "files": []string{"svc-api/src/handler.rs"}}, wantJSON: true},
		{name: "get_relevant_context/missing_files", handler: s.handleRelevantContext, args: map[string]any{"project": "test"}, wantError: true},
		{name: "query_stig_evidence/ok", handler: s.handleSTIGEvidence, args: map[string]any{"project": "test", "control_id": "V-222596"}, wantJSON: true},
		{name: "query_stig_evidence/missing_control", handler: s.handleSTIGEvidence, args: map[string]any{"project": "test"}, wantError: true},
		{name: "visualize/escaping_output_path", handler: s.handleVisualize, args: map[string]any{"project": "test", "output_path": filepath.Join(outDir, "graph.html"), "max_nodes": 10}, wantError: true, contains: []string{"escapes project root"}},
		{name: "visualize/bad_extension", handler: s.handleVisualize, args: map[string]any{"project": "test", "output_path": "graph.txt"}, wantError: true},
		{name: "get_code_snippet/batch_unknown", handler: s.handleGetCodeSnippet, args: map[string]any{"project": "test", "qualified_names": []string{"test.handler.handle_request", "test.nope.missing"}}},

		// Tools that need Voyage or Anthropic credentials must degrade to an
		// error result, never a crash, when neither is configured.
		{name: "find_similar_functions/no_embeddings", handler: s.handleFindSimilarFunctions, args: map[string]any{"project": "test", "name": "handle_request"}, wantError: true},
		{name: "search_code_semantic/no_key", handler: s.handleSearchCodeSemantic, args: map[string]any{"project": "test", "query": "authentication"}, wantError: true},

		// Git-backed tools against a project whose root is not a repository.
		{name: "get_affected_tests/not_a_repo", handler: s.handleAffectedTests, args: map[string]any{"project": "test"}, wantError: true},
		{name: "detect_changes/not_a_repo", handler: s.handleDetectChanges, args: map[string]any{"project": "test"}, wantError: true},
		{name: "get_change_coupling/not_a_repo", handler: s.handleChangeCoupling, args: map[string]any{"project": "test"}},
		{name: "get_review_context/not_a_repo", handler: s.handleGetReviewContext, args: map[string]any{"project": "test"}, wantError: true},
		{name: "diff_graph/missing_required", handler: s.handleDiffGraph, args: map[string]any{"project": "test"}, wantError: true},
		{name: "ingest_traces/missing_file", handler: s.handleIngestTraces, args: map[string]any{"project": "test", "file_path": filepath.Join(outDir, "absent.json")}, wantError: true},
		{name: "ingest_traces/missing_required", handler: s.handleIngestTraces, args: map[string]any{"project": "test"}, wantError: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("VOYAGE_API_KEY", "")
			t.Setenv("ANTHROPIC_API_KEY", "")
			res := callHandler(t, c.handler, strings.SplitN(c.name, "/", 2)[0], c.args)
			text := resultText(t, res)
			if c.wantError && !res.IsError {
				t.Fatalf("expected an error result, got: %s", truncate(text, 300))
			}
			if c.wantJSON {
				if res.IsError {
					t.Fatalf("expected success, got error result: %s", truncate(text, 300))
				}
				var doc map[string]any
				if err := json.Unmarshal([]byte(text), &doc); err != nil {
					t.Fatalf("result is not a JSON object: %v\n%s", err, truncate(text, 300))
				}
			}
			for _, want := range c.contains {
				if !strings.Contains(text, want) {
					t.Fatalf("result missing %q: %s", want, truncate(text, 300))
				}
			}
		})
	}
}

// TestHandlers_ManageADR_Lifecycle walks manage_adr through every mode so the
// store/get/update/delete/auto branches are all exercised on one project.
func TestHandlers_ManageADR_Lifecycle(t *testing.T) {
	s := newServerWithSeededProject(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	res := callHandler(t, s.handleManageADR, "manage_adr", map[string]any{"project": "test", "mode": "get"})
	if res.IsError {
		t.Fatalf("get on empty project should not be an error result: %s", resultText(t, res))
	}

	res = callHandler(t, s.handleManageADR, "manage_adr", map[string]any{
		"project": "test", "mode": "store", "content": adrContent("Seeded fixture."),
	})
	if res.IsError {
		t.Fatalf("store: %s", resultText(t, res))
	}

	res = callHandler(t, s.handleManageADR, "manage_adr", map[string]any{"project": "test", "mode": "get"})
	if res.IsError || !strings.Contains(resultText(t, res), "Seeded fixture") {
		t.Fatalf("get after store: %s", truncate(resultText(t, res), 300))
	}

	res = callHandler(t, s.handleManageADR, "manage_adr", map[string]any{
		"project": "test", "mode": "update", "sections": map[string]any{"PURPOSE": "Updated fixture."},
	})
	if res.IsError {
		t.Fatalf("update: %s", resultText(t, res))
	}

	// auto runs the architecture summary; it must not depend on an LLM key.
	res = callHandler(t, s.handleManageADR, "manage_adr", map[string]any{"project": "test", "mode": "auto"})
	if res == nil {
		t.Fatal("auto returned nil")
	}

	res = callHandler(t, s.handleManageADR, "manage_adr", map[string]any{"project": "test", "mode": "delete"})
	if res.IsError {
		t.Fatalf("delete: %s", resultText(t, res))
	}

	res = callHandler(t, s.handleManageADR, "manage_adr", map[string]any{"project": "test", "mode": "bogus"})
	if !res.IsError {
		t.Fatalf("unknown mode should be an error result: %s", truncate(resultText(t, res), 200))
	}
	res = callHandler(t, s.handleManageADR, "manage_adr", map[string]any{"project": "test"})
	if !res.IsError {
		t.Fatal("missing mode should be an error result")
	}
}

// TestHandlers_DeleteProject removes a registered project and confirms a
// second delete reports the absence instead of failing hard.
func TestHandlers_DeleteProject(t *testing.T) {
	s, router := newServerWithRouter(t)
	s.watcher = watcher.New(router, s.syncProject)
	upsertTestProject(t, router, "doomed", filepath.Join(t.TempDir(), "doomed"))

	res := callHandler(t, s.handleDeleteProject, "delete_project", map[string]any{"project_name": "doomed"})
	if res.IsError {
		t.Fatalf("delete existing project: %s", resultText(t, res))
	}
	res = callHandler(t, s.handleDeleteProject, "delete_project", map[string]any{"project_name": "doomed"})
	if res == nil {
		t.Fatal("second delete returned nil")
	}
	res = callHandler(t, s.handleDeleteProject, "delete_project", map[string]any{})
	if !res.IsError {
		t.Fatal("missing project_name should be an error result")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// adrContent builds an ADR document with every section manage_adr requires.
func adrContent(context string) string {
	var b strings.Builder
	b.WriteString("# Architecture Decision Record\n\n")
	for _, section := range []string{"PURPOSE", "STACK", "ARCHITECTURE", "PATTERNS", "TRADEOFFS", "PHILOSOPHY"} {
		b.WriteString("## " + section + "\n\n" + context + "\n\n")
	}
	return b.String()
}
