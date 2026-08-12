package tools

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func TestLocalizeAcrossProjectsReturnsStableProjectBalancedResults(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.CloseAll)
	for _, project := range []string{"beta", "alpha"} {
		st, release, err := router.AcquireStore(project)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertProject(project, "/tmp/"+project); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertNode(&store.Node{
			Project: project, Label: "Function", Name: "Authenticate",
			QualifiedName: project + ".auth.Authenticate", FilePath: "auth.go",
			StartLine: 10, EndLine: 20,
		}); err != nil {
			t.Fatal(err)
		}
		release()
	}

	srv := NewServer(router)
	response := metadataResponseFromHandler(t, srv.handleLocalizeAcrossProjects, "localize_across_projects", map[string]any{
		"query":             "Authenticate",
		"seed_strategy":     "substring",
		"depth":             0,
		"per_project_top_k": 2,
		"top_k":             10,
	})
	if got := response["projects_attempted"]; got != float64(2) {
		t.Fatalf("projects_attempted = %v, want 2", got)
	}
	if got := response["projects_with_matches"]; got != float64(2) {
		t.Fatalf("projects_with_matches = %v, want 2", got)
	}
	results, ok := response["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results = %T %v, want two", response["results"], response["results"])
	}
	first := results[0].(map[string]any)
	second := results[1].(map[string]any)
	if first["project"] != "alpha" || second["project"] != "beta" {
		t.Fatalf("project order = %v, %v; want alpha, beta", first["project"], second["project"])
	}
	if response["ranking_policy"] != "project_balanced_round_robin" {
		t.Fatalf("ranking_policy = %v", response["ranking_policy"])
	}
	if response["cross_project_score_comparable"] != false {
		t.Fatalf("cross_project_score_comparable = %v, want false", response["cross_project_score_comparable"])
	}
}
