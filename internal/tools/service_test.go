package tools

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// --- Pure function tests ---

func TestClassifyDomain(t *testing.T) {
	tests := []struct {
		service string
		want    string
	}{
		{"controlsd", "autonomy"},
		{"trackerd", "perception"},
		{"apid", "communications"},
		{"doomper", "recording"},
		{"sysmanager", "management"},
		{"ship-os", "ui"},
		{"libtracker", "perception"},
		{"libfoo", "library"}, // lib prefix fallback
		{"unknown-service", "other"},
		{"", "other"},
	}

	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			got := classifyDomain(tt.service)
			if got != tt.want {
				t.Errorf("classifyDomain(%q) = %q, want %q", tt.service, got, tt.want)
			}
		})
	}
}

func TestEdgeConfidence(t *testing.T) {
	t.Run("nil properties returns 1.0", func(t *testing.T) {
		e := &store.Edge{Properties: nil}
		if c := edgeConfidence(e); c != 1.0 {
			t.Errorf("expected 1.0, got %f", c)
		}
	})

	t.Run("with confidence property", func(t *testing.T) {
		e := &store.Edge{Properties: map[string]any{"confidence": 0.75}}
		if c := edgeConfidence(e); c != 0.75 {
			t.Errorf("expected 0.75, got %f", c)
		}
	})

	t.Run("without confidence property", func(t *testing.T) {
		e := &store.Edge{Properties: map[string]any{"other": "value"}}
		if c := edgeConfidence(e); c != 1.0 {
			t.Errorf("expected 1.0, got %f", c)
		}
	})
}

func TestCommonMethodNames(t *testing.T) {
	t.Run("common names are blocked", func(t *testing.T) {
		blocked := []string{"ok", "map", "into", "new", "clone", "unwrap", "init", "send"}
		for _, name := range blocked {
			if !commonMethodNames[name] {
				t.Errorf("expected %q to be in commonMethodNames", name)
			}
		}
	})

	t.Run("real function names are not blocked", func(t *testing.T) {
		allowed := []string{"handle_request", "process_order", "authenticate", "validate_jwt"}
		for _, name := range allowed {
			if commonMethodNames[name] {
				t.Errorf("expected %q to NOT be in commonMethodNames", name)
			}
		}
	})
}

// --- Graph-dependent tests ---

func TestExplainServiceFindsComponents(t *testing.T) {
	st, ids := setupSecurityGraph(t)
	defer st.Close()

	projName := "test"
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	t.Run("finds nodes by service prefix", func(t *testing.T) {
		allNodes, err := st.AllNodes(projName)
		if err != nil {
			t.Fatalf("AllNodes: %v", err)
		}

		// svc-api/ prefix should match handle_request, authenticate, validate_jwt, etc.
		var serviceNodes []*store.Node
		for _, n := range allNodes {
			if len(n.FilePath) > 0 && n.FilePath[:7] == "svc-api" {
				serviceNodes = append(serviceNodes, n)
			}
		}

		if len(serviceNodes) < 5 {
			t.Errorf("expected ≥5 nodes in svc-api/, got %d", len(serviceNodes))
		}
	})

	t.Run("finds env vars for service", func(t *testing.T) {
		// get_user reads DATABASE_URL
		edges, err := st.FindEdgesBySourceAndType(ids["get_user"], "READS_ENV")
		if err != nil {
			t.Fatalf("FindEdgesBySourceAndType: %v", err)
		}
		if len(edges) == 0 {
			t.Error("expected READS_ENV edges from get_user")
		}
		envNode, err := st.FindNodeByID(edges[0].TargetID)
		if err != nil || envNode == nil {
			t.Fatal("expected env var node")
		}
		if envNode.Name != "DATABASE_URL" {
			t.Errorf("expected DATABASE_URL, got %q", envNode.Name)
		}
	})

	t.Run("cross-service deps detected", func(t *testing.T) {
		// process_order (svc-orders) -> send_notification (svc-notify) via HTTP_CALLS
		edges, err := st.FindEdgesBySourceAndType(ids["process_order"], "HTTP_CALLS")
		if err != nil {
			t.Fatalf("FindEdgesBySourceAndType: %v", err)
		}
		if len(edges) == 0 {
			t.Error("expected HTTP_CALLS edge from process_order to send_notification")
		}
	})
}

func TestServiceMapDomainGrouping(t *testing.T) {
	st, _ := setupSecurityGraph(t)
	defer st.Close()

	projName := "test"
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	// Get all unique top-level directories
	allNodes, _ := st.AllNodes(projName)
	services := make(map[string]bool)
	for _, n := range allNodes {
		crate := extractTopLevelCrate(n.FilePath)
		if crate != "" {
			services[crate] = true
		}
	}

	t.Run("discovers multiple services", func(t *testing.T) {
		// We have svc-api, svc-orders, svc-notify in the test graph
		if len(services) < 3 {
			t.Errorf("expected ≥3 services, got %d: %v", len(services), services)
		}
	})

	t.Run("all services get a domain classification", func(t *testing.T) {
		for svc := range services {
			domain := classifyDomain(svc)
			if domain == "" {
				t.Errorf("service %q got empty domain", svc)
			}
			// Our test services aren't in domainPatterns, so they should be "other"
			if domain != "other" {
				t.Errorf("test service %q classified as %q, expected 'other'", svc, domain)
			}
		}
	})
}
