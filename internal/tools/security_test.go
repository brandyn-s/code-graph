package tools

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
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

func TestCheckPathSanitized(t *testing.T) {
	// Simulated BFS visited list: source(hop 0) -> sanitizer(hop 1) -> sink(hop 2)
	visited := []*store.NodeHop{
		{Node: &store.Node{ID: 1, Name: "source"}, Hop: 0},
		{Node: &store.Node{ID: 2, Name: "validate_input"}, Hop: 1},
		{Node: &store.Node{ID: 3, Name: "sink"}, Hop: 2},
	}
	sanitizerIDs := map[int64]string{2: "validate_input"}

	t.Run("sanitized path", func(t *testing.T) {
		sanitized, by := checkPathSanitized(visited, 2, 1, 3, sanitizerIDs)
		if !sanitized {
			t.Error("expected path to be sanitized")
		}
		if by != "validate_input" {
			t.Errorf("expected sanitized by 'validate_input', got %q", by)
		}
	})

	t.Run("direct call unsanitized", func(t *testing.T) {
		// hop=1 means direct call, no intermediate nodes possible
		sanitized, _ := checkPathSanitized(visited, 1, 1, 3, sanitizerIDs)
		if sanitized {
			t.Error("direct call (hop=1) should not be sanitized")
		}
	})

	t.Run("no sanitizer on path", func(t *testing.T) {
		emptySanitizers := map[int64]string{}
		sanitized, _ := checkPathSanitized(visited, 2, 1, 3, emptySanitizers)
		if sanitized {
			t.Error("expected unsanitized when no sanitizers defined")
		}
	})
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

	t.Run("sanitizer exists on authenticate->get_user path", func(t *testing.T) {
		// The path handle_request -> authenticate -> validate_jwt exists
		// and validate_jwt is a sanitizer. But authenticate -> get_user is
		// a sibling call, not through validate_jwt. The BFS visited list
		// includes validate_jwt at hop < sink hop, so checkPathSanitized
		// should detect it.
		edgeTypes := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}
		result, err := st.BFS(ids["handle_request"], "outbound", edgeTypes, 4, 500)
		if err != nil {
			t.Fatalf("BFS: %v", err)
		}

		// Build sanitizer ID set
		sanitizerIDs := map[int64]string{
			ids["validate_jwt"]: "validate_jwt",
			ids["authenticate"]: "authenticate",
		}

		// Find the get_user sink in visited
		for _, nh := range result.Visited {
			if nh.Node.ID == ids["get_user"] {
				sanitized, by := checkPathSanitized(result.Visited, nh.Hop, ids["handle_request"], nh.Node.ID, sanitizerIDs)
				if !sanitized {
					t.Error("path to get_user should be sanitized (authenticate is auth_boundary)")
				}
				if by == "" {
					t.Error("expected non-empty sanitizer name")
				}
				break
			}
		}
	})
}
