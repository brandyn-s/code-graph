package pipeline

import "testing"

// TestTagIndirectDispatch verifies the post-resolve edge annotation:
// executor_submit / depends get confidence=high, getattr gets medium,
// unknown kinds default to medium (defensive), empty kind is a no-op.
// This is the single wiring point between cbm.Call.DispatchKind and
// the resolvedEdge.Properties that downstream stores see.
func TestTagIndirectDispatch(t *testing.T) {
	cases := []struct {
		name         string
		kind         string
		wantTagged   bool
		wantKind     string
		wantConfidence string
	}{
		{"empty: no tag", "", false, "", ""},
		{"executor_submit: high", "executor_submit", true, "executor_submit", "high"},
		{"depends: high", "depends", true, "depends", "high"},
		{"getattr: medium", "getattr", true, "getattr", "medium"},
		{"unknown: medium fallback", "fn_pointer_v0_5_future", true, "fn_pointer_v0_5_future", "medium"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &resolvedEdge{Type: "CALLS"}
			tagIndirectDispatch(e, c.kind)
			if !c.wantTagged {
				if e.Properties != nil {
					t.Errorf("expected no properties for empty kind, got %v", e.Properties)
				}
				return
			}
			if e.Properties == nil {
				t.Fatalf("expected properties to be set for kind=%q", c.kind)
			}
			if got := e.Properties["dispatch_kind"]; got != c.wantKind {
				t.Errorf("dispatch_kind: got %v, want %q", got, c.wantKind)
			}
			if got := e.Properties["confidence"]; got != c.wantConfidence {
				t.Errorf("confidence: got %v, want %q", got, c.wantConfidence)
			}
		})
	}
}

// TestTagIndirectDispatchPreservesExistingProps confirms the helper
// merges into existing edge properties rather than overwriting them.
// resolveCallEdge already populates resolver_rule, caller_node_kind,
// etc.; the dispatch tag must coexist.
func TestTagIndirectDispatchPreservesExistingProps(t *testing.T) {
	e := &resolvedEdge{
		Type: "CALLS",
		Properties: map[string]any{
			"resolver_rule":    "same-package-shadow",
			"caller_node_kind": "Function",
		},
	}
	tagIndirectDispatch(e, "executor_submit")
	if e.Properties["resolver_rule"] != "same-package-shadow" {
		t.Errorf("resolver_rule was clobbered: %v", e.Properties)
	}
	if e.Properties["caller_node_kind"] != "Function" {
		t.Errorf("caller_node_kind was clobbered: %v", e.Properties)
	}
	if e.Properties["dispatch_kind"] != "executor_submit" {
		t.Errorf("dispatch_kind not set: %v", e.Properties)
	}
}
