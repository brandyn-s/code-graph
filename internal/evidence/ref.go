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

type EvidenceRef struct {
	ID              string     `json:"id"`
	SchemaVersion   int        `json:"schema_version"`
	RepositoryID    string     `json:"repository_id"`
	SourceRevision  string     `json:"source_revision"`
	IndexGeneration string     `json:"index_generation"`
	RelativePath    string     `json:"relative_path"`
	StartLine       int        `json:"start_line"`
	EndLine         int        `json:"end_line"`
	EvidenceType    string     `json:"evidence_type"`
	SymbolRef       *SymbolRef `json:"symbol_ref,omitempty"`
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
