package tools

import (
	"testing"
)

func TestHealthIndexCoverage(t *testing.T) {
	st, _ := setupSecurityGraph(t)
	defer st.Close()

	projName := "test"
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	t.Run("counts nodes and edges", func(t *testing.T) {
		allNodes, err := st.AllNodes(projName)
		if err != nil {
			t.Fatalf("AllNodes: %v", err)
		}
		// We inserted 14 nodes in setupSecurityGraph
		if len(allNodes) < 14 {
			t.Errorf("expected ≥14 nodes, got %d", len(allNodes))
		}
	})

	t.Run("unique files in index", func(t *testing.T) {
		allNodes, _ := st.AllNodes(projName)
		files := make(map[string]bool)
		for _, n := range allNodes {
			if n.FilePath != "" {
				files[n.FilePath] = true
			}
		}
		// We have nodes across multiple files: handler.rs, auth.rs, db.rs, logging.rs,
		// main.rs, config.rs, auth_test.rs, svc-orders/handler.rs, svc-notify/sender.rs
		if len(files) < 8 {
			t.Errorf("expected ≥8 unique files, got %d", len(files))
		}
	})

	t.Run("edge type distribution", func(t *testing.T) {
		edgeTypes := []string{"CALLS", "TESTS", "READS_ENV", "HTTP_CALLS", "FILE_CHANGES_WITH"}
		for _, et := range edgeTypes {
			edges, err := st.FindEdgesByType(projName, et)
			if err != nil {
				t.Errorf("FindEdgesByType(%s): %v", et, err)
				continue
			}
			if len(edges) == 0 {
				t.Errorf("expected ≥1 %s edge, got 0", et)
			}
		}
	})

	t.Run("security-tagged node count", func(t *testing.T) {
		roles := []string{"auth_boundary", "input_entry_point", "sensitive_sink",
			"crypto_operation", "audit_logging", "sanitizer"}
		total := 0
		for _, role := range roles {
			nodes, _ := st.FindNodesByProperty(projName, "", "security_role", role)
			total += len(nodes)
		}
		// We tagged: handle_request, authenticate, validate_jwt, get_user, execute_sql,
		// write_log, main, parse_config, process_order = 9 security-tagged nodes
		if total < 9 {
			t.Errorf("expected ≥9 security-tagged nodes, got %d", total)
		}
	})
}
