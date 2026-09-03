package tools

import (
	"fmt"

	"github.com/brandyn-s/code-graph/internal/evidence"
	"github.com/brandyn-s/code-graph/internal/indexidentity"
	"github.com/brandyn-s/code-graph/internal/store"
)

func copyStringAnyMap(source map[string]any) map[string]any {
	copied := make(map[string]any, len(source))
	for key, value := range source {
		copied[key] = value
	}
	return copied
}

func intResultField(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func stringResultField(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}

// withGraphEvidenceRefs returns a copy of a search_graph response carrying
// canonical generation-bound evidence. It performs a live identity check so a
// cached query result can never replay evidence from a stale checkout.
func (s *Server) withGraphEvidenceRefs(
	responseData map[string]any,
	st *store.Store,
	project string,
	rootPath string,
) map[string]any {
	annotated := copyStringAnyMap(responseData)
	metadata := map[string]any{}
	if existing, ok := responseData["_metadata"].(map[string]any); ok {
		metadata = copyStringAnyMap(existing)
	}
	annotated["_metadata"] = metadata

	refsMetadata := map[string]any{
		"schema_version": evidence.SchemaVersion,
		"emitted":        false,
		"count":          0,
	}
	metadata["evidence_refs"] = refsMetadata

	identityState := map[string]any{}
	if !s.addLiveIndexIdentity(identityState, st, project, rootPath) {
		reason := stringResultField(identityState["identity_status"])
		if detail := stringResultField(identityState["identity_reason"]); detail != "" {
			reason = fmt.Sprintf("%s:%s", reason, detail)
		}
		if reason == "" {
			reason = "index_identity_not_ready"
		}
		refsMetadata["reason"] = reason
		return annotated
	}

	identity, ok := identityState["index_identity"].(*indexidentity.Envelope)
	if !ok || identity == nil {
		refsMetadata["reason"] = "index_identity_missing"
		return annotated
	}

	results, ok := responseData["results"].([]map[string]any)
	if !ok {
		refsMetadata["reason"] = "unsupported_result_shape"
		return annotated
	}
	annotatedResults := make([]map[string]any, 0, len(results))
	emitted := 0
	for _, result := range results {
		entry := copyStringAnyMap(result)
		relativePath := stringResultField(entry["file_path"])
		qualifiedName := stringResultField(entry["qualified_name"])
		if qualifiedName == "" {
			qualifiedName = stringResultField(entry["name"])
		}
		startLine := intResultField(entry["start_line"])
		endLine := intResultField(entry["end_line"])
		if relativePath == "" || qualifiedName == "" || startLine < 0 || endLine < startLine {
			annotatedResults = append(annotatedResults, entry)
			continue
		}
		symbolRef := evidence.NewSymbolRef(
			identity.RepositoryID,
			identity.SourceRevision,
			relativePath,
			stringResultField(entry["label"]),
			qualifiedName,
			startLine,
			endLine,
		)
		evidenceRef := evidence.NewEvidenceRef(
			identity.RepositoryID,
			identity.SourceRevision,
			identity.IndexGeneration,
			relativePath,
			startLine,
			endLine,
			"graph_match",
			&symbolRef,
		)
		observationRef := evidence.NewObservationRef(
			evidenceRef,
			"support",
			"code-graph",
			"graph_filter_match",
			"unknown",
		)
		entry["symbol_ref"] = symbolRef
		entry["evidence_ref"] = evidenceRef
		entry["observation_ref"] = observationRef
		annotatedResults = append(annotatedResults, entry)
		emitted++
	}
	annotated["results"] = annotatedResults
	refsMetadata["emitted"] = true
	refsMetadata["count"] = emitted
	refsMetadata["index_generation"] = identity.IndexGeneration
	return annotated
}
