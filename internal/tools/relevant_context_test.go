package tools

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// setupTestGraph creates a small in-memory graph for testing get_relevant_context.
//
//	auth.rs: authenticate(), validate_jwt()
//	handler.rs: handle_request() -CALLS-> authenticate()
//	db.rs: get_user() <-CALLS- authenticate()
//	auth_test.rs: test_auth() -TESTS-> authenticate()
//	config.rs: AppConfig (FILE_CHANGES_WITH auth.rs)
func setupTestGraph(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}

	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	nodes := []*store.Node{
		{Project: "test", Label: "Function", Name: "authenticate", QualifiedName: "test.auth.authenticate", FilePath: "src/auth.rs", StartLine: 10, EndLine: 40},
		{Project: "test", Label: "Function", Name: "validate_jwt", QualifiedName: "test.auth.validate_jwt", FilePath: "src/auth.rs", StartLine: 45, EndLine: 70},
		{Project: "test", Label: "Function", Name: "handle_request", QualifiedName: "test.handler.handle_request", FilePath: "src/handler.rs", StartLine: 1, EndLine: 30},
		{Project: "test", Label: "Function", Name: "get_user", QualifiedName: "test.db.get_user", FilePath: "src/db.rs", StartLine: 1, EndLine: 25},
		{Project: "test", Label: "Function", Name: "test_auth", QualifiedName: "test.auth_test.test_auth", FilePath: "tests/auth_test.rs", StartLine: 1, EndLine: 20},
		{Project: "test", Label: "Struct", Name: "AppConfig", QualifiedName: "test.config.AppConfig", FilePath: "src/config.rs", StartLine: 1, EndLine: 15},
		// A distant node not connected to auth
		{Project: "test", Label: "Function", Name: "format_log", QualifiedName: "test.logging.format_log", FilePath: "src/logging.rs", StartLine: 1, EndLine: 10},
	}

	ids := make(map[string]int64)
	for _, n := range nodes {
		id, upsertErr := st.UpsertNode(n)
		if upsertErr != nil {
			t.Fatalf("UpsertNode %s: %v", n.Name, upsertErr)
		}
		ids[n.Name] = id
	}

	// Edges: handle_request -> authenticate (CALLS)
	if _, err := st.InsertEdge(&store.Edge{
		Project: "test", SourceID: ids["handle_request"], TargetID: ids["authenticate"],
		Type: "CALLS",
	}); err != nil {
		t.Fatalf("UpsertEdge handler->auth: %v", err)
	}

	// authenticate -> get_user (CALLS)
	if _, err := st.InsertEdge(&store.Edge{
		Project: "test", SourceID: ids["authenticate"], TargetID: ids["get_user"],
		Type: "CALLS",
	}); err != nil {
		t.Fatalf("UpsertEdge auth->db: %v", err)
	}

	// authenticate -> validate_jwt (CALLS, same file)
	if _, err := st.InsertEdge(&store.Edge{
		Project: "test", SourceID: ids["authenticate"], TargetID: ids["validate_jwt"],
		Type: "CALLS",
	}); err != nil {
		t.Fatalf("UpsertEdge auth->jwt: %v", err)
	}

	// test_auth -> authenticate (TESTS)
	if _, err := st.InsertEdge(&store.Edge{
		Project: "test", SourceID: ids["test_auth"], TargetID: ids["authenticate"],
		Type: "TESTS",
	}); err != nil {
		t.Fatalf("UpsertEdge test->auth: %v", err)
	}

	// config.rs <-> auth.rs (FILE_CHANGES_WITH)
	if _, err := st.InsertEdge(&store.Edge{
		Project: "test", SourceID: ids["AppConfig"], TargetID: ids["authenticate"],
		Type: "FILE_CHANGES_WITH", Properties: map[string]any{"coupling_score": 0.7},
	}); err != nil {
		t.Fatalf("UpsertEdge config<->auth: %v", err)
	}

	return st
}

func TestRelevantContextFindsCallersCalleesTests(t *testing.T) {
	st := setupTestGraph(t)
	defer st.Close()

	projName := "test"
	files := []string{"src/auth.rs"}
	fileScores := make(map[string]*contextFile)

	// Find target nodes
	nodes, err := st.FindNodesByFile(projName, "src/auth.rs")
	if err != nil {
		t.Fatalf("FindNodesByFile: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected nodes in src/auth.rs")
	}

	targetNodeIDs := make([]int64, 0, len(nodes))
	for _, n := range nodes {
		targetNodeIDs = append(targetNodeIDs, n.ID)
	}
	fileScores["src/auth.rs"] = &contextFile{
		File: "src/auth.rs", Relationship: "target", Priority: 1, TokenEst: 280,
	}

	// Find callers/callees
	callEdgeTypes := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}
	outEdges, _ := st.FindEdgesBySourceIDs(targetNodeIDs, callEdgeTypes)
	inEdges, _ := st.FindEdgesByTargetIDs(targetNodeIDs, callEdgeTypes)

	callerCalleeIDs := make(map[int64]string)
	for _, edges := range outEdges {
		for _, e := range edges {
			callerCalleeIDs[e.TargetID] = "callee"
		}
	}
	for _, edges := range inEdges {
		for _, e := range edges {
			callerCalleeIDs[e.SourceID] = "caller"
		}
	}
	addNodesAsFiles(st, callerCalleeIDs, fileScores, 2, files)

	// Find tests
	testIDs := make(map[int64]string)
	for _, nid := range targetNodeIDs {
		edges, findErr := st.FindEdgesByTargetAndType(nid, "TESTS")
		if findErr != nil {
			continue
		}
		for _, e := range edges {
			testIDs[e.SourceID] = "test"
		}
	}
	addNodesAsFiles(st, testIDs, fileScores, 3, files)

	// Find change-coupled
	changeCoupled := findChangeCoupledFiles(st, projName, files)
	for f, score := range changeCoupled {
		if _, exists := fileScores[f]; !exists {
			fileScores[f] = &contextFile{
				File: f, Relationship: "change_coupled", Priority: 4, TokenEst: 500,
			}
		}
		_ = score
	}

	// Verify: target file included
	if _, ok := fileScores["src/auth.rs"]; !ok {
		t.Error("expected src/auth.rs (target)")
	}

	// Verify: caller (handler.rs) included
	if cf, ok := fileScores["src/handler.rs"]; !ok {
		t.Error("expected src/handler.rs (caller)")
	} else if cf.Relationship != "caller" {
		t.Errorf("handler.rs relationship = %q, want 'caller'", cf.Relationship)
	}

	// Verify: callee (db.rs) included
	if cf, ok := fileScores["src/db.rs"]; !ok {
		t.Error("expected src/db.rs (callee)")
	} else if cf.Relationship != "callee" {
		t.Errorf("db.rs relationship = %q, want 'callee'", cf.Relationship)
	}

	// Verify: test file included
	if cf, ok := fileScores["tests/auth_test.rs"]; !ok {
		t.Error("expected tests/auth_test.rs (test)")
	} else if cf.Relationship != "test" {
		t.Errorf("auth_test.rs relationship = %q, want 'test'", cf.Relationship)
	}

	// Verify: unrelated file NOT included
	if _, ok := fileScores["src/logging.rs"]; ok {
		t.Error("src/logging.rs should NOT be in context (unrelated)")
	}
}

func TestRelevantContextRespectsTokenBudget(t *testing.T) {
	st := setupTestGraph(t)
	defer st.Close()

	projName := "test"
	files := []string{"src/auth.rs"}

	nodes, _ := st.FindNodesByFile(projName, "src/auth.rs")
	targetNodeIDs := make([]int64, 0, len(nodes))
	for _, n := range nodes {
		targetNodeIDs = append(targetNodeIDs, n.ID)
	}

	fileScores := make(map[string]*contextFile)
	fileScores["src/auth.rs"] = &contextFile{
		File: "src/auth.rs", Relationship: "target", Priority: 1, TokenEst: 280,
	}

	// Add callers/callees
	callEdgeTypes := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}
	outEdges, _ := st.FindEdgesBySourceIDs(targetNodeIDs, callEdgeTypes)
	inEdges, _ := st.FindEdgesByTargetIDs(targetNodeIDs, callEdgeTypes)

	callerCalleeIDs := make(map[int64]string)
	for _, edges := range outEdges {
		for _, e := range edges {
			callerCalleeIDs[e.TargetID] = "callee"
		}
	}
	for _, edges := range inEdges {
		for _, e := range edges {
			callerCalleeIDs[e.SourceID] = "caller"
		}
	}
	addNodesAsFiles(st, callerCalleeIDs, fileScores, 2, files)

	// Add tests
	testIDs := make(map[int64]string)
	for _, nid := range targetNodeIDs {
		edges, findErr := st.FindEdgesByTargetAndType(nid, "TESTS")
		if findErr != nil {
			continue
		}
		for _, e := range edges {
			testIDs[e.SourceID] = "test"
		}
	}
	addNodesAsFiles(st, testIDs, fileScores, 3, files)

	// Sort and apply a tiny token budget
	result := make([]*contextFile, 0, len(fileScores))
	for _, cf := range fileScores {
		result = append(result, cf)
	}

	// With a budget of 500 tokens, only the target (280 tokens) + maybe one more should fit
	budget := 500
	usedTokens := 0
	var selected []*contextFile
	// Sort by priority
	for p := 1; p <= 5; p++ {
		for _, cf := range result {
			if cf.Priority == p && usedTokens+cf.TokenEst <= budget {
				selected = append(selected, cf)
				usedTokens += cf.TokenEst
			}
		}
	}

	if usedTokens > budget {
		t.Errorf("used %d tokens, exceeds budget %d", usedTokens, budget)
	}

	// Target should always be included (280 tokens < 500 budget)
	hasTarget := false
	for _, cf := range selected {
		if cf.File == "src/auth.rs" {
			hasTarget = true
		}
	}
	if !hasTarget {
		t.Error("target file should always fit within budget")
	}

	// With only 220 tokens remaining, not all files should fit
	if len(selected) >= len(result) {
		t.Errorf("expected some files excluded at budget=%d, but all %d fit", budget, len(result))
	}
}

func TestGetStringSliceArg(t *testing.T) {
	args := map[string]any{
		"files":   []any{"src/auth.rs", "src/handler.rs"},
		"empty":   []any{},
		"invalid": "not-a-slice",
	}

	files := getStringSliceArg(args, "files")
	if len(files) != 2 || files[0] != "src/auth.rs" || files[1] != "src/handler.rs" {
		t.Errorf("unexpected files: %v", files)
	}

	empty := getStringSliceArg(args, "empty")
	if len(empty) != 0 {
		t.Errorf("expected empty slice, got %v", empty)
	}

	invalid := getStringSliceArg(args, "invalid")
	if invalid != nil {
		t.Errorf("expected nil for invalid type, got %v", invalid)
	}

	missing := getStringSliceArg(args, "missing")
	if missing != nil {
		t.Errorf("expected nil for missing key, got %v", missing)
	}
}
