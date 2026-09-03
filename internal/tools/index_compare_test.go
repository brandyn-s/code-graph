package tools

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

func TestCompareProjectIndexesReportsDeterministicFileAndSymbolDelta(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.CloseAll)

	seed := func(project string, hashes map[string]string, nodes []*store.Node) {
		t.Helper()
		st, release, err := router.AcquireStore(project)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := st.UpsertProject(project, "/tmp/"+project); err != nil {
			t.Fatal(err)
		}
		for path, hash := range hashes {
			if err := st.UpsertFileHash(project, path, hash, 0, 1); err != nil {
				t.Fatal(err)
			}
		}
		for _, node := range nodes {
			node.Project = project
			if _, err := st.UpsertNode(node); err != nil {
				t.Fatal(err)
			}
		}
	}

	seed("base", map[string]string{
		"same.go": "same", "modified.go": "old", "removed.go": "gone",
	}, []*store.Node{
		{Label: "Function", Name: "Authenticate", QualifiedName: "auth.Authenticate", FilePath: "modified.go", StartLine: 10, EndLine: 20},
		{Label: "Function", Name: "Legacy", QualifiedName: "auth.Legacy", FilePath: "removed.go", StartLine: 1, EndLine: 4},
	})
	seed("target", map[string]string{
		"added.go": "new", "modified.go": "newer", "same.go": "same",
	}, []*store.Node{
		{Label: "Function", Name: "Authenticate", QualifiedName: "auth.Authenticate", FilePath: "modified.go", StartLine: 11, EndLine: 22},
		{Label: "Function", Name: "Current", QualifiedName: "auth.Current", FilePath: "added.go", StartLine: 1, EndLine: 5},
	})

	srv := NewServer(router)
	response := metadataResponseFromHandler(t, srv.handleCompareProjectIndexes, "compare_project_indexes", map[string]any{
		"base_project":   "base",
		"target_project": "target",
		"limit":          20,
	})

	files := requireMapValue(t, response["file_delta"], "file_delta")
	for key, want := range map[string]float64{"added_count": 1, "removed_count": 1, "modified_count": 1, "unchanged_count": 1} {
		if got := files[key]; got != want {
			t.Fatalf("file_delta.%s = %v, want %v", key, got, want)
		}
	}
	symbols := requireMapValue(t, response["symbol_delta"], "symbol_delta")
	for key, want := range map[string]float64{"added_count": 1, "removed_count": 1, "changed_count": 1} {
		if got := symbols[key]; got != want {
			t.Fatalf("symbol_delta.%s = %v, want %v", key, got, want)
		}
	}
	changed, ok := symbols["changed"].([]any)
	if !ok || len(changed) != 1 {
		t.Fatalf("changed symbols = %T %v, want one entry", symbols["changed"], symbols["changed"])
	}
	firstChanged := requireMapValue(t, changed[0], "symbol_delta.changed[0]")
	if firstChanged["qualified_name"] != "auth.Authenticate" {
		t.Fatalf("changed symbols = %v", changed)
	}
	if response["comparison_contract"] != "immutable_index_snapshot_delta" {
		t.Fatalf("comparison_contract = %v", response["comparison_contract"])
	}
}
