package pipeline

import "testing"

// TestTagIndirectDispatch verifies the post-resolve edge annotation.
// confidence is NUMERIC ([0,1]) — required by the SQL scan path in
// internal/store/traverse.go::BFS which reads json_extract into a
// float64 column. confidence_band is the human-readable string.
// Storing the band STRING in `confidence` (the v0.1 shape, fixed in
// v0.3 alongside Pattern A) broke trace_call_path's BFS the moment
// an indirect-dispatch edge was walked.
//
// executor_submit / depends / Flask hooks all map to (0.9,"high"),
// getattr to (0.6,"medium"), unknown kinds default to medium, empty
// kind is a no-op.
func TestTagIndirectDispatch(t *testing.T) {
	cases := []struct {
		name           string
		kind           string
		wantTagged     bool
		wantKind       string
		wantConfidence float64
		wantBand       string
	}{
		{"empty: no tag", "", false, "", 0, ""},
		{"executor_submit: high", "executor_submit", true, "executor_submit", 0.9, "high"},
		{"depends: high", "depends", true, "depends", 0.9, "high"},
		{"getattr: medium", "getattr", true, "getattr", 0.6, "medium"},
		// v0.3 Pattern A — Flask hook-registrar family. All "high"
		// because the registered function is a deterministic Name
		// reference at the registration call site.
		{"before_request_hook: high", "before_request_hook", true, "before_request_hook", 0.9, "high"},
		{"after_request_hook: high", "after_request_hook", true, "after_request_hook", 0.9, "high"},
		{"teardown_request_hook: high", "teardown_request_hook", true, "teardown_request_hook", 0.9, "high"},
		{"teardown_appcontext_hook: high", "teardown_appcontext_hook", true, "teardown_appcontext_hook", 0.9, "high"},
		{"errorhandler_hook: high", "errorhandler_hook", true, "errorhandler_hook", 0.9, "high"},
		{"context_processor_hook: high", "context_processor_hook", true, "context_processor_hook", 0.9, "high"},
		{"before_first_request_hook: high", "before_first_request_hook", true, "before_first_request_hook", 0.9, "high"},
		{"unknown: medium fallback", "fn_pointer_v0_5_future", true, "fn_pointer_v0_5_future", 0.6, "medium"},
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
			if got, ok := e.Properties["confidence"].(float64); !ok || got != c.wantConfidence {
				t.Errorf("confidence: got %v (type %T), want %v (float64)",
					e.Properties["confidence"], e.Properties["confidence"], c.wantConfidence)
			}
			if got := e.Properties["confidence_band"]; got != c.wantBand {
				t.Errorf("confidence_band: got %v, want %q", got, c.wantBand)
			}
		})
	}
}

// TestTagIndirectDispatchBandMatchesConfidence pins the round-trip
// invariant: dispatchKindBand(kind) == confidenceBand(dispatchKindConfidence(kind)).
// If the resolver's confidenceBand thresholds (resolver.go:1282) ever
// shift, this test catches the desync before it lands a wrong band
// string on every synthesized edge.
func TestTagIndirectDispatchBandMatchesConfidence(t *testing.T) {
	for _, kind := range []string{
		"executor_submit", "depends", "getattr",
		"before_request_hook", "after_request_hook",
		"teardown_request_hook", "teardown_appcontext_hook",
		"errorhandler_hook", "context_processor_hook",
		"before_first_request_hook",
		"fn_pointer_v0_5_future",
	} {
		want := confidenceBand(dispatchKindConfidence(kind))
		if got := dispatchKindBand(kind); got != want {
			t.Errorf("kind=%q: dispatchKindBand=%q, want %q (derived from confidence=%v)",
				kind, got, want, dispatchKindConfidence(kind))
		}
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
