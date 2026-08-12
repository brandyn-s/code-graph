package store

import "testing"

func TestFindNodeSeedCandidatesProjectsCoreFieldsAndFiltersTokens(t *testing.T) {
	st, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer st.Close()

	const project = "seed-candidates"
	if err := st.UpsertProject(project, "/tmp/seed-candidates"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	for _, node := range []*Node{
		{Project: project, Label: "Function", Name: "Parser", QualifiedName: "pkg.syntax.Parser", FilePath: "parser.go", Properties: map[string]any{"large": "unused"}},
		{Project: project, Label: "Method", Name: "Run", QualifiedName: "pkg.diagnostics.Run", FilePath: "diagnostics.go", Properties: map[string]any{"large": "unused"}},
		{Project: project, Label: "Function", Name: "Unrelated", QualifiedName: "pkg.other.Unrelated", FilePath: "other.go"},
	} {
		if _, err := st.UpsertNode(node); err != nil {
			t.Fatalf("UpsertNode(%s): %v", node.Name, err)
		}
	}

	got, err := st.FindNodeSeedCandidates(project, []string{"parser", "run"}, []string{"diagnostics"})
	if err != nil {
		t.Fatalf("FindNodeSeedCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	byName := make(map[string]*Node, len(got))
	for _, node := range got {
		byName[node.Name] = node
		if node.Properties != nil {
			t.Fatalf("candidate %q decoded properties unexpectedly: %+v", node.Name, node.Properties)
		}
	}
	if byName["Parser"] == nil || byName["Parser"].QualifiedName != "pkg.syntax.Parser" {
		t.Fatalf("missing exact-name candidate: %+v", got)
	}
	if byName["Run"] == nil || byName["Run"].QualifiedName != "pkg.diagnostics.Run" {
		t.Fatalf("missing qualified-name candidate: %+v", got)
	}
}
