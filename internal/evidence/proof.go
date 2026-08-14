package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
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

type AssuranceRequirement struct {
	RequiredCapabilities []string `json:"required_capabilities"`
}

type AssuranceLattice struct {
	RequiredCapabilities             []string `json:"required_capabilities"`
	SupportingCapabilities           []string `json:"supporting_capabilities"`
	ContradictingCapabilities        []string `json:"contradicting_capabilities"`
	MissingSupportingCapabilities    []string `json:"missing_supporting_capabilities"`
	MissingContradictingCapabilities []string `json:"missing_contradicting_capabilities"`
	SatisfiedBy                      *string  `json:"satisfied_by"`
}

type ProofBundle struct {
	SchemaVersion        int                   `json:"schema_version"`
	Claim                ClaimRef              `json:"claim"`
	IndexState           IndexState            `json:"index_state"`
	AssuranceRequirement *AssuranceRequirement `json:"assurance_requirement,omitempty"`
	Observations         []ObservationRef      `json:"observations"`
	ContradictionSearch  ContradictionSearch   `json:"contradiction_search"`
	Coverage             Coverage              `json:"coverage"`
	Invariant            *InvariantResult      `json:"invariant,omitempty"`
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
	AssuranceLattice            AssuranceLattice    `json:"assurance_lattice"`
}

var assuranceCapabilities = map[string]bool{
	"source_coordinates":      true,
	"semantic_retrieval":      true,
	"lexical_retrieval":       true,
	"structural_relationship": true,
	"compiler_resolution":     true,
	"runtime_observation":     true,
	"variable_level_taint":    true,
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
	if ref.ResolutionArtifactSHA256 != "" {
		decoded, err := hex.DecodeString(ref.ResolutionArtifactSHA256)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(ref.ResolutionArtifactSHA256) != ref.ResolutionArtifactSHA256 {
			return fmt.Errorf("resolution_artifact_sha256 must be 64 lowercase hex characters")
		}
		if !slices.Contains(strings.Split(ref.ResolutionSource, "+"), "scip-ingest") {
			return fmt.Errorf("resolution_artifact_sha256 requires scip-ingest provenance")
		}
	}
	if ref.RuntimeObserved && ref.ObservationCount == 0 {
		return fmt.Errorf("runtime_observed requires a positive observation_count")
	}
	if !ref.RuntimeObserved && ref.ObservationCount != 0 {
		return fmt.Errorf("observation_count requires runtime_observed=true")
	}
	expected := NewRelationshipRefWithArtifact(
		ref.RepositoryID,
		ref.SourceRevision,
		ref.IndexGeneration,
		ref.RelationType,
		ref.SourceSymbolRef,
		ref.TargetSymbolRef,
		ref.ResolutionSource,
		ref.ResolutionArtifactSHA256,
		ref.ConfidenceBand,
		ref.RuntimeObserved,
		ref.ObservationCount,
	)
	if ref.ID != expected.ID {
		return fmt.Errorf("relationship_ref id does not match canonical contents")
	}
	return nil
}

func validateEvidenceRef(ref *EvidenceRef) error {
	if err := validateSymbolRef(ref.SymbolRef); err != nil {
		return err
	}
	if ref.RelationshipRef != nil && ref.AnalysisRef != nil {
		return fmt.Errorf("evidence_ref cannot contain both relationship_ref and analysis_ref")
	}
	var expected EvidenceRef
	switch {
	case ref.AnalysisRef != nil:
		if err := validateAnalysisRef(ref.AnalysisRef); err != nil {
			return err
		}
		analysis := ref.AnalysisRef
		if analysis.RepositoryID != ref.RepositoryID ||
			analysis.SourceRevision != ref.SourceRevision ||
			analysis.IndexGeneration != ref.IndexGeneration {
			return fmt.Errorf("analysis_ref belongs to different evidence coordinates")
		}
		if ref.EvidenceType != "codeql_path" {
			return fmt.Errorf("analysis_ref requires codeql_path evidence")
		}
		expected = NewAnalysisEvidenceRef(*analysis)
	case ref.RelationshipRef == nil:
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
	default:
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

func validateAnalysisRef(ref *AnalysisRef) error {
	if ref == nil {
		return nil
	}
	if ref.AnalysisKind != "variable_level_taint" || ref.Analyzer != "codeql" {
		return fmt.Errorf("analysis_ref must be CodeQL variable_level_taint")
	}
	if ref.DatabaseQuality.Status != "pass" || ref.DatabaseQuality.SourceFiles <= 0 || ref.DatabaseQuality.BaselineLines <= 0 || ref.DatabaseQuality.ExtractorErrors < 0 {
		return fmt.Errorf("analysis_ref database quality is not passing")
	}
	for _, artifact := range []struct {
		name   string
		digest string
	}{
		{name: "database_manifest_sha256", digest: ref.DatabaseManifestSHA256},
		{name: "database_content_sha256", digest: ref.DatabaseContentSHA256},
		{name: "query_pack_manifest_sha256", digest: ref.QueryPackManifestSHA256},
		{name: "sarif_sha256", digest: ref.SARIFSHA256},
	} {
		decoded, err := hex.DecodeString(artifact.digest)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(artifact.digest) != artifact.digest {
			return fmt.Errorf("analysis_ref %s must be 64 lowercase hex characters", artifact.name)
		}
	}
	if ref.QueryAttestationSHA256 != "" {
		decoded, err := hex.DecodeString(ref.QueryAttestationSHA256)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(ref.QueryAttestationSHA256) != ref.QueryAttestationSHA256 {
			return fmt.Errorf("analysis_ref query_attestation_sha256 must be 64 lowercase hex characters")
		}
	}
	if len(ref.PathSteps) < 2 {
		return fmt.Errorf("analysis_ref path must contain at least two steps")
	}
	for i, step := range ref.PathSteps {
		wantRole := "intermediate"
		if i == 0 {
			wantRole = "source"
		} else if i == len(ref.PathSteps)-1 {
			wantRole = "sink"
		}
		if step.Position != i || step.Role != wantRole {
			return fmt.Errorf("analysis_ref path step %d has invalid position or role", i)
		}
		if step.StartLine <= 0 || step.StartColumn <= 0 || step.EndLine <= 0 || step.EndColumn <= 0 ||
			step.EndLine < step.StartLine || (step.EndLine == step.StartLine && step.EndColumn < step.StartColumn) {
			return fmt.Errorf("analysis_ref path step %d has invalid coordinates", i)
		}
	}
	expected := NewAttestedCodeQLAnalysisRef(
		ref.RepositoryID,
		ref.SourceRevision,
		ref.IndexGeneration,
		ref.AnalyzerVersion,
		ref.ExtractorVersion,
		ref.Language,
		ref.DatabaseManifestSHA256,
		ref.DatabaseContentSHA256,
		ref.DatabaseQuality,
		ref.QueryPackManifestSHA256,
		ref.SARIFSHA256,
		ref.QueryAttestationSHA256,
		ref.QueryID,
		ref.ResultIndex,
		ref.CodeFlowIndex,
		ref.ThreadFlowIndex,
		ref.PathSteps,
	)
	if ref.ID != expected.ID {
		return fmt.Errorf("analysis_ref id does not match canonical contents")
	}
	return nil
}

func validateObservationRef(ref *ObservationRef) error {
	if err := validateEvidenceRef(&ref.EvidenceRef); err != nil {
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

func validateProofHeader(bundle *ProofBundle) error {
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
	return nil
}

func validateCoverage(coverage Coverage) error {
	if !validToken(coverage.State, "complete", "partial", "unknown") {
		return fmt.Errorf("coverage state %q is invalid", coverage.State)
	}
	if coverage.Examined < 0 || coverage.Unresolved < 0 {
		return fmt.Errorf("coverage counts cannot be negative")
	}
	if coverage.Expected != nil {
		if *coverage.Expected < 0 {
			return fmt.Errorf("coverage expected cannot be negative")
		}
		if coverage.Examined > *coverage.Expected {
			return fmt.Errorf("coverage examined cannot exceed expected")
		}
	}
	if coverage.State == "complete" && coverage.Expected == nil {
		return fmt.Errorf("complete coverage requires a known expected count")
	}
	if coverage.State == "complete" && coverage.Examined != *coverage.Expected {
		return fmt.Errorf("complete coverage requires examined to equal expected")
	}
	return nil
}

func validateObservations(bundle *ProofBundle) error {
	seen := make(map[string]bool, len(bundle.Observations))
	for i := range bundle.Observations {
		observation := &bundle.Observations[i]
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
	return nil
}

func validateInvariant(invariant *InvariantResult) error {
	if invariant == nil {
		return nil
	}
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
	return nil
}

func validateAssuranceRequirement(requirement *AssuranceRequirement) error {
	if requirement == nil {
		return nil
	}
	if len(requirement.RequiredCapabilities) == 0 {
		return fmt.Errorf("assurance_requirement.required_capabilities cannot be empty")
	}
	seen := make(map[string]bool, len(requirement.RequiredCapabilities))
	for _, capability := range requirement.RequiredCapabilities {
		capability = strings.ToLower(strings.TrimSpace(capability))
		if !assuranceCapabilities[capability] {
			return fmt.Errorf("unsupported assurance capability %q", capability)
		}
		if seen[capability] {
			return fmt.Errorf("assurance capabilities must be unique")
		}
		seen[capability] = true
	}
	return nil
}

func (bundle *ProofBundle) Validate() error {
	if bundle == nil {
		return fmt.Errorf("proof bundle is required")
	}
	if err := validateProofHeader(bundle); err != nil {
		return err
	}
	if err := validateCoverage(bundle.Coverage); err != nil {
		return err
	}
	if err := validateObservations(bundle); err != nil {
		return err
	}
	if err := validateInvariant(bundle.Invariant); err != nil {
		return err
	}
	if err := validateAssuranceRequirement(bundle.AssuranceRequirement); err != nil {
		return err
	}
	return nil
}

func observationCapabilities(observation *ObservationRef) map[string]bool {
	capabilities := map[string]bool{"source_coordinates": true}
	switch observation.EvidenceRef.EvidenceType {
	case "semantic_match", "hybrid_match":
		capabilities["semantic_retrieval"] = true
	case "lexical_match":
		capabilities["lexical_retrieval"] = true
	case "codeql_path":
		if analysis := observation.EvidenceRef.AnalysisRef; analysis != nil &&
			analysis.Analyzer == "codeql" && analysis.AnalysisKind == "variable_level_taint" &&
			analysis.QueryAttestationSHA256 != "" {
			capabilities["variable_level_taint"] = true
		}
	}
	if relationship := observation.EvidenceRef.RelationshipRef; relationship != nil {
		capabilities["structural_relationship"] = true
		for _, source := range strings.Split(relationship.ResolutionSource, "+") {
			if source == "scip-ingest" && relationship.ResolutionArtifactSHA256 != "" {
				capabilities["compiler_resolution"] = true
			}
		}
		if relationship.RuntimeObserved {
			capabilities["runtime_observation"] = true
		}
	}
	return capabilities
}

func capabilityList(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func buildAssuranceLattice(requirement *AssuranceRequirement, supporting, contradicting []*ObservationRef) AssuranceLattice {
	required := map[string]bool{}
	if requirement != nil {
		for _, capability := range requirement.RequiredCapabilities {
			required[strings.ToLower(strings.TrimSpace(capability))] = true
		}
	}
	collect := func(observations []*ObservationRef) map[string]bool {
		result := map[string]bool{}
		for _, observation := range observations {
			for capability := range observationCapabilities(observation) {
				result[capability] = true
			}
		}
		return result
	}
	supportingCapabilities := collect(supporting)
	contradictingCapabilities := collect(contradicting)
	missing := func(observed map[string]bool) []string {
		result := []string{}
		for capability := range required {
			if !observed[capability] {
				result = append(result, capability)
			}
		}
		sort.Strings(result)
		return result
	}
	missingSupporting := missing(supportingCapabilities)
	missingContradicting := missing(contradictingCapabilities)
	var satisfiedBy *string
	if len(required) > 0 && len(contradicting) > 0 && len(missingContradicting) == 0 {
		value := "contradiction"
		satisfiedBy = &value
	} else if len(required) > 0 && len(supporting) > 0 && len(missingSupporting) == 0 {
		value := "support"
		satisfiedBy = &value
	}
	return AssuranceLattice{
		RequiredCapabilities:             capabilityList(required),
		SupportingCapabilities:           capabilityList(supportingCapabilities),
		ContradictingCapabilities:        capabilityList(contradictingCapabilities),
		MissingSupportingCapabilities:    missingSupporting,
		MissingContradictingCapabilities: missingContradicting,
		SatisfiedBy:                      satisfiedBy,
	}
}

func assuranceLatticeMap(lattice *AssuranceLattice) map[string]any {
	var satisfiedBy any
	if lattice.SatisfiedBy != nil {
		satisfiedBy = *lattice.SatisfiedBy
	}
	return map[string]any{
		"required_capabilities":              lattice.RequiredCapabilities,
		"supporting_capabilities":            lattice.SupportingCapabilities,
		"contradicting_capabilities":         lattice.ContradictingCapabilities,
		"missing_supporting_capabilities":    lattice.MissingSupportingCapabilities,
		"missing_contradicting_capabilities": lattice.MissingContradictingCapabilities,
		"satisfied_by":                       satisfiedBy,
	}
}

func proofConfidence(verdict string, supporting, contradicting []*ObservationRef) Confidence {
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

func isTrustedSupport(observation *ObservationRef) bool {
	if validToken(observation.ConfidenceBand, "high", "medium") {
		return true
	}
	return observation.EvidenceRef.RelationshipRef != nil &&
		observation.EvidenceRef.RelationshipRef.RuntimeObserved
}

func incompleteProofCaveats(
	bundle *ProofBundle,
	supporting []*ObservationRef,
	invariantUnresolved bool,
	assuranceRequired bool,
	assuranceSatisfiedBy func(string) bool,
) []string {
	caveats := make([]string, 0)
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
	if assuranceRequired && !assuranceSatisfiedBy("support") {
		caveats = append(caveats, "required_assurance_not_satisfied")
	}
	return caveats
}

// EvaluateProof works from a value copy so evaluation cannot mutate caller-owned evidence.
//
//nolint:gocritic // The defensive copy is part of the deterministic proof contract.
func EvaluateProof(bundle ProofBundle) (ProofResult, error) {
	if err := bundle.Validate(); err != nil {
		return ProofResult{}, err
	}

	supporting := make([]*ObservationRef, 0)
	contradicting := make([]*ObservationRef, 0)
	for i := range bundle.Observations {
		observation := &bundle.Observations[i]
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
	assuranceLattice := buildAssuranceLattice(bundle.AssuranceRequirement, supporting, contradicting)
	assuranceRequired := len(assuranceLattice.RequiredCapabilities) > 0
	assuranceSatisfiedBy := func(value string) bool {
		return assuranceLattice.SatisfiedBy != nil && *assuranceLattice.SatisfiedBy == value
	}

	verdict := VerdictVerified
	switch {
	case len(blockers) > 0:
		verdict = VerdictBlocked
	case len(contradicting) > 0 || invariantFailed:
		if assuranceRequired && !assuranceSatisfiedBy("contradiction") {
			caveats = append(caveats, "required_assurance_not_satisfied")
			verdict = VerdictUnresolved
		} else {
			verdict = VerdictContradicted
		}
	default:
		caveats = append(caveats, incompleteProofCaveats(
			&bundle,
			supporting,
			invariantUnresolved,
			assuranceRequired,
			assuranceSatisfiedBy,
		)...)
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
	if assuranceRequired {
		proofPayload["assurance_lattice"] = assuranceLatticeMap(&assuranceLattice)
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
		AssuranceLattice:            assuranceLattice,
	}, nil
}
