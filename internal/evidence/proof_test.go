package evidence

import (
	"slices"
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

func proofRelationshipObservationWithSource(resolutionSource, confidenceBand string, runtimeObserved bool, observationCount int, generation string) ObservationRef {
	repositoryID := strings.Repeat("r", 64)
	sourceRevision := strings.Repeat("s", 40)
	source := NewSymbolRef(
		repositoryID,
		sourceRevision,
		"src/api/admin.py",
		"function",
		"repo.src.api.admin.admin_handler",
		1,
		10,
	)
	target := NewSymbolRef(
		repositoryID,
		sourceRevision,
		"src/auth/middleware.py",
		"method",
		"repo.src.auth.middleware.AuthMiddleware.verify",
		1,
		10,
	)
	artifact := ""
	if slices.Contains(strings.Split(resolutionSource, "+"), "scip-ingest") {
		artifact = strings.Repeat("a", 64)
	}
	relationship := NewRelationshipRefWithArtifact(
		repositoryID,
		sourceRevision,
		generation,
		"CALLS",
		source,
		target,
		resolutionSource,
		artifact,
		confidenceBand,
		runtimeObserved,
		observationCount,
	)
	evidence := NewRelationshipEvidenceRef(relationship, "static_relationship")
	if runtimeObserved {
		evidence = NewRelationshipEvidenceRef(relationship, "runtime_validated_relationship")
	}
	return NewObservationRef(evidence, "support", "code-graph", resolutionSource, confidenceBand)
}

func proofRelationshipObservation(confidenceBand string, runtimeObserved bool, observationCount int, generation string) ObservationRef {
	return proofRelationshipObservationWithSource(
		"go_lsp_cross_file",
		confidenceBand,
		runtimeObserved,
		observationCount,
		generation,
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
			Coherent:        true,
			Freshness:       "current",
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
			Performed:      true,
			Strategy:       "enumerate_routes_and_search_bypasses",
			CandidateCount: 13,
		},
		Coverage: Coverage{
			State:      "complete",
			Examined:   13,
			Expected:   intPtr(13),
			Unresolved: 0,
		},
		Invariant: &InvariantResult{
			ID:         "SEC-AUTH-001",
			Status:     "pass",
			Checked:    13,
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

func TestEvaluateProofAcceptsCanonicalRelationshipEvidence(t *testing.T) {
	bundle := proofFixture()
	bundle.Observations = []ObservationRef{
		proofRelationshipObservation("high", true, 17, bundle.IndexState.IndexGeneration),
	}

	result, err := EvaluateProof(bundle)
	if err != nil {
		t.Fatalf("EvaluateProof: %v", err)
	}
	if result.Verdict != VerdictVerified {
		t.Fatalf("verdict = %q, want verified", result.Verdict)
	}
}

func TestEvaluateProofTreatsSpeculativeOnlySupportAsUnresolved(t *testing.T) {
	bundle := proofFixture()
	bundle.Observations = []ObservationRef{
		proofRelationshipObservation("speculative", false, 0, bundle.IndexState.IndexGeneration),
	}

	result, err := EvaluateProof(bundle)
	if err != nil {
		t.Fatalf("EvaluateProof: %v", err)
	}
	if result.Verdict != VerdictUnresolved {
		t.Fatalf("verdict = %q, want unresolved", result.Verdict)
	}
	if len(result.Caveats) != 1 || result.Caveats[0] != "supporting_evidence_not_trustworthy" {
		t.Fatalf("caveats = %#v", result.Caveats)
	}
}

func TestEvaluateProofRequiresCompilerCapabilityWhenRequested(t *testing.T) {
	bundle := proofFixture()
	bundle.AssuranceRequirement = &AssuranceRequirement{
		RequiredCapabilities: []string{"compiler_resolution"},
	}
	bundle.Observations = []ObservationRef{
		proofRelationshipObservationWithSource(
			"heuristic_static_resolution",
			"high",
			false,
			0,
			bundle.IndexState.IndexGeneration,
		),
	}

	result, err := EvaluateProof(bundle)
	if err != nil {
		t.Fatalf("EvaluateProof: %v", err)
	}
	if result.Verdict != VerdictUnresolved {
		t.Fatalf("verdict = %q, want unresolved", result.Verdict)
	}
	if !slices.Contains(result.Caveats, "required_assurance_not_satisfied") {
		t.Fatalf("caveats = %v", result.Caveats)
	}
	if got := result.AssuranceLattice.MissingSupportingCapabilities; len(got) != 1 || got[0] != "compiler_resolution" {
		t.Fatalf("missing capabilities = %v", got)
	}
}

func TestEvaluateProofAcceptsSCIPCompilerCapabilityVector(t *testing.T) {
	bundle := proofFixture()
	bundle.AssuranceRequirement = &AssuranceRequirement{
		RequiredCapabilities: []string{
			"source_coordinates",
			"structural_relationship",
			"compiler_resolution",
		},
	}
	bundle.Observations = []ObservationRef{
		proofRelationshipObservationWithSource(
			"scip-ingest",
			"high",
			false,
			0,
			bundle.IndexState.IndexGeneration,
		),
	}

	result, err := EvaluateProof(bundle)
	if err != nil {
		t.Fatalf("EvaluateProof: %v", err)
	}
	if result.Verdict != VerdictVerified {
		t.Fatalf("verdict = %q, want verified", result.Verdict)
	}
	const expected = "proof:v1:6a0310be2366d2696dc6645546cc137bd32dc5d490e504c983c3d71c01c04bd1"
	if result.ProofID != expected {
		t.Fatalf("lattice proof id mismatch: got %s want %s", result.ProofID, expected)
	}
	if result.AssuranceLattice.SatisfiedBy == nil || *result.AssuranceLattice.SatisfiedBy != "support" {
		t.Fatalf("satisfied_by = %v", result.AssuranceLattice.SatisfiedBy)
	}
}

func TestEvaluateProofRejectsUnboundLegacySCIPCompilerCapability(t *testing.T) {
	bundle := proofFixture()
	bundle.AssuranceRequirement = &AssuranceRequirement{
		RequiredCapabilities: []string{"compiler_resolution"},
	}
	bound := proofRelationshipObservationWithSource(
		"scip-ingest",
		"high",
		false,
		0,
		bundle.IndexState.IndexGeneration,
	)
	legacyRelationship := NewRelationshipRef(
		bound.EvidenceRef.RelationshipRef.RepositoryID,
		bound.EvidenceRef.RelationshipRef.SourceRevision,
		bound.EvidenceRef.RelationshipRef.IndexGeneration,
		bound.EvidenceRef.RelationshipRef.RelationType,
		bound.EvidenceRef.RelationshipRef.SourceSymbolRef,
		bound.EvidenceRef.RelationshipRef.TargetSymbolRef,
		"scip-ingest",
		"high",
		false,
		0,
	)
	legacyEvidence := NewRelationshipEvidenceRef(legacyRelationship, "static_relationship")
	bundle.Observations = []ObservationRef{
		NewObservationRef(legacyEvidence, "support", "code-graph", "scip-ingest", "high"),
	}

	result, err := EvaluateProof(bundle)
	if err != nil {
		t.Fatalf("EvaluateProof: %v", err)
	}
	if result.Verdict != VerdictUnresolved {
		t.Fatalf("verdict = %q, want unresolved", result.Verdict)
	}
	if got := result.AssuranceLattice.MissingSupportingCapabilities; len(got) != 1 || got[0] != "compiler_resolution" {
		t.Fatalf("missing capabilities = %v", got)
	}
}

func TestProofValidationRejectsRelationshipProvenanceOverrides(t *testing.T) {
	t.Run("confidence", func(t *testing.T) {
		bundle := proofFixture()
		observation := proofRelationshipObservation("speculative", false, 0, bundle.IndexState.IndexGeneration)
		bundle.Observations = []ObservationRef{
			NewObservationRef(observation.EvidenceRef, "support", "code-graph", observation.Derivation, "high"),
		}
		if _, err := EvaluateProof(bundle); err == nil || !strings.Contains(err.Error(), "confidence band disagrees") {
			t.Fatalf("expected confidence mismatch error, got %v", err)
		}
	})

	t.Run("derivation", func(t *testing.T) {
		bundle := proofFixture()
		observation := proofRelationshipObservation("high", true, 17, bundle.IndexState.IndexGeneration)
		bundle.Observations = []ObservationRef{
			NewObservationRef(observation.EvidenceRef, "support", "code-graph", "rewritten_after_capture", "high"),
		}
		if _, err := EvaluateProof(bundle); err == nil || !strings.Contains(err.Error(), "derivation disagrees") {
			t.Fatalf("expected derivation mismatch error, got %v", err)
		}
	})
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
		ID:         "SEC-AUTH-001",
		Status:     "fail",
		Checked:    13,
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

func TestProofValidationRejectsFalseCompleteCoverage(t *testing.T) {
	t.Run("missing expected count", func(t *testing.T) {
		bundle := proofFixture()
		bundle.Coverage.Expected = nil
		if _, err := EvaluateProof(bundle); err == nil || !strings.Contains(err.Error(), "known expected count") {
			t.Fatalf("expected missing-denominator error, got %v", err)
		}
	})

	t.Run("examined count is incomplete", func(t *testing.T) {
		bundle := proofFixture()
		bundle.Coverage.Examined--
		if _, err := EvaluateProof(bundle); err == nil || !strings.Contains(err.Error(), "examined to equal expected") {
			t.Fatalf("expected incomplete-coverage error, got %v", err)
		}
	})
}

func TestProofValidationRejectsForgedReferenceID(t *testing.T) {
	bundle := proofFixture()
	bundle.Observations[0].EvidenceRef.ID = "ev:v1:" + strings.Repeat("0", 64)
	if _, err := EvaluateProof(bundle); err == nil || !strings.Contains(err.Error(), "canonical contents") {
		t.Fatalf("expected canonical-id error, got %v", err)
	}
}
