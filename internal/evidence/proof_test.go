package evidence

import (
	"strings"
	"testing"
)

func intPtr(value int) *int { return &value }

func proofObservation(path, qualifiedName, sourceEngine, derivation, stance, generation string) ObservationRef {
	symbol := NewSymbolRef(
		strings.Repeat("r", 64),
		strings.Repeat("s", 40),
		path,
		"function",
		qualifiedName,
		1,
		10,
	)
	evidence := NewEvidenceRef(
		strings.Repeat("r", 64),
		strings.Repeat("s", 40),
		generation,
		path,
		1,
		10,
		derivation,
		&symbol,
	)
	return NewObservationRef(
		evidence,
		stance,
		sourceEngine,
		derivation,
		"high",
	)
}

func proofFixture() ProofBundle {
	generation := strings.Repeat("g", 64)
	return ProofBundle{
		SchemaVersion: SchemaVersion,
		Claim: NewClaimRef(
			strings.Repeat("r", 64),
			"security_invariant",
			"All administrative routes pass through authorization.",
		),
		IndexState: IndexState{
			Coherent: true,
			Freshness: "current",
			IndexGeneration: generation,
		},
		Observations: []ObservationRef{
			proofObservation(
				"src/api/admin.py",
				"admin_handler",
				"code-graph",
				"resolved_call_path",
				"support",
				generation,
			),
			proofObservation(
				"src/auth/middleware.py",
				"AuthMiddleware.verify",
				"code-search",
				"hybrid_match",
				"support",
				generation,
			),
		},
		ContradictionSearch: ContradictionSearch{
			Performed: true,
			Strategy: "enumerate_routes_and_search_bypasses",
			CandidateCount: 13,
		},
		Coverage: Coverage{
			State: "complete",
			Examined: 13,
			Expected: intPtr(13),
			Unresolved: 0,
		},
		Invariant: &InvariantResult{
			ID: "SEC-AUTH-001",
			Status: "pass",
			Checked: 13,
			Violations: 0,
			Unresolved: 0,
		},
	}
}

func TestEvaluateProofVerifiedCrossEngineVector(t *testing.T) {
	result, err := EvaluateProof(proofFixture())
	if err != nil {
		t.Fatalf("EvaluateProof: %v", err)
	}
	if result.Verdict != VerdictVerified {
		t.Fatalf("verdict = %q, want verified", result.Verdict)
	}
	if result.Confidence.Band != "high" {
		t.Fatalf("confidence = %q, want high", result.Confidence.Band)
	}
	const expected = "proof:v1:5ad05cdd391d4c55f3bc7bbf6cabecefea17d423d9c7802c886ed29b44175df7"
	if result.ProofID != expected {
		t.Fatalf("cross-engine proof id mismatch: got %s want %s", result.ProofID, expected)
	}
}

func TestEvaluateProofCounterexampleContradicts(t *testing.T) {
	bundle := proofFixture()
	bundle.Observations = append(
		bundle.Observations,
		proofObservation(
			"src/api/debug.py",
			"debug_admin_handler",
			"code-graph",
			"authorization_bypass",
			"contradict",
			bundle.IndexState.IndexGeneration,
		),
	)
	bundle.Invariant = &InvariantResult{
		ID: "SEC-AUTH-001",
		Status: "fail",
		Checked: 13,
		Violations: 1,
		Unresolved: 0,
	}
	result, err := EvaluateProof(bundle)
	if err != nil {
		t.Fatalf("EvaluateProof: %v", err)
	}
	if result.Verdict != VerdictContradicted {
		t.Fatalf("verdict = %q, want contradicted", result.Verdict)
	}
	if len(result.ContradictingObservationIDs) != 1 {
		t.Fatalf("contradicting observations = %d, want 1", len(result.ContradictingObservationIDs))
	}
}

func TestEvaluateProofWithoutContradictionPassIsUnresolved(t *testing.T) {
	bundle := proofFixture()
	bundle.ContradictionSearch.Performed = false
	result, err := EvaluateProof(bundle)
	if err != nil {
		t.Fatalf("EvaluateProof: %v", err)
	}
	if result.Verdict != VerdictUnresolved {
		t.Fatalf("verdict = %q, want unresolved", result.Verdict)
	}
}

func TestEvaluateProofIncoherentIndexIsBlocked(t *testing.T) {
	bundle := proofFixture()
	bundle.IndexState.Coherent = false
	result, err := EvaluateProof(bundle)
	if err != nil {
		t.Fatalf("EvaluateProof: %v", err)
	}
	if result.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %q, want blocked", result.Verdict)
	}
	if len(result.Blockers) != 1 || result.Blockers[0] != "cross_engine_index_incoherent" {
		t.Fatalf("blockers = %#v", result.Blockers)
	}
}

func TestProofValidationRejectsOtherGeneration(t *testing.T) {
	bundle := proofFixture()
	bundle.Observations[0] = proofObservation(
		"src/api/admin.py",
		"admin_handler",
		"code-graph",
		"resolved_call_path",
		"support",
		strings.Repeat("x", 64),
	)
	if _, err := EvaluateProof(bundle); err == nil || !strings.Contains(err.Error(), "different index generation") {
		t.Fatalf("expected generation error, got %v", err)
	}
}

func TestProofValidationRejectsForgedReferenceID(t *testing.T) {
	bundle := proofFixture()
	bundle.Observations[0].EvidenceRef.ID = "ev:v1:" + strings.Repeat("0", 64)
	if _, err := EvaluateProof(bundle); err == nil || !strings.Contains(err.Error(), "canonical contents") {
		t.Fatalf("expected canonical-id error, got %v", err)
	}
}
