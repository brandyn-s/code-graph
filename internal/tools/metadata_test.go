package tools

import (
	"testing"
)

func TestMetadataBuilder_EmptyOmits(t *testing.T) {
	out := NewMetadataBuilder().Build()
	if len(out) != 0 {
		t.Fatalf("empty builder should produce empty map, got %v", out)
	}
}

func TestMetadataBuilder_FreshnessOnly(t *testing.T) {
	out := NewMetadataBuilder().
		WithFreshness("current", "2026-05-05T18:00:00Z").
		Build()

	freshness, ok := out["freshness"].(map[string]any)
	if !ok {
		t.Fatalf("expected freshness map, got %T (%v)", out["freshness"], out)
	}
	if freshness["state"] != "current" {
		t.Errorf("expected state=current, got %v", freshness["state"])
	}
	if freshness["indexed_at"] != "2026-05-05T18:00:00Z" {
		t.Errorf("expected indexed_at, got %v", freshness["indexed_at"])
	}
	if _, present := out["provenance"]; present {
		t.Errorf("provenance should be omitted when not set")
	}
	if _, present := out["confidence"]; present {
		t.Errorf("confidence should be omitted when not set")
	}
	if _, present := out["fallback_reason"]; present {
		t.Errorf("fallback_reason should be omitted when not set")
	}
}

func TestMetadataBuilder_AllFields(t *testing.T) {
	out := NewMetadataBuilder().
		WithFreshness("current", "2026-05-05T18:00:00Z").
		WithStaleness(120).
		WithProvenance("v1.2.3", "index").
		WithModel("claude-haiku-4-5").
		WithGrammarVersions(map[string]string{"python": "deadbeef", "go": "cafebabe"}).
		WithConfidence("high", "432 of 480 calls resolved").
		WithFallback("sonnet_reranker_timeout").
		Build()

	freshness := out["freshness"].(map[string]any)
	if freshness["staleness_seconds"].(int64) != 120 {
		t.Errorf("staleness mismatch: %v", freshness["staleness_seconds"])
	}

	provenance := out["provenance"].(map[string]any)
	if provenance["tool_version"] != "v1.2.3" || provenance["data_source"] != "index" {
		t.Errorf("provenance mismatch: %v", provenance)
	}
	if provenance["model"] != "claude-haiku-4-5" {
		t.Errorf("model mismatch: %v", provenance["model"])
	}
	grammars := provenance["grammar_versions"].(map[string]string)
	if grammars["python"] != "deadbeef" || grammars["go"] != "cafebabe" {
		t.Errorf("grammar versions mismatch: %v", grammars)
	}

	conf := out["confidence"].(map[string]any)
	if conf["band"] != "high" || conf["rationale"] != "432 of 480 calls resolved" {
		t.Errorf("confidence mismatch: %v", conf)
	}

	if out["fallback_reason"] != "sonnet_reranker_timeout" {
		t.Errorf("fallback mismatch: %v", out["fallback_reason"])
	}
}

func TestMetadataBuilder_FallbackEmptyOmits(t *testing.T) {
	// Empty fallback reason means "no fallback happened" — should NOT
	// surface as a key in the output.
	out := NewMetadataBuilder().
		WithProvenance("v1", "index").
		WithFallback("").
		Build()
	if _, present := out["fallback_reason"]; present {
		t.Errorf("empty fallback reason should be omitted, got %v", out["fallback_reason"])
	}
}

func TestMetadataBuilder_GrammarVersionsCopied(t *testing.T) {
	// The builder should defensively copy the input map so callers
	// can't mutate metadata after Build().
	src := map[string]string{"python": "abc"}
	b := NewMetadataBuilder().WithGrammarVersions(src)
	src["python"] = "MUTATED"
	out := b.Build()
	provenance := out["provenance"].(map[string]any)
	grammars := provenance["grammar_versions"].(map[string]string)
	if grammars["python"] != "abc" {
		t.Errorf("expected defensive copy; mutation leaked: %v", grammars["python"])
	}
}

func TestFreshnessFromProject_Nil(t *testing.T) {
	state, indexedAt := FreshnessFromProject(nil)
	if state != "unknown" || indexedAt != "" {
		t.Errorf("nil project should return ('unknown', ''), got (%q, %q)", state, indexedAt)
	}
}

func TestMetadataBuilder_ActionOutcome(t *testing.T) {
	out := NewMetadataBuilder().
		WithActionOutcome(ActionOutcomeDeleted).
		Build()

	if got := out["action_outcome"]; got != ActionOutcomeDeleted {
		t.Errorf("expected action_outcome=%q, got %v", ActionOutcomeDeleted, got)
	}
	// Other categories should be omitted when only outcome is set.
	if _, present := out["freshness"]; present {
		t.Errorf("freshness should be omitted when only action_outcome is set")
	}
	if _, present := out["provenance"]; present {
		t.Errorf("provenance should be omitted when only action_outcome is set")
	}
}

func TestMetadataBuilder_ActionOutcome_AllConstants(t *testing.T) {
	cases := []string{
		ActionOutcomeCreated,
		ActionOutcomeUpdated,
		ActionOutcomeDeleted,
		ActionOutcomeNoOp,
		ActionOutcomeFailed,
	}
	for _, outcome := range cases {
		out := NewMetadataBuilder().WithActionOutcome(outcome).Build()
		if out["action_outcome"] != outcome {
			t.Errorf("WithActionOutcome(%q): got %v", outcome, out["action_outcome"])
		}
	}
}

func TestMetadataBuilder_ActionOutcome_EmptyOmits(t *testing.T) {
	out := NewMetadataBuilder().WithActionOutcome("").Build()
	if _, present := out["action_outcome"]; present {
		t.Errorf("empty outcome should be omitted, got %v", out)
	}
}
