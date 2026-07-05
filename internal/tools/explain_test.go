package tools

import (
	"testing"
)

// --- Pure function tests ---

func TestExtractPackageFromQN(t *testing.T) {
	tests := []struct {
		name    string
		qn      string
		project string
		want    string
	}{
		{"3+ segments extracts middle", "project.auth.authenticate", "project", "auth"},
		{"4 segments extracts middle two", "project.pkg.subpkg.func", "project", "pkg.subpkg"},
		{"2 segments returns empty", "project.func", "project", ""},
		{"1 segment returns empty", "func", "project", ""},
		{"5 segments extracts middle three", "project.a.b.c.func", "project", "a.b.c"},
		{"empty returns empty", "", "project", ""},
		// Symptom (e), observed live 2026-07-04: a project name that
		// contains a dot (home-dir path) must be stripped as a whole, not
		// split — the old code returned "schult-...pipeline" as the package.
		{"dotted project name", "Users-brandyn.schult-Documents-GitHub-code-graph.internal.pipeline.pipeline.Pipeline", "Users-brandyn.schult-Documents-GitHub-code-graph", "internal.pipeline.pipeline"},
		{"external non-project QN", "github.com/foo/bar.Server.AddTool", "project", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPackageFromQN(tt.qn, tt.project)
			if got != tt.want {
				t.Errorf("extractPackageFromQN(%q, %q) = %q, want %q", tt.qn, tt.project, got, tt.want)
			}
		})
	}
}

// --- Graph-dependent tests ---

func TestBuildExplanation(t *testing.T) {
	st, ids := setupSecurityGraph(t)
	defer st.Close()

	t.Run("authenticate has callers and callees and tests", func(t *testing.T) {
		authNode, err := st.FindNodeByID(ids["authenticate"])
		if err != nil {
			t.Fatalf("FindNodeByID: %v", err)
		}

		result := buildExplanation(st, authNode, "test")

		// Basic info
		if result["name"] != "authenticate" {
			t.Errorf("expected name 'authenticate', got %v", result["name"])
		}
		if result["label"] != "Function" {
			t.Errorf("expected label 'Function', got %v", result["label"])
		}

		// Callers: handle_request calls authenticate
		callers, ok := result["callers"].([]map[string]any)
		if !ok {
			t.Fatal("expected callers to be []map[string]any")
		}
		if len(callers) == 0 {
			t.Error("expected at least 1 caller (handle_request)")
		}
		foundCaller := false
		for _, c := range callers {
			if c["name"] == "handle_request" {
				foundCaller = true
			}
		}
		if !foundCaller {
			t.Error("expected handle_request as a caller")
		}

		// Callees: authenticate calls validate_jwt and get_user
		callees, ok := result["callees"].([]map[string]any)
		if !ok {
			t.Fatal("expected callees to be []map[string]any")
		}
		if len(callees) < 2 {
			t.Errorf("expected at least 2 callees (validate_jwt, get_user), got %d", len(callees))
		}

		// Tests: test_auth covers authenticate
		testCount, _ := result["test_count"].(int)
		if testCount == 0 {
			t.Error("expected test_count > 0 (test_auth covers authenticate)")
		}
		tests, ok := result["tests"].([]map[string]any)
		if !ok || len(tests) == 0 {
			t.Error("expected tests list with test_auth")
		}

		// Properties should include security_role
		props, ok := result["properties"].(map[string]any)
		if !ok {
			t.Fatal("expected properties map")
		}
		if props["security_role"] != "auth_boundary" {
			t.Errorf("expected security_role 'auth_boundary', got %v", props["security_role"])
		}
	})

	t.Run("get_user reads env vars", func(t *testing.T) {
		getUserNode, err := st.FindNodeByID(ids["get_user"])
		if err != nil {
			t.Fatalf("FindNodeByID: %v", err)
		}

		result := buildExplanation(st, getUserNode, "test")

		envVars, ok := result["env_vars"].([]string)
		if !ok || len(envVars) == 0 {
			t.Error("expected env_vars with DATABASE_URL")
		} else {
			found := false
			for _, v := range envVars {
				if v == "DATABASE_URL" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected DATABASE_URL in env_vars, got %v", envVars)
			}
		}
	})

	t.Run("unconnected node has no callers or callees", func(t *testing.T) {
		configNode, err := st.FindNodeByID(ids["AppConfig"])
		if err != nil {
			t.Fatalf("FindNodeByID: %v", err)
		}

		result := buildExplanation(st, configNode, "test")

		if _, ok := result["callers"]; ok {
			t.Error("AppConfig struct should have no callers")
		}
		if _, ok := result["callees"]; ok {
			t.Error("AppConfig struct should have no callees")
		}
		if result["test_count"] != 0 {
			t.Errorf("expected test_count 0, got %v", result["test_count"])
		}
	})

	t.Run("process_order has cross-service connections", func(t *testing.T) {
		orderNode, err := st.FindNodeByID(ids["process_order"])
		if err != nil {
			t.Fatalf("FindNodeByID: %v", err)
		}

		result := buildExplanation(st, orderNode, "test")

		crossService, ok := result["cross_service"].([]string)
		if !ok || len(crossService) == 0 {
			t.Error("expected cross_service connections for process_order -> send_notification")
		}
	})
}
