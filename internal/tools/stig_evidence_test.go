package tools

import (
	"sort"
	"testing"
)

// --- Pure function/data tests ---

func TestStigToRolesMapping(t *testing.T) {
	t.Run("AC-3 maps to auth_boundary", func(t *testing.T) {
		roles := stigToRoles["AC-3"]
		if len(roles) == 0 {
			t.Fatal("AC-3 should have roles")
		}
		if roles[0] != "auth_boundary" {
			t.Errorf("AC-3 should map to auth_boundary, got %v", roles)
		}
	})

	t.Run("SI-10 maps to entry points and sinks", func(t *testing.T) {
		roles := stigToRoles["SI-10"]
		if len(roles) != 2 {
			t.Fatalf("SI-10 should have 2 roles, got %d", len(roles))
		}
		hasEntry, hasSink := false, false
		for _, r := range roles {
			if r == "input_entry_point" {
				hasEntry = true
			}
			if r == "sensitive_sink" {
				hasSink = true
			}
		}
		if !hasEntry || !hasSink {
			t.Errorf("SI-10 should map to input_entry_point + sensitive_sink, got %v", roles)
		}
	})

	t.Run("SC-13 maps to crypto_operation", func(t *testing.T) {
		roles := stigToRoles["SC-13"]
		if len(roles) != 1 || roles[0] != "crypto_operation" {
			t.Errorf("SC-13 should map to [crypto_operation], got %v", roles)
		}
	})

	t.Run("all controls have at least one role", func(t *testing.T) {
		for control, roles := range stigToRoles {
			if len(roles) == 0 {
				t.Errorf("control %s has empty roles list", control)
			}
		}
	})
}

func TestStigPrefixMatching(t *testing.T) {
	// Simulate the prefix matching logic from handleSTIGEvidence
	lookup := func(controlID string) []string {
		roles := stigToRoles[controlID]
		if roles == nil {
			for key, val := range stigToRoles {
				if len(key) > 0 && len(controlID) > 0 {
					if hasPrefix(key, controlID) || hasPrefix(controlID, key) {
						roles = append(roles, val...)
					}
				}
			}
		}
		// Dedup
		if len(roles) > 0 {
			seen := make(map[string]bool)
			unique := make([]string, 0, len(roles))
			for _, r := range roles {
				if !seen[r] {
					seen[r] = true
					unique = append(unique, r)
				}
			}
			roles = unique
		}
		return roles
	}

	t.Run("exact match works", func(t *testing.T) {
		roles := lookup("AC-3")
		if len(roles) == 0 {
			t.Error("AC-3 exact match should return roles")
		}
	})

	t.Run("prefix AC-3(4) matches AC-3 entry", func(t *testing.T) {
		// "AC-3(4)" has prefix "AC-3" which is a key in stigToRoles
		roles := lookup("AC-3(4)")
		if len(roles) == 0 {
			t.Error("AC-3(4) should match AC-3 via prefix")
		}
	})

	t.Run("unknown control returns nil", func(t *testing.T) {
		roles := lookup("ZZ-99")
		if len(roles) != 0 {
			t.Errorf("ZZ-99 should return no roles, got %v", roles)
		}
	})
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// --- Graph-dependent tests ---

func TestStigEvidenceFromGraph(t *testing.T) {
	st, _ := setupSecurityGraph(t)
	defer st.Close()

	projName := "test"
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	t.Run("AC-3 finds auth_boundary nodes", func(t *testing.T) {
		roles := stigToRoles["AC-3"]
		for _, role := range roles {
			nodes, err := st.FindNodesByProperty(projName, "", "security_role", role)
			if err != nil {
				t.Fatalf("FindNodesByProperty(%s): %v", role, err)
			}
			if len(nodes) == 0 {
				t.Errorf("expected auth_boundary nodes for AC-3, got 0")
			}
			// Verify the found node is actually authenticate
			found := false
			for _, n := range nodes {
				if n.Name == "authenticate" {
					found = true
				}
			}
			if !found {
				t.Error("expected to find 'authenticate' as auth_boundary evidence")
			}
		}
	})

	t.Run("SC-13 finds crypto_operation nodes", func(t *testing.T) {
		roles := stigToRoles["SC-13"]
		for _, role := range roles {
			nodes, err := st.FindNodesByProperty(projName, "", "security_role", role)
			if err != nil {
				t.Fatalf("FindNodesByProperty(%s): %v", role, err)
			}
			if len(nodes) == 0 {
				t.Errorf("expected crypto_operation nodes for SC-13, got 0")
			}
			found := false
			for _, n := range nodes {
				if n.Name == "parse_config" {
					found = true
				}
			}
			if !found {
				t.Error("expected to find 'parse_config' as crypto_operation evidence")
			}
		}
	})

	t.Run("supported controls list is complete", func(t *testing.T) {
		supported := make([]string, 0, len(stigToRoles))
		for k := range stigToRoles {
			supported = append(supported, k)
		}
		sort.Strings(supported)

		expectedControls := []string{"AC-3", "AC-6", "AU-2", "AU-3", "IA-2", "IA-5", "SC-13", "SC-23", "SC-8", "SI-10", "SI-11"}
		sort.Strings(expectedControls)

		if len(supported) != len(expectedControls) {
			t.Errorf("expected %d controls, got %d", len(expectedControls), len(supported))
		}
		for i, c := range expectedControls {
			if i < len(supported) && supported[i] != c {
				t.Errorf("expected control %s at position %d, got %s", c, i, supported[i])
			}
		}
	})
}
