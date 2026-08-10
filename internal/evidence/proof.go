package evidence

import (
	"fmt"
	"sort"
	"strings"
)

const (
	VerdictVerified     = "verified"
	VerdictContradicted = "contradicted"
	VerdictUnresolved   = "unresolved"
	VerdictBlocked      = "blocked"
)

type IndexState struct {
	Coherent        bool   `json:"coherent"`
	Freshness       string `json:"freshness"`
	IndexGeneration string `json:"index_generation"`
}

type ContradictionSearch struct {
	Performed      bool   `json:"performed"`
	Strategy       string `json:"strategy"`
	CandidateCount int    `json:"candidate_count"`
}

type Coverage struct {
	State      string `json:"state"`
	Examined   int    `json:"examined"`
	Expected   *int   `json:"expected"`
	Unresolved int    `json:"unresolved"`
}

type InvariantResult struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Checked    int    `json:"checked"`
	Violations int    `json:"violations"`
	Unresolved int    `json:"unresolved"`
}

type ProofBundle struct {
	SchemaVersion       int                 `json:"schema_version"`
	Claim               ClaimRef            `json:"claim"`
	IndexState          IndexState          `json:"index_state"`
	Observations        []ObservationRef    `json:"observations"`
	ContradictionSearch ContradictionSearch `json:"contradiction_search"`
	Coverage            Coverage            `json:"coverage"`
	Invariant           *InvariantResult    `json:"invariant,omitempty"`
}

type Confidence struct {
	Band      string   `json:"band"`
	Rationale []string `json:"rationale"`
}

type ProofResult struct {
	ProofID                     string              `json:"proof_id"`
	SchemaVersion               int                 `json:"schema_version"`
	ClaimID                     string              `json:"claim_id"`
	IndexGeneration             string              `json:"index_generation"`
	Verdict                     string              `json:"verdict"`
	SupportingObservationIDs    []string            `json:"supporting_observation_ids"`
	ContradictingObservationIDs []string            `json:"contradicting_observation_ids"`
	Blockers                    []string            `json:"blockers"`
	Caveats                     []string            `json:"caveats"`
	Confidence                  Confidence          `json:"confidence"`
	Coverage                    Coverage            `json:"coverage"`
	ContradictionSearch         ContradictionSearch `json:"contradiction_search"`
	Invariant                   *InvariantResult    `json:"invariant,omitempty"`
}

func validToken(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateSymbolRef(ref *SymbolRef) error {
	if ref == nil {
		return nil
	}
	expected := NewSymbolRef(
		ref.RepositoryID,
		ref.SourceRevision,
		ref.RelativePath,
		ref.SymbolKind,
		ref.QualifiedName,
		ref.StartLine,
		ref.EndLine,
	)
	if ref.ID != expected.ID {
		return fmt.Errorf("symbol_ref id does not match canonical contents")
	}
	return nil
}

func validateRelationshipRef(ref *RelationshipRef) error {
	if ref == nil {
		return nil
	}
	if err := validateSymbolRef(&ref.SourceSymbolRef); err != nil {
		return fmt.Errorf("source_symbol_ref: %w", err)
	}
	if err := validateSymbolRef(&ref.TargetSymbolRef); err != nil {
		return fmt.Errorf("target_symbol_ref: %w", err)
	}
	for role, symbol := range map[string]SymbolRef{
		"source": ref.SourceSymbolRef,
		"target": ref.TargetSymbolRef,
	} {
		if symbol.RepositoryID != ref.RepositoryID {
			return fmt.Errorf("%s_symbol_ref belongs to a different repository", role)
		}
		if symbol.SourceRevision != ref.SourceRevision {
			return fmt.Errorf("%s_symbol_ref belongs to a different source revision", role)
		}
	}
	if !validToken(ref.ConfidenceBand, "high", "medium", "low", "speculative", "unknown") {
		return fmt.Errorf("relationship confidence band %q is invalid", ref.ConfidenceBand)
	}
	if ref.RuntimeObserved && ref.ObservationCount == 0 {
		return fmt.Errorf("runtime_observed requires a positive observation_count")
	}
	if !ref.RuntimeObserved && ref.ObservationCount != 0 {
		return fmt.Errorf("observation_count requires runtime_observed=true")
	}
	expected := NewRelationshipRef(
		ref.RepositoryID,
		ref.SourceRevision,
		ref.IndexGeneration,
		ref.RelationType,
		ref.SourceSymbolRef,
		ref.TargetSymbolRef,
		ref.ResolutionSource,
		ref.ConfidenceBand,
		ref.RuntimeObserved,
		ref.ObservationCount,
	)
	if ref.ID != expected.ID {
		return fmt.Errorf("relationship_ref id does not match canonical contents")
	}
	return nil
}

func validateEvidenceRef(ref EvidenceRef) error {
	if err := validateSymbolRef(ref.SymbolRef); err != nil {
		return err
	}
	var expected EvidenceRef
	if ref.RelationshipRef == nil {
		expected = NewEvidenceRef(
			ref.RepositoryID,
			ref.SourceRevision,
			ref.IndexGeneration,
			ref.RelativePath,
			ref.StartLine,
			ref.EndLine,
			ref.EvidenceType,
			ref.SymbolRef,
		)
	} else {
		if err := validateRelationshipRef(ref.RelationshipRef); err != nil {
			return err
		}
		relationship := ref.RelationshipRef
		if relationship.RepositoryID != ref.RepositoryID ||
			relationship.SourceRevision != ref.SourceRevision ||
			relationship.IndexGeneration != ref.IndexGeneration {
			return fmt.Errorf("relationship_ref belongs to different evidence coordinates")
		}
		if ref.SymbolRef == nil || relationship.SourceSymbolRef.ID != ref.SymbolRef.ID {
			return fmt.Errorf("relationship_ref source does not match symbol_ref")
		}
		expected = NewRelationshipEvidenceRef(*relationship, ref.EvidenceType)
	}
	if ref.ID != expected.ID {
		return fmt.Errorf("evidence_ref id does not match canonical contents")
	}
	return nil
}

func validateObservationRef(ref ObservationRef) error {
	if err := validateEvidenceRef(ref.EvidenceRef); err != nil {
		return err
	}
	if !validToken(ref.Stance, "support", "contradict") {
		return fmt.Errorf("observation stance %q is invalid", ref.Stance)
	}
	if !validToken(ref.ConfidenceBand, "high", "medium", "low", "speculative", "unknown") {
		return fmt.Errorf("observation confidence band %q is invalid", ref.ConfidenceBand)
	}
	if strings.TrimSpace(ref.SourceEngine) == "" || strings.TrimSpace(ref.Derivation) == "" {
		return fmt.Errorf("observation source_engine and derivation are required")
	}
	if relationship := ref.EvidenceRef.RelationshipRef; relationship != nil {
		if relationship.ConfidenceBand != ref.ConfidenceBand {
			return fmt.Errorf("observation confidence band disagrees with relationship_ref")
		}
		if relationship.ResolutionSource != ref.Derivation {
			return fmt.Errorf("observation derivation disagrees with relationship_ref")
		}
	}
	expected := NewObservationRef(
		ref.EvidenceRef,
		ref.Stance,
		ref.SourceEngine,
		ref.Derivation,
		ref.ConfidenceBand,
	)
	if ref.ID != expected.ID {
		return fmt.Errorf("observation id does not match canonical contents")
	}
	return nil
}

func (bundle ProofBundle) Validate() error {
	if bundle.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must equal %d", SchemaVersion)
	}
	expectedClaim := NewClaimRef(
		bundle.Claim.RepositoryID,
		bundle.Claim.ClaimKind,
		bundle.Claim.ClaimText,
	)
	if bundle.Claim.ID != expectedClaim.ID {
		return fmt.Errorf("claim id does not match canonical contents")
	}
	if bundle.IndexState.IndexGeneration == "" {
		return fmt.Errorf("index_generation is required")
	}
	if !validToken(bundle.IndexState.Freshness, "current", "stale", "unknown") {
		return fmt.Errorf("index freshness %q is invalid", bundle.IndexState.Freshness)
	}
	if strings.TrimSpace(bundle.ContradictionSearch.Strategy) == "" {
		return fmt.Errorf("contradiction search strategy is required")
	}
	if bundle.ContradictionSearch.CandidateCount < 0 {
		return fmt.Errorf("contradiction candidate_count cannot be negative")
	}
	if !validToken(bundle.Coverage.State, "complete", "partial", "unknown") {
		return fmt.Errorf("coverage state %q is invalid", bundle.Coverage.State)
	}
	if bundle.Coverage.Examined < 0 || bundle.Coverage.Unresolved < 0 {
		return fmt.Errorf("coverage counts cannot be negative")
	}
	if bundle.Coverage.Expected != nil {
		if *bundle.Coverage.Expected < 0 {
			return fmt.Errorf("coverage expected cannot be negative")
		}
		if bundle.Coverage.Examined > *bundle.Coverage.Expected {
			return fmt.Errorf("coverage examined cannot exceed expected")
		}
	}
	if bundle.Coverage.State == "complete" && bundle.Coverage.Expected == nil {
		return fmt.Errorf("complete coverage requires a known expected count")
	}
	if bundle.Coverage.State == "complete" && bundle.Coverage.Examined != *bundle.Coverage.Expected {
		return fmt.Errorf("complete coverage requires examined to equal expected")
	}

	seen := make(map[string]bool, len(bundle.Observations))
	for i, observation := range bundle.Observations {
		if err := validateObservationRef(observation); err != nil {
			return fmt.Errorf("observations[%d]: %w", i, err)
		}
		if observation.EvidenceRef.RepositoryID != bundle.Claim.RepositoryID {
			return fmt.Errorf("observations[%d] belongs to a different repository", i)
		}
		if observation.EvidenceRef.IndexGeneration != bundle.IndexState.IndexGeneration {
			return fmt.Errorf("observations[%d] belongs to a different index generation", i)
		}
		if seen[observation.ID] {
			return fmt.Errorf("observation ids must be unique")
		}
		seen[observation.ID] = true
	}

	if bundle.Invariant != nil {
		invariant := bundle.Invariant
		if strings.TrimSpace(invariant.ID) == "" {
			return fmt.Errorf("invariant id is required")
		}
		if !validToken(invariant.Status, "pass", "fail", "unresolved") {
			return fmt.Errorf("invariant status %q is invalid", invariant.Status)
		}
		if invariant.Checked < 0 || invariant.Violations < 0 || invariant.Unresolved < 0 {
			return fmt.Errorf("invariant counts cannot be negative")
		}
		if invariant.Status == "pass" && (invariant.Violations > 0 || invariant.Unresolved > 0) {
			return fmt.Errorf("a passing invariant cannot have violations or unresolved subjects")
		}
		if invariant.Status == "fail" && invariant.Violations == 0 {
			return fmt.Errorf("a failing invariant must contain a violation")
		}
	}
	return nil
}

func proofConfidence(verdict string, supporting, contradicting []ObservationRef) Confidence {
	switch verdict {
	case VerdictBlocked:
		return Confidence{
			Band:      "unknown",
			Rationale: []string{"proof evaluation was blocked before evidence could be trusted"},
		}
	case VerdictContradicted:
		band := "medium"
		if len(contradicting) > 0 {
			band = "high"
		}
		return Confidence{
			Band:      band,
			Rationale: []string{"a counterexample or invariant violation directly contradicts the claim"},
		}
	case VerdictUnresolved:
		return Confidence{
			Band:      "low",
			Rationale: []string{"one or more proof completeness requirements were not met"},
		}
	}

	engines := map[string]bool{}
	derivations := map[string]bool{}
	for _, observation := range supporting {
		engines[observation.SourceEngine] = true
		derivations[observation.Derivation] = true
	}
	if len(engines) >= 2 || len(derivations) >= 2 {
		return Confidence{
			Band: "high",
			Rationale: []string{
				"complete proof with an explicit contradiction pass",
				"support is corroborated by independent engines or derivations",
			},
		}
	}
	return Confidence{
		Band: "medium",
		Rationale: []string{
			"complete proof with an explicit contradiction pass",
			"support comes from one engine and derivation",
		},
	}
}

func isTrustedSupport(observation ObservationRef) bool {
	if validToken(observation.ConfidenceBand, "high", "medium") {
		return true
	}
	return observation.EvidenceRef.RelationshipRef != nil &&
		observation.EvidenceRef.RelationshipRef.RuntimeObserved
}

func EvaluateProof(bundle ProofBundle) (ProofResult, error) {
	if err := bundle.Validate(); err != nil {
		return ProofResult{}, err
	}

	supporting := make([]ObservationRef, 0)
	contradicting := make([]ObservationRef, 0)
	for _, observation := range bundle.Observations {
		if observation.Stance == "contradict" {
			contradicting = append(contradicting, observation)
		} else {
			supporting = append(supporting, observation)
		}
	}

	blockers := make([]string, 0)
	caveats := make([]string, 0)
	if !bundle.IndexState.Coherent {
		blockers = append(blockers, "cross_engine_index_incoherent")
	}
	if bundle.IndexState.Freshness != "current" {
		blockers = append(blockers, "index_"+bundle.IndexState.Freshness)
	}

	invariantFailed := bundle.Invariant != nil &&
		(bundle.Invariant.Status == "fail" || bundle.Invariant.Violations > 0)
	invariantUnresolved := bundle.Invariant != nil &&
		(bundle.Invariant.Status == "unresolved" || bundle.Invariant.Unresolved > 0)

	verdict := VerdictVerified
	switch {
	case len(blockers) > 0:
		verdict = VerdictBlocked
	case len(contradicting) > 0 || invariantFailed:
		verdict = VerdictContradicted
	default:
		if !bundle.ContradictionSearch.Performed {
			caveats = append(caveats, "contradiction_search_not_performed")
		}
		if bundle.Coverage.State != "complete" {
			caveats = append(caveats, "coverage_"+bundle.Coverage.State)
		}
		if bundle.Coverage.Unresolved > 0 {
			caveats = append(caveats, "coverage_has_unresolved_subjects")
		}
		if invariantUnresolved {
			caveats = append(caveats, "invariant_unresolved")
		}
		if len(supporting) == 0 {
			caveats = append(caveats, "no_supporting_evidence")
		} else {
			trusted := false
			for _, observation := range supporting {
				if isTrustedSupport(observation) {
					trusted = true
					break
				}
			}
			if !trusted {
				caveats = append(caveats, "supporting_evidence_not_trustworthy")
			}
		}
		if len(caveats) > 0 {
			verdict = VerdictUnresolved
		}
	}

	supportingIDs := make([]string, 0, len(supporting))
	for _, observation := range supporting {
		supportingIDs = append(supportingIDs, observation.ID)
	}
	contradictingIDs := make([]string, 0, len(contradicting))
	for _, observation := range contradicting {
		contradictingIDs = append(contradictingIDs, observation.ID)
	}
	sort.Strings(supportingIDs)
	sort.Strings(contradictingIDs)
	sort.Strings(blockers)
	sort.Strings(caveats)

	proofPayload := map[string]any{
		"schema_version":                SchemaVersion,
		"claim_id":                      bundle.Claim.ID,
		"index_generation":              bundle.IndexState.IndexGeneration,
		"verdict":                       verdict,
		"supporting_observation_ids":    supportingIDs,
		"contradicting_observation_ids": contradictingIDs,
		"blockers":                      blockers,
		"caveats":                       caveats,
	}
	return ProofResult{
		ProofID:                     stableID("proof", proofPayload),
		SchemaVersion:               SchemaVersion,
		ClaimID:                     bundle.Claim.ID,
		IndexGeneration:             bundle.IndexState.IndexGeneration,
		Verdict:                     verdict,
		SupportingObservationIDs:    supportingIDs,
		ContradictingObservationIDs: contradictingIDs,
		Blockers:                    blockers,
		Caveats:                     caveats,
		Confidence:                  proofConfidence(verdict, supporting, contradicting),
		Coverage:                    bundle.Coverage,
		ContradictionSearch:         bundle.ContradictionSearch,
		Invariant:                   bundle.Invariant,
	}, nil
}
