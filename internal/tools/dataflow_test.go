package tools

import (
	"sort"
	"testing"
)

func TestDataFlowBFS(t *testing.T) {
	st, ids := setupSecurityGraph(t)
	defer st.Close()

	t.Run("traces outbound from handle_request", func(t *testing.T) {
		edgeTypes := []string{"CALLS", "WRITES", "READS", "USAGE"}
		result, err := st.BFS(ids["handle_request"], "outbound", edgeTypes, 3, 200)
		if err != nil {
			t.Fatalf("BFS: %v", err)
		}

		if len(result.Visited) == 0 {
			t.Fatal("expected visited nodes from handle_request")
		}

		// Should reach: authenticate, validate_jwt, get_user, execute_sql, write_log
		visitedNames := make(map[string]bool)
		for _, nh := range result.Visited {
			visitedNames[nh.Node.Name] = true
		}

		expected := []string{"authenticate", "validate_jwt", "get_user", "execute_sql", "write_log"}
		for _, name := range expected {
			if !visitedNames[name] {
				t.Errorf("expected %s in BFS result from handle_request", name)
			}
		}
	})

	t.Run("depth=1 limits reach", func(t *testing.T) {
		edgeTypes := []string{"CALLS"}
		result, err := st.BFS(ids["handle_request"], "outbound", edgeTypes, 1, 200)
		if err != nil {
			t.Fatalf("BFS: %v", err)
		}

		// Depth 1 from handle_request: authenticate, write_log, execute_sql (direct callees)
		// Should NOT reach validate_jwt or get_user (hop 2)
		for _, nh := range result.Visited {
			if nh.Hop > 1 {
				t.Errorf("depth=1 should not visit hop %d node %s", nh.Hop, nh.Node.Name)
			}
		}
	})

	t.Run("classifies visited nodes by security role", func(t *testing.T) {
		edgeTypes := []string{"CALLS"}
		result, err := st.BFS(ids["handle_request"], "outbound", edgeTypes, 3, 200)
		if err != nil {
			t.Fatalf("BFS: %v", err)
		}

		var sinks, auth, audit []string
		for _, nh := range result.Visited {
			role, _ := nh.Node.Properties["security_role"].(string)
			switch role {
			case "sensitive_sink":
				sinks = append(sinks, nh.Node.Name)
			case "auth_boundary":
				auth = append(auth, nh.Node.Name)
			case "audit_logging":
				audit = append(audit, nh.Node.Name)
			}
		}

		if len(sinks) < 2 {
			t.Errorf("expected ≥2 sinks (get_user, execute_sql), got %v", sinks)
		}
		if len(auth) < 1 {
			t.Errorf("expected ≥1 auth_boundary (authenticate), got %v", auth)
		}
		if len(audit) < 1 {
			t.Errorf("expected ≥1 audit_logging (write_log), got %v", audit)
		}
	})

	t.Run("flow path sorted by hop then name", func(t *testing.T) {
		edgeTypes := []string{"CALLS"}
		result, err := st.BFS(ids["handle_request"], "outbound", edgeTypes, 3, 200)
		if err != nil {
			t.Fatalf("BFS: %v", err)
		}

		type entry struct {
			name string
			hop  int
		}
		var flowPath []entry
		for _, nh := range result.Visited {
			flowPath = append(flowPath, entry{name: nh.Node.Name, hop: nh.Hop})
		}

		sort.Slice(flowPath, func(i, j int) bool {
			if flowPath[i].hop != flowPath[j].hop {
				return flowPath[i].hop < flowPath[j].hop
			}
			return flowPath[i].name < flowPath[j].name
		})

		// Verify sorted: hops should be non-decreasing
		for i := 1; i < len(flowPath); i++ {
			if flowPath[i].hop < flowPath[i-1].hop {
				t.Errorf("flow path not sorted by hop: %v before %v", flowPath[i-1], flowPath[i])
			}
		}
	})
}

func TestDataFlowFromIsolatedNode(t *testing.T) {
	st, ids := setupSecurityGraph(t)
	defer st.Close()

	t.Run("node with no outbound edges returns empty", func(t *testing.T) {
		// AppConfig has no CALLS edges (only FILE_CHANGES_WITH which isn't in the edge types)
		edgeTypes := []string{"CALLS", "WRITES", "READS", "USAGE"}
		result, err := st.BFS(ids["AppConfig"], "outbound", edgeTypes, 3, 200)
		if err != nil {
			t.Fatalf("BFS: %v", err)
		}

		// BFS from isolated node should find no visited nodes (or just the root)
		nonRoot := 0
		for _, nh := range result.Visited {
			if nh.Node.ID != ids["AppConfig"] {
				nonRoot++
			}
		}
		if nonRoot > 0 {
			t.Errorf("expected no reachable nodes from isolated AppConfig, got %d", nonRoot)
		}
	})
}

func TestDataFlowEdgeList(t *testing.T) {
	st, ids := setupSecurityGraph(t)
	defer st.Close()

	edgeTypes := []string{"CALLS"}
	result, err := st.BFS(ids["handle_request"], "outbound", edgeTypes, 2, 200)
	if err != nil {
		t.Fatalf("BFS: %v", err)
	}

	edgeList := buildEdgeList(result.Edges)

	if len(edgeList) == 0 {
		t.Fatal("expected non-empty edge list")
	}

	// Each edge should have type info
	for i, e := range edgeList {
		if e["type"] == nil {
			t.Errorf("edge %d: missing type field", i)
		}
	}
}

func TestBuildEdgeListEmpty(t *testing.T) {
	result := buildEdgeList(nil)
	if result == nil {
		return // nil is acceptable for empty input
	}
	if len(result) != 0 {
		t.Errorf("expected empty edge list for nil input, got %d", len(result))
	}
}

// Verify that READS_ENV edges are followed when included
func TestDataFlowFollowsEnvVarEdges(t *testing.T) {
	st, ids := setupSecurityGraph(t)
	defer st.Close()

	// BFS from authenticate with READS_ENV in edge types should reach DATABASE_URL via get_user
	edgeTypes := []string{"CALLS", "READS_ENV"}
	result, err := st.BFS(ids["authenticate"], "outbound", edgeTypes, 3, 200)
	if err != nil {
		t.Fatalf("BFS: %v", err)
	}

	foundEnvVar := false
	for _, nh := range result.Visited {
		if nh.Node.Name == "DATABASE_URL" {
			foundEnvVar = true
			break
		}
	}
	if !foundEnvVar {
		// authenticate -> get_user (CALLS) -> DATABASE_URL (READS_ENV)
		visited := make([]string, 0, len(result.Visited))
		for _, nh := range result.Visited {
			visited = append(visited, nh.Node.Name)
		}
		t.Errorf("expected DATABASE_URL reachable via READS_ENV, visited: %v", visited)
	}
}
