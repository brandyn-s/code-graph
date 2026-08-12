package store

import "testing"

func TestFindEdgeEndpointsByTypesFiltersWithoutDecodingProperties(t *testing.T) {
	st, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer st.Close()

	const project = "edge-endpoints"
	if err := st.UpsertProject(project, "/tmp/edge-endpoints"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	sourceID, err := st.UpsertNode(&Node{
		Project: project, Label: "Function", Name: "source", QualifiedName: project + ".source",
	})
	if err != nil {
		t.Fatalf("UpsertNode(source): %v", err)
	}
	targetID, err := st.UpsertNode(&Node{
		Project: project, Label: "Function", Name: "target", QualifiedName: project + ".target",
	})
	if err != nil {
		t.Fatalf("UpsertNode(target): %v", err)
	}

	allowedID, err := st.InsertEdge(&Edge{
		Project: project, SourceID: sourceID, TargetID: targetID, Type: "CALLS",
		Properties: map[string]any{"confidence_tier": "INFERRED", "resolver_rule": "test-only"},
	})
	if err != nil {
		t.Fatalf("InsertEdge(CALLS): %v", err)
	}
	if _, err := st.InsertEdge(&Edge{
		Project: project, SourceID: sourceID, TargetID: targetID, Type: "USAGE",
	}); err != nil {
		t.Fatalf("InsertEdge(USAGE): %v", err)
	}

	got, err := st.FindEdgeEndpointsByTypes(project, []string{"CALLS"})
	if err != nil {
		t.Fatalf("FindEdgeEndpointsByTypes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d endpoints, want 1: %+v", len(got), got)
	}
	if got[0].SourceID != sourceID || got[0].TargetID != targetID || got[0].Type != "CALLS" {
		t.Fatalf("unexpected endpoint projection: %+v", got[0])
	}
	if allowedID == 0 {
		t.Fatal("expected persisted edge id")
	}
}
