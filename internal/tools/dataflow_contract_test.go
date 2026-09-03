package tools

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

func TestTraceDataFlowDeclaresGraphReachabilityContract(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.CloseAll)
	const project = "dataflow-contract"
	st, release, err := router.AcquireStore(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProject(project, "/tmp/dataflow-contract"); err != nil {
		t.Fatal(err)
	}
	source := upsertSecNode(t, st, project, "entry", "app.entry", "input_entry_point", "http_handler", "app.py")
	sink := upsertSecNode(t, st, project, "sink", "db.sink", "sensitive_sink", "sql_query", "db.py")
	if _, err := st.InsertEdge(&store.Edge{Project: project, SourceID: source, TargetID: sink, Type: "CALLS"}); err != nil {
		t.Fatal(err)
	}
	release()

	srv := NewServer(router)
	response := metadataResponseFromHandler(t, srv.handleDataFlow, "trace_data_flow", map[string]any{
		"source":  "entry",
		"project": project,
	})
	contract, ok := response["analysis_contract"].(map[string]any)
	if !ok {
		t.Fatalf("analysis_contract missing or wrong type: %T", response["analysis_contract"])
	}
	if got := contract["analysis_kind"]; got != "interprocedural_graph_reachability" {
		t.Fatalf("analysis_kind = %v", got)
	}
	if got := contract["variable_level_taint"]; got != false {
		t.Fatalf("variable_level_taint = %v, want false", got)
	}
	if got := contract["edge_semantics"]; got != "CALLS_READS_WRITES_USAGE_connectivity" {
		t.Fatalf("edge_semantics = %v", got)
	}
}

func TestTraceDataFlowFailsClosedWhenVariableLevelTaintIsRequired(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.CloseAll)
	const project = "taint-contract"
	st, release, err := router.AcquireStore(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProject(project, "/tmp/taint-contract"); err != nil {
		t.Fatal(err)
	}
	release()
	srv := NewServer(router)

	response := metadataResponseFromHandler(t, srv.handleDataFlow, "trace_data_flow", map[string]any{
		"source":             "untrusted_input",
		"required_assurance": "variable_level_taint",
		"project":            project,
	})
	if got := response["status"]; got != "requires_external_analyzer" {
		t.Fatalf("status = %v, want requires_external_analyzer", got)
	}
	if got := response["recommended_analyzer"]; got != "CodeQL" {
		t.Fatalf("recommended_analyzer = %v, want CodeQL", got)
	}
	if _, present := response["flow_path"]; present {
		t.Fatal("variable-level request must not return graph reachability as if it were taint evidence")
	}
}
