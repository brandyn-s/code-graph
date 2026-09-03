package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

// --- Pure function tests ---

func TestClassifyDomain(t *testing.T) {
	patterns := compileDomainPatterns(defaultDomainPatterns)
	tests := []struct {
		service string
		want    string
	}{
		{"controlsd", "service"},      // *d
		{"orders-service", "service"}, // *-service
		{"kubectl", "tooling"},        // *ctl
		{"terraform", "infrastructure"},
		{"test-runner", "testing"}, // test*
		{"web-frontend", "ui"},     // web-* and *-frontend tie on length → alphabetical domain (both ui)
		{"libtracker", "library"},  // lib*
		{"libfoo", "library"},
		{"unknown-thing", "other"},
		{"", "other"},
		{"D", "other"}, // suffix "*d" must not match the bare suffix itself
	}

	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			got := classifyDomainWith(patterns, tt.service)
			if got != tt.want {
				t.Errorf("classifyDomain(%q) = %q, want %q", tt.service, got, tt.want)
			}
		})
	}
}

func TestClassifyDomain_LongestPatternWins(t *testing.T) {
	patterns := compileDomainPatterns(map[string][]string{
		"service":  {"*d"},
		"payments": {"billingd", "billing-*"},
	})
	if got := classifyDomainWith(patterns, "billingd"); got != "payments" {
		t.Fatalf("billingd → %q, want payments (exact/longer pattern beats *d)", got)
	}
	if got := classifyDomainWith(patterns, "billing-api"); got != "payments" {
		t.Fatalf("billing-api → %q, want payments", got)
	}
	if got := classifyDomainWith(patterns, "authd"); got != "service" {
		t.Fatalf("authd → %q, want service", got)
	}
}

func TestLoadDomainPatterns_FromEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service_map.json")
	if err := os.WriteFile(path, []byte(`{"navigation": ["nav*", "*-gps"], "fleet": ["fleetd"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == ServiceMapEnv {
			return path
		}
		return ""
	}
	patterns := loadDomainPatterns(getenv)
	if got := classifyDomainWith(patterns, "navd"); got != "navigation" {
		t.Fatalf("navd → %q, want navigation", got)
	}
	if got := classifyDomainWith(patterns, "boat-gps"); got != "navigation" {
		t.Fatalf("boat-gps → %q, want navigation", got)
	}
	if got := classifyDomainWith(patterns, "fleetd"); got != "fleet" {
		t.Fatalf("fleetd → %q, want fleet", got)
	}
	// Custom table replaces the default entirely: *d no longer means service.
	if got := classifyDomainWith(patterns, "controlsd"); got != "other" {
		t.Fatalf("controlsd → %q, want other under custom table", got)
	}
}

func TestLoadDomainPatterns_InvalidFileFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service_map.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == ServiceMapEnv {
			return path
		}
		return ""
	}
	patterns := loadDomainPatterns(getenv)
	if got := classifyDomainWith(patterns, "controlsd"); got != "service" {
		t.Fatalf("controlsd → %q, want default 'service' after invalid config", got)
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
			// Classification must be deterministic across calls.
			if again := classifyDomain(svc); again != domain {
				t.Errorf("service %q classified as %q then %q", svc, domain, again)
			}
		}
	})
}
