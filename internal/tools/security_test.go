package tools

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

// --- Pure function tests (no store needed) ---

func TestIsInfraCallee(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		// Should match (infra callees)
		{"init", true},
		{"Init", true},
		{"INIT", true},
		{"setup", true},
		{"Setup", true},
		{"configure", true},
		{"log_init", true},
		{"logInit", true},
		{"set_logger", true},
		{"setLogger", true},
		{"shutdown", true},
		{"cleanup", true},
		{"close", true},
		{"drop", true},
		{"signal_handler", true},
		{"panic_hook", true},
		// Should NOT match (real functions)
		{"handle_request", false},
		{"authenticate", false},
		{"parse_input", false},
		{"initialize_database", false}, // "initialize" != "init"
		{"get_config", false},
		{"process_order", false},
		{"validate_jwt", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isInfraCallee(tt.name)
			if got != tt.want {
				t.Errorf("isInfraCallee(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestIsTestNode(t *testing.T) {
	tests := []struct {
		name string
		node *store.Node
		want bool
	}{
		// Test file patterns
		{"go test file", &store.Node{Name: "foo", FilePath: "pkg/auth_test.go"}, true},
		{"rust test file", &store.Node{Name: "foo", FilePath: "src/auth_test.rs"}, true},
		{"python test file", &store.Node{Name: "foo", FilePath: "test_auth.py"}, true},
		{"python test dir", &store.Node{Name: "foo", FilePath: "tests/test_auth.py"}, true},
		{"js test dir", &store.Node{Name: "foo", FilePath: "__tests__/auth.js"}, true},
		{"spec dir", &store.Node{Name: "foo", FilePath: "spec/auth_spec.rb"}, true},
		{"ts spec file", &store.Node{Name: "foo", FilePath: "src/auth_spec.ts"}, true},
		// Test function name patterns
		{"go test func", &store.Node{Name: "TestAuthenticate", FilePath: "src/auth.go"}, true},
		{"python test func", &store.Node{Name: "test_authenticate", FilePath: "src/auth.py"}, true},
		// QN patterns
		{"qn with .test.", &store.Node{Name: "foo", QualifiedName: "pkg.test.auth", FilePath: "src/auth.go"}, true},
		{"qn with .tests.", &store.Node{Name: "foo", QualifiedName: "pkg.tests.auth", FilePath: "src/auth.go"}, true},
		// NOT test nodes
		{"regular source", &store.Node{Name: "authenticate", FilePath: "src/auth.rs"}, false},
		{"regular go", &store.Node{Name: "handleRequest", FilePath: "src/handler.go"}, false},
		{"test in name but not prefix", &store.Node{Name: "latestVersion", FilePath: "src/version.go"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTestNode(tt.node)
			if got != tt.want {
				t.Errorf("isTestNode(%q, %q) = %v, want %v", tt.node.Name, tt.node.FilePath, got, tt.want)
			}
		})
	}
}

func TestFilterTestNodes(t *testing.T) {
	nodes := []*store.Node{
		{Name: "authenticate", FilePath: "src/auth.rs"},
		{Name: "test_auth", FilePath: "tests/test_auth.py"},
		{Name: "get_user", FilePath: "src/db.rs"},
		{Name: "TestDB", FilePath: "src/db_test.go"},
	}

	t.Run("exclude=true filters test nodes", func(t *testing.T) {
		result := filterTestNodes(nodes, true)
		if len(result) != 2 {
			t.Fatalf("expected 2 nodes, got %d", len(result))
		}
		for _, n := range result {
			if isTestNode(n) {
				t.Errorf("test node %q should have been filtered", n.Name)
			}
		}
	})

	t.Run("exclude=false passes all through", func(t *testing.T) {
		result := filterTestNodes(nodes, false)
		if len(result) != len(nodes) {
			t.Errorf("expected %d nodes, got %d", len(nodes), len(result))
		}
	})
}

// --- Graph-dependent tests ---

// setupSanitizerGraph builds a graph that exercises path-level sanitization,
// including the failure mode the previous implementation got wrong: a sanitizer
// on a SIBLING branch that is not actually on the source->sink path.
//
//	src ─CALLS→ san1 (sanitizer) ─CALLS→ sink2     # sink2: only path crosses san1 → SANITIZED
//	                              └CALLS→ sink3     # sink3 also reachable via san1 ...
//	src ─CALLS→ mid             ─CALLS→ sink1       # sink1: path avoids san1 → UNSANITIZED
//	src ─CALLS→ bypass          ─CALLS→ sink3       # ... and via bypass (no sanitizer) → UNSANITIZED
//	src ─CALLS→ sink_direct                          # direct call, no intermediate → UNSANITIZED
func setupSanitizerGraph(t *testing.T) (*store.Store, testIDs) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	mk := func(name, role string) *store.Node {
		props := map[string]any{}
		if role != "" {
			props["security_role"] = role
		}
		return &store.Node{
			Project: "test", Label: "Function", Name: name,
			QualifiedName: "test." + name, FilePath: "src/" + name + ".go",
			Properties: props,
		}
	}
	nodes := []*store.Node{
		mk("src", "input_entry_point"),
		mk("san1", "sanitizer"),
		mk("mid", ""),
		mk("bypass", ""),
		mk("sink1", "sensitive_sink"),
		mk("sink2", "sensitive_sink"),
		mk("sink3", "sensitive_sink"),
		mk("sink_direct", "sensitive_sink"),
	}
	ids := make(testIDs)
	for _, n := range nodes {
		id, upErr := st.UpsertNode(n)
		if upErr != nil {
			t.Fatalf("UpsertNode %s: %v", n.Name, upErr)
		}
		ids[n.Name] = id
	}

	edges := [][2]string{
		{"src", "san1"}, {"src", "mid"}, {"src", "bypass"}, {"src", "sink_direct"},
		{"san1", "sink2"}, {"san1", "sink3"},
		{"mid", "sink1"},
		{"bypass", "sink3"},
	}
	for _, e := range edges {
		if _, insErr := st.InsertEdge(&store.Edge{
			Project: "test", SourceID: ids[e[0]], TargetID: ids[e[1]], Type: "CALLS",
		}); insErr != nil {
			t.Fatalf("InsertEdge %s->%s: %v", e[0], e[1], insErr)
		}
	}
	return st, ids
}

func TestPathSanitized(t *testing.T) {
	st, ids := setupSanitizerGraph(t)
	defer st.Close()

	edgeTypes := []string{"CALLS"}
	sanitizerIDs := map[int64]string{ids["san1"]: "san1"}

	cases := []struct {
		name          string
		sink          string
		wantSanitized bool
		wantBy        string // "" = don't care / expect empty
	}{
		// Regression pin for the sibling-branch false positive: san1 is reachable
		// from src at a shallower hop than sink1, but it is NOT on src->mid->sink1.
		// The old visited-set heuristic reported this SANITIZED; the sound check
		// must report UNSANITIZED.
		{"sibling sanitizer does not sanitize", "sink1", false, ""},
		// Every path to sink2 crosses san1 → sanitized, witness names san1.
		{"only path crosses sanitizer", "sink2", true, "san1"},
		// sink3 has a sanitized path (via san1) AND an unsanitized one (via bypass);
		// the existence of any sanitizer-free path makes the pair unsanitized.
		{"bypass path makes pair unsanitized", "sink3", false, ""},
		// Direct call: no intermediate node, so never sanitized.
		{"direct call is unsanitized", "sink_direct", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sanitized, by := pathSanitized(st, ids["src"], ids[tc.sink], edgeTypes, 4, sanitizerIDs)
			if sanitized != tc.wantSanitized {
				t.Errorf("pathSanitized(src->%s) sanitized=%v, want %v (by=%q)", tc.sink, sanitized, tc.wantSanitized, by)
			}
			if tc.wantSanitized && tc.wantBy != "" && by != tc.wantBy {
				t.Errorf("pathSanitized(src->%s) by=%q, want %q", tc.sink, by, tc.wantBy)
			}
			if !tc.wantSanitized && by != "" {
				t.Errorf("pathSanitized(src->%s) reported unsanitized but named sanitizer %q", tc.sink, by)
			}
		})
	}
}

func TestRefineSources(t *testing.T) {
	st, ids := setupSecurityGraph(t)
	defer st.Close()

	t.Run("main replaced by non-infra callees", func(t *testing.T) {
		mainNode, err := st.FindNodeByID(ids["main"])
		if err != nil {
			t.Fatalf("FindNodeByID(main): %v", err)
		}
		sources := []*store.Node{mainNode}

		refined := refineSources(st, sources)

		// main() calls init_logger (infra → filtered) and handle_request (kept)
		if len(refined) != 1 {
			t.Fatalf("expected 1 refined source, got %d", len(refined))
		}
		if refined[0].Name != "handle_request" {
			t.Errorf("expected handle_request as refined source, got %q", refined[0].Name)
		}
	})

	t.Run("non-main entry points kept as-is", func(t *testing.T) {
		handlerNode, err := st.FindNodeByID(ids["handle_request"])
		if err != nil {
			t.Fatalf("FindNodeByID(handle_request): %v", err)
		}
		sources := []*store.Node{handlerNode}

		refined := refineSources(st, sources)

		if len(refined) != 1 {
			t.Fatalf("expected 1 source, got %d", len(refined))
		}
		if refined[0].Name != "handle_request" {
			t.Errorf("expected handle_request, got %q", refined[0].Name)
		}
	})

	t.Run("deduplicates sources", func(t *testing.T) {
		mainNode, _ := st.FindNodeByID(ids["main"])
		handlerNode, _ := st.FindNodeByID(ids["handle_request"])
		// main refines to handle_request, which is also passed directly
		sources := []*store.Node{mainNode, handlerNode}

		refined := refineSources(st, sources)

		// Should deduplicate: handle_request appears once
		names := make(map[string]bool)
		for _, n := range refined {
			if names[n.Name] {
				t.Errorf("duplicate refined source: %s", n.Name)
			}
			names[n.Name] = true
		}
	})
}

func TestHandleSurfacesQuery(t *testing.T) {
	st, _ := setupSecurityGraph(t)
	defer st.Close()

	projName := "test"
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	t.Run("query all roles returns multiple categories", func(t *testing.T) {
		// Query surfaces for all roles by passing no role filter
		roles := []string{"auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation", "audit_logging", "sanitizer"}
		totalCount := 0
		results := make(map[string]int)

		for _, role := range roles {
			nodes, err := st.FindNodesByProperty(projName, "", "security_role", role)
			if err != nil {
				continue
			}
			if len(nodes) > 0 {
				results[role] = len(nodes)
				totalCount += len(nodes)
			}
		}

		if results["auth_boundary"] != 1 {
			t.Errorf("expected 1 auth_boundary, got %d", results["auth_boundary"])
		}
		if results["input_entry_point"] < 2 {
			t.Errorf("expected ≥2 input_entry_points (handle_request + main), got %d", results["input_entry_point"])
		}
		if results["sensitive_sink"] != 2 {
			t.Errorf("expected 2 sensitive_sinks (get_user + execute_sql), got %d", results["sensitive_sink"])
		}
		if results["sanitizer"] != 1 {
			t.Errorf("expected 1 sanitizer (validate_jwt), got %d", results["sanitizer"])
		}
	})

	t.Run("filter by single role", func(t *testing.T) {
		nodes, err := st.FindNodesByProperty(projName, "", "security_role", "crypto_operation")
		if err != nil {
			t.Fatalf("FindNodesByProperty: %v", err)
		}
		if len(nodes) != 1 {
			t.Fatalf("expected 1 crypto_operation, got %d", len(nodes))
		}
		if nodes[0].Name != "parse_config" {
			t.Errorf("expected parse_config, got %q", nodes[0].Name)
		}
		subtype, _ := nodes[0].Properties["security_subtype"].(string)
		if subtype != "encryption" {
			t.Errorf("expected subtype 'encryption', got %q", subtype)
		}
	})
}

func TestTaintedPathsBFS(t *testing.T) {
	st, ids := setupSecurityGraph(t)
	defer st.Close()

	t.Run("finds path from entry point to sink", func(t *testing.T) {
		// BFS from handle_request should reach get_user and execute_sql (sinks)
		edgeTypes := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}
		result, err := st.BFS(ids["handle_request"], "outbound", edgeTypes, 4, 500)
		if err != nil {
			t.Fatalf("BFS: %v", err)
		}

		sinkNames := make(map[string]bool)
		for _, nh := range result.Visited {
			if role, ok := nh.Node.Properties["security_role"]; ok {
				if role == "sensitive_sink" {
					sinkNames[nh.Node.Name] = true
				}
			}
		}

		if !sinkNames["get_user"] {
			t.Error("BFS should reach get_user sink")
		}
		if !sinkNames["execute_sql"] {
			t.Error("BFS should reach execute_sql sink")
		}
	})

	edgeTypes := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}

	t.Run("get_user sanitized when authenticate is on the only path", func(t *testing.T) {
		// The only path is handle_request -> authenticate -> get_user, and
		// authenticate is in the sanitizer set, so every path crosses it.
		sanitizerIDs := map[int64]string{
			ids["validate_jwt"]: "validate_jwt",
			ids["authenticate"]: "authenticate",
		}
		sanitized, by := pathSanitized(st, ids["handle_request"], ids["get_user"], edgeTypes, 4, sanitizerIDs)
		if !sanitized {
			t.Error("path to get_user should be sanitized (authenticate is on the only path)")
		}
		if by != "authenticate" {
			t.Errorf("expected witness sanitizer 'authenticate', got %q", by)
		}
	})

	t.Run("get_user NOT sanitized by a sibling-branch sanitizer", func(t *testing.T) {
		// validate_jwt is authenticate's sibling of get_user (authenticate calls
		// BOTH validate_jwt and get_user). It is reachable from the source but is
		// NOT on the handle_request -> authenticate -> get_user path. The sound
		// check must report unsanitized; the old visited-set heuristic was wrong.
		sanitizerIDs := map[int64]string{ids["validate_jwt"]: "validate_jwt"}
		sanitized, by := pathSanitized(st, ids["handle_request"], ids["get_user"], edgeTypes, 4, sanitizerIDs)
		if sanitized {
			t.Errorf("get_user must NOT be sanitized by sibling-branch validate_jwt (by=%q)", by)
		}
	})

	t.Run("direct-call sink is unsanitized", func(t *testing.T) {
		// handle_request -> execute_sql is a direct call; no intermediate node,
		// so no sanitizer can be on the path.
		sanitizerIDs := map[int64]string{ids["authenticate"]: "authenticate"}
		sanitized, _ := pathSanitized(st, ids["handle_request"], ids["execute_sql"], edgeTypes, 4, sanitizerIDs)
		if sanitized {
			t.Error("direct call handle_request->execute_sql should be unsanitized")
		}
	})
}
