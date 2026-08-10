package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const SchemaVersion = 1

type SymbolRef struct {
	ID             string `json:"id"`
	SchemaVersion  int    `json:"schema_version"`
	RepositoryID   string `json:"repository_id"`
	SourceRevision string `json:"source_revision"`
	RelativePath   string `json:"relative_path"`
	SymbolKind     string `json:"symbol_kind"`
	QualifiedName  string `json:"qualified_name"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
}

// RelationshipRef is an immutable assertion about one graph edge in one index
// generation. It carries the resolver provenance that ordinary source-location
// evidence cannot express and can record runtime confirmation without replacing
// the underlying static relationship.
type RelationshipRef struct {
	ID               string    `json:"id"`
	SchemaVersion    int       `json:"schema_version"`
	RepositoryID     string    `json:"repository_id"`
	SourceRevision   string    `json:"source_revision"`
	IndexGeneration  string    `json:"index_generation"`
	RelationType     string    `json:"relation_type"`
	SourceSymbolRef  SymbolRef `json:"source_symbol_ref"`
	TargetSymbolRef  SymbolRef `json:"target_symbol_ref"`
	ResolutionSource string    `json:"resolution_source"`
	ConfidenceBand   string    `json:"confidence_band"`
	RuntimeObserved  bool      `json:"runtime_observed"`
	ObservationCount int       `json:"observation_count"`
}

type EvidenceRef struct {
	ID              string           `json:"id"`
	SchemaVersion   int              `json:"schema_version"`
	RepositoryID    string           `json:"repository_id"`
	SourceRevision  string           `json:"source_revision"`
	IndexGeneration string           `json:"index_generation"`
	RelativePath    string           `json:"relative_path"`
	StartLine       int              `json:"start_line"`
	EndLine         int              `json:"end_line"`
	EvidenceType    string           `json:"evidence_type"`
	SymbolRef       *SymbolRef       `json:"symbol_ref,omitempty"`
	RelationshipRef *RelationshipRef `json:"relationship_ref,omitempty"`
}

type ObservationRef struct {
	ID             string      `json:"id"`
	SchemaVersion  int         `json:"schema_version"`
	EvidenceRef    EvidenceRef `json:"evidence_ref"`
	Stance         string      `json:"stance"`
	SourceEngine   string      `json:"source_engine"`
	Derivation     string      `json:"derivation"`
	ConfidenceBand string      `json:"confidence_band"`
}

type ClaimRef struct {
	ID            string `json:"id"`
	SchemaVersion int    `json:"schema_version"`
	RepositoryID  string `json:"repository_id"`
	ClaimKind     string `json:"claim_kind"`
	ClaimText     string `json:"claim_text"`
}

func canonicalPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	for strings.HasPrefix(value, "./") {
		value = strings.TrimPrefix(value, "./")
	}
	return value
}

func canonicalText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func canonicalToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// stableID marshals maps because encoding/json sorts map keys recursively.
// Python uses json.dumps(sort_keys=True) over the same payload, so both engines
// derive identical IDs for identical evidence, including nested references.
func stableID(prefix string, payload map[string]any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(encoded)
	return prefix + ":v1:" + hex.EncodeToString(digest[:])
}

func symbolRefMap(ref *SymbolRef) map[string]any {
	if ref == nil {
		return nil
	}
	return map[string]any{
		"id":              ref.ID,
		"schema_version":  ref.SchemaVersion,
		"repository_id":   ref.RepositoryID,
		"source_revision": ref.SourceRevision,
		"relative_path":   ref.RelativePath,
		"symbol_kind":     ref.SymbolKind,
		"qualified_name":  ref.QualifiedName,
		"start_line":      ref.StartLine,
		"end_line":        ref.EndLine,
	}
}

func relationshipRefMap(ref *RelationshipRef) map[string]any {
	if ref == nil {
		return nil
	}
	return map[string]any{
		"id":                ref.ID,
		"schema_version":    ref.SchemaVersion,
		"repository_id":     ref.RepositoryID,
		"source_revision":   ref.SourceRevision,
		"index_generation":  ref.IndexGeneration,
		"relation_type":     ref.RelationType,
		"source_symbol_ref": symbolRefMap(&ref.SourceSymbolRef),
		"target_symbol_ref": symbolRefMap(&ref.TargetSymbolRef),
		"resolution_source": ref.ResolutionSource,
		"confidence_band":   ref.ConfidenceBand,
		"runtime_observed":  ref.RuntimeObserved,
		"observation_count": ref.ObservationCount,
	}
}

func evidenceRefMap(ref *EvidenceRef) map[string]any {
	if ref == nil {
		return nil
	}
	payload := map[string]any{
		"id":               ref.ID,
		"schema_version":   ref.SchemaVersion,
		"repository_id":    ref.RepositoryID,
		"source_revision":  ref.SourceRevision,
		"index_generation": ref.IndexGeneration,
		"relative_path":    ref.RelativePath,
		"start_line":       ref.StartLine,
		"end_line":         ref.EndLine,
		"evidence_type":    ref.EvidenceType,
	}
	if ref.SymbolRef != nil {
		payload["symbol_ref"] = symbolRefMap(ref.SymbolRef)
	}
	if ref.RelationshipRef != nil {
		payload["relationship_ref"] = relationshipRefMap(ref.RelationshipRef)
	}
	return payload
}

func NewSymbolRef(repositoryID, sourceRevision, relativePath, symbolKind, qualifiedName string, startLine, endLine int) SymbolRef {
	ref := SymbolRef{
		SchemaVersion:  SchemaVersion,
		RepositoryID:   repositoryID,
		SourceRevision: sourceRevision,
		RelativePath:   canonicalPath(relativePath),
		SymbolKind:     strings.ToLower(strings.TrimSpace(symbolKind)),
		QualifiedName:  strings.TrimSpace(qualifiedName),
		StartLine:      startLine,
		EndLine:        endLine,
	}
	payload := symbolRefMap(&ref)
	delete(payload, "id")
	ref.ID = stableID("sym", payload)
	return ref
}

// NewRelationshipRef copies both symbol refs so later caller mutation cannot
// change the canonical relationship contents after its ID is derived.
//
//nolint:gocritic // Value copies preserve the immutable reference contract.
func NewRelationshipRef(
	repositoryID, sourceRevision, indexGeneration, relationType string,
	sourceSymbolRef, targetSymbolRef SymbolRef,
	resolutionSource, confidenceBand string,
	runtimeObserved bool,
	observationCount int,
) RelationshipRef {
	if observationCount < 0 {
		observationCount = 0
	}
	ref := RelationshipRef{
		SchemaVersion:    SchemaVersion,
		RepositoryID:     repositoryID,
		SourceRevision:   sourceRevision,
		IndexGeneration:  indexGeneration,
		RelationType:     canonicalToken(relationType),
		SourceSymbolRef:  sourceSymbolRef,
		TargetSymbolRef:  targetSymbolRef,
		ResolutionSource: canonicalToken(resolutionSource),
		ConfidenceBand:   canonicalToken(confidenceBand),
		RuntimeObserved:  runtimeObserved,
		ObservationCount: observationCount,
	}
	payload := relationshipRefMap(&ref)
	delete(payload, "id")
	ref.ID = stableID("rel", payload)
	return ref
}

func NewEvidenceRef(repositoryID, sourceRevision, indexGeneration, relativePath string, startLine, endLine int, evidenceType string, symbolRef *SymbolRef) EvidenceRef {
	ref := EvidenceRef{
		SchemaVersion:   SchemaVersion,
		RepositoryID:    repositoryID,
		SourceRevision:  sourceRevision,
		IndexGeneration: indexGeneration,
		RelativePath:    canonicalPath(relativePath),
		StartLine:       startLine,
		EndLine:         endLine,
		EvidenceType:    canonicalToken(evidenceType),
		SymbolRef:       symbolRef,
	}
	payload := evidenceRefMap(&ref)
	delete(payload, "id")
	ref.ID = stableID("ev", payload)
	return ref
}

// NewRelationshipEvidenceRef copies the relationship into the evidence ref so
// the generated evidence ID remains stable after construction.
//
//nolint:gocritic // Value copy preserves the immutable reference contract.
func NewRelationshipEvidenceRef(relationshipRef RelationshipRef, evidenceType string) EvidenceRef {
	source := relationshipRef.SourceSymbolRef
	ref := EvidenceRef{
		SchemaVersion:   SchemaVersion,
		RepositoryID:    relationshipRef.RepositoryID,
		SourceRevision:  relationshipRef.SourceRevision,
		IndexGeneration: relationshipRef.IndexGeneration,
		RelativePath:    source.RelativePath,
		StartLine:       source.StartLine,
		EndLine:         source.EndLine,
		EvidenceType:    canonicalToken(evidenceType),
		SymbolRef:       &source,
		RelationshipRef: &relationshipRef,
	}
	payload := evidenceRefMap(&ref)
	delete(payload, "id")
	ref.ID = stableID("ev", payload)
	return ref
}

// NewObservationRef copies its evidence so the observation remains bound to
// the exact canonical evidence contents used to derive its ID.
//
//nolint:gocritic // Value copy preserves the immutable reference contract.
func NewObservationRef(evidenceRef EvidenceRef, stance, sourceEngine, derivation, confidenceBand string) ObservationRef {
	ref := ObservationRef{
		SchemaVersion:  SchemaVersion,
		EvidenceRef:    evidenceRef,
		Stance:         canonicalToken(stance),
		SourceEngine:   canonicalToken(sourceEngine),
		Derivation:     canonicalToken(derivation),
		ConfidenceBand: canonicalToken(confidenceBand),
	}
	payload := map[string]any{
		"schema_version":  ref.SchemaVersion,
		"evidence_ref":    evidenceRefMap(&ref.EvidenceRef),
		"stance":          ref.Stance,
		"source_engine":   ref.SourceEngine,
		"derivation":      ref.Derivation,
		"confidence_band": ref.ConfidenceBand,
	}
	ref.ID = stableID("obs", payload)
	return ref
}

func NewClaimRef(repositoryID, claimKind, claimText string) ClaimRef {
	ref := ClaimRef{
		SchemaVersion: SchemaVersion,
		RepositoryID:  repositoryID,
		ClaimKind:     canonicalToken(claimKind),
		ClaimText:     canonicalText(claimText),
	}
	payload := map[string]any{
		"schema_version": ref.SchemaVersion,
		"repository_id":  ref.RepositoryID,
		"claim_kind":     ref.ClaimKind,
		"claim_text":     ref.ClaimText,
	}
	ref.ID = stableID("claim", payload)
	return ref
}
