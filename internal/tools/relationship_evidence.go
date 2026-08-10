package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/evidence"
	"github.com/DeusData/codebase-memory-mcp/internal/indexidentity"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerRelationshipEvidenceTool() {
	s.addTool(&mcp.Tool{
		Name: "get_relationship_evidence",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Return generation-bound evidence for graph relationships adjacent to one exact qualified symbol. Preserves the edge's resolver strategy/rule, confidence tier, and OpenTelemetry confirmation (`validated_by_trace`, call count) in canonical relationship/evidence/observation references. Use this after search_graph or get_code_snippet when a PROVE or change-impact workflow needs to distinguish compiler/LSP-resolved, AST-extracted, inferred, ambiguous, and runtime-observed relationships. Fails closed when the live checkout no longer matches the indexed generation.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project name. Defaults to the session project."
				},
				"qualified_name": {
					"type": "string",
					"description": "Exact canonical qualified name of the focal symbol."
				},
				"direction": {
					"type": "string",
					"enum": ["outbound", "inbound", "both"],
					"default": "both",
					"description": "Which adjacent graph edges to return."
				},
				"relationship_types": {
					"type": "array",
					"items": {"type": "string"},
					"uniqueItems": true,
					"description": "Optional edge-type allowlist, for example CALLS, HTTP_CALLS, ASYNC_CALLS, TESTS, POLICY_GATES."
				},
				"related_qualified_name": {
					"type": "string",
					"description": "Optional exact qualified-name filter for the symbol on the other end of the relationship."
				},
				"limit": {
					"type": "integer",
					"minimum": 1,
					"maximum": 500,
					"default": 100
				}
			},
			"required": ["qualified_name"]
		}`),
	}, s.handleGetRelationshipEvidence)
}

func relationshipTypeSet(args map[string]any) map[string]bool {
	raw, ok := args["relationship_types"]
	if !ok {
		return nil
	}
	result := map[string]bool{}
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				result[strings.ToUpper(strings.TrimSpace(text))] = true
			}
		}
	case []string:
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				result[strings.ToUpper(strings.TrimSpace(value))] = true
			}
		}
	}
	return result
}

func relationshipConfidenceBand(edge *store.Edge) string {
	if edge == nil {
		return "unknown"
	}
	if runtimeObserved(edge.Properties) {
		return "high"
	}
	if raw, ok := edge.Properties["confidence_band"].(string); ok {
		band := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case band == "high", band == "medium", band == "low", band == "unknown":
			return band
		case strings.HasPrefix(band, "speculative"):
			return "speculative"
		}
	}
	switch edge.ConfidenceTier() {
	case store.ConfidenceExtracted:
		return "high"
	case store.ConfidenceInferred:
		return "medium"
	case store.ConfidenceAmbiguous:
		return "low"
	default:
		return "unknown"
	}
}

func relationshipResolutionSource(edge *store.Edge) string {
	if edge == nil {
		return "unknown"
	}
	base := ""
	for _, key := range []string{"resolution_strategy", "resolver_rule"} {
		if value, ok := edge.Properties[key].(string); ok && strings.TrimSpace(value) != "" {
			base = strings.TrimSpace(value)
			break
		}
	}
	if base == "" {
		base = strings.ToLower(edge.ConfidenceTier())
	}
	if runtimeObserved(edge.Properties) {
		return base + "+runtime_trace"
	}
	return base
}

func runtimeObserved(properties map[string]any) bool {
	value, _ := properties["validated_by_trace"].(bool)
	return value
}

func relationshipObservationCount(properties map[string]any) int {
	value := properties["trace_call_count"]
	switch count := value.(type) {
	case int:
		if count > 0 {
			return count
		}
	case int64:
		if count > 0 {
			return int(count)
		}
	case float64:
		if count > 0 {
			return int(count)
		}
	}
	return 0
}

func nodeSymbolRef(identity *indexidentity.Envelope, node *store.Node) evidence.SymbolRef {
	return evidence.NewSymbolRef(
		identity.RepositoryID,
		identity.SourceRevision,
		node.FilePath,
		node.Label,
		node.QualifiedName,
		node.StartLine,
		node.EndLine,
	)
}

func relationshipEntry(
	identity *indexidentity.Envelope,
	edge *store.Edge,
	source, target *store.Node,
) map[string]any {
	sourceRef := nodeSymbolRef(identity, source)
	targetRef := nodeSymbolRef(identity, target)
	resolutionSource := relationshipResolutionSource(edge)
	confidenceBand := relationshipConfidenceBand(edge)
	runtimeWasObserved := runtimeObserved(edge.Properties)
	observationCount := relationshipObservationCount(edge.Properties)
	relationshipRef := evidence.NewRelationshipRef(
		identity.RepositoryID,
		identity.SourceRevision,
		identity.IndexGeneration,
		edge.Type,
		sourceRef,
		targetRef,
		resolutionSource,
		confidenceBand,
		runtimeWasObserved,
		observationCount,
	)
	evidenceType := "static_relationship"
	if runtimeWasObserved {
		evidenceType = "runtime_validated_relationship"
	}
	evidenceRef := evidence.NewRelationshipEvidenceRef(
		relationshipRef,
		evidenceType,
	)
	observationRef := evidence.NewObservationRef(
		evidenceRef,
		"support",
		"code-graph",
		resolutionSource,
		confidenceBand,
	)
	entry := map[string]any{
		"relation_type": edge.Type,
		"source": map[string]any{
			"name":           source.Name,
			"qualified_name": source.QualifiedName,
			"file_path":      source.FilePath,
			"start_line":     source.StartLine,
			"end_line":       source.EndLine,
		},
		"target": map[string]any{
			"name":           target.Name,
			"qualified_name": target.QualifiedName,
			"file_path":      target.FilePath,
			"start_line":     target.StartLine,
			"end_line":       target.EndLine,
		},
		"resolution_source": resolutionSource,
		"confidence_band":   confidenceBand,
		"confidence_tier":   strings.ToLower(edge.ConfidenceTier()),
		"runtime_observed":  runtimeWasObserved,
		"observation_count": observationCount,
		"relationship_ref":  relationshipRef,
		"evidence_ref":      evidenceRef,
		"observation_ref":   observationRef,
	}
	if value, ok := edge.Properties["p99_latency_ns"]; ok {
		entry["p99_latency_ns"] = value
	}
	if value, ok := edge.Properties["confidence"]; ok {
		entry["numeric_confidence"] = value
	}
	return entry
}

func relationshipIdentityBlockedResult(identityData map[string]any) *mcp.CallToolResult {
	reason := stringResultField(identityData["identity_status"])
	if detail := stringResultField(identityData["identity_reason"]); detail != "" {
		if reason == "" {
			reason = detail
		} else {
			reason += ":" + detail
		}
	}
	if reason == "" {
		reason = "index_identity_not_ready"
	}
	identityData["relationships"] = []any{}
	identityData["total"] = 0
	identityData["_metadata"] = map[string]any{
		"evidence_refs": map[string]any{
			"schema_version": evidence.SchemaVersion,
			"emitted":        false,
			"count":          0,
			"reason":         reason,
		},
	}
	return jsonResult(identityData)
}

func (s *Server) handleGetRelationshipEvidence(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}
	qualifiedName := strings.TrimSpace(getStringArg(args, "qualified_name"))
	if qualifiedName == "" {
		return errResult("qualified_name is required"), nil
	}
	direction := strings.ToLower(strings.TrimSpace(getStringArg(args, "direction")))
	if direction == "" {
		direction = "both"
	}
	if direction != "outbound" && direction != "inbound" && direction != "both" {
		return errResult("direction must be outbound, inbound, or both"), nil
	}
	limit := getIntArg(args, "limit", 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}

	projectArg := getStringArg(args, "project")
	st, err := s.resolveStore(projectArg)
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}
	projectName := s.resolveProjectName(projectArg)
	if projectName == "" {
		projectName = s.sessionProject
	}
	project, err := st.GetProject(projectName)
	if err != nil || project == nil {
		return errResult("project metadata is unavailable"), nil
	}

	identityData := map[string]any{}
	if !s.addLiveIndexIdentity(identityData, st, projectName, project.RootPath) {
		return relationshipIdentityBlockedResult(identityData), nil
	}
	identity, ok := identityData["index_identity"].(*indexidentity.Envelope)
	if !ok || identity == nil {
		return errResult("ready index identity is unavailable"), nil
	}

	focal, err := st.FindNodeByQN(projectName, qualifiedName)
	if err != nil {
		return errResult(fmt.Sprintf("find symbol: %v", err)), nil
	}
	if focal == nil {
		return errResult("qualified_name was not found in the indexed graph"), nil
	}

	type candidate struct {
		edge   *store.Edge
		source *store.Node
		target *store.Node
		other  *store.Node
	}
	seen := map[int64]bool{}
	candidates := []candidate{}
	addEdges := func(edges []*store.Edge, inbound bool) error {
		for _, edge := range edges {
			if seen[edge.ID] {
				continue
			}
			seen[edge.ID] = true
			source, findErr := st.FindNodeByID(edge.SourceID)
			if findErr != nil || source == nil {
				continue
			}
			target, findErr := st.FindNodeByID(edge.TargetID)
			if findErr != nil || target == nil {
				continue
			}
			other := target
			if inbound {
				other = source
			}
			candidates = append(candidates, candidate{edge: edge, source: source, target: target, other: other})
		}
		return nil
	}
	if direction == "outbound" || direction == "both" {
		edges, findErr := st.FindEdgesBySource(focal.ID)
		if findErr != nil {
			return errResult(fmt.Sprintf("find outbound relationships: %v", findErr)), nil
		}
		_ = addEdges(edges, false)
	}
	if direction == "inbound" || direction == "both" {
		edges, findErr := st.FindEdgesByTarget(focal.ID)
		if findErr != nil {
			return errResult(fmt.Sprintf("find inbound relationships: %v", findErr)), nil
		}
		_ = addEdges(edges, true)
	}

	allowedTypes := relationshipTypeSet(args)
	relatedQN := strings.TrimSpace(getStringArg(args, "related_qualified_name"))
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].edge.Type != candidates[j].edge.Type {
			return candidates[i].edge.Type < candidates[j].edge.Type
		}
		return candidates[i].other.QualifiedName < candidates[j].other.QualifiedName
	})

	finalIdentityData := map[string]any{}
	if !s.addLiveIndexIdentity(finalIdentityData, st, projectName, project.RootPath) {
		return relationshipIdentityBlockedResult(finalIdentityData), nil
	}
	finalIdentity, ok := finalIdentityData["index_identity"].(*indexidentity.Envelope)
	if !ok || finalIdentity == nil {
		finalIdentityData["identity_status"] = indexidentity.StatusError
		finalIdentityData["identity_reason"] = "ready index identity is unavailable after graph traversal"
		return relationshipIdentityBlockedResult(finalIdentityData), nil
	}
	if !sameSourceIdentity(identity, finalIdentity) {
		finalIdentityData["identity_status"] = indexidentity.StatusStaleSource
		finalIdentityData["identity_reason"] = fmt.Sprintf(
			"source_or_index_changed_during_relationship_query: %s; retry against a stable checkout",
			describeSourceIdentityDifferences("initial", identity, "final", finalIdentity),
		)
		return relationshipIdentityBlockedResult(finalIdentityData), nil
	}
	identity = finalIdentity

	results := make([]map[string]any, 0, len(candidates))
	skippedUnaddressable := 0
	for _, candidate := range candidates {
		if len(results) >= limit {
			break
		}
		if len(allowedTypes) > 0 && !allowedTypes[strings.ToUpper(candidate.edge.Type)] {
			continue
		}
		if relatedQN != "" && candidate.other.QualifiedName != relatedQN {
			continue
		}
		if !store.IsSurfaceableCodeNode(candidate.source.Label, candidate.source.FilePath) ||
			!store.IsSurfaceableCodeNode(candidate.target.Label, candidate.target.FilePath) {
			skippedUnaddressable++
			continue
		}
		results = append(results, relationshipEntry(
			identity,
			candidate.edge,
			candidate.source,
			candidate.target,
		))
	}

	metadata := s.stdReadGraphMetadata(projectName)
	metadata["evidence_refs"] = map[string]any{
		"schema_version":   1,
		"emitted":          len(results) > 0,
		"count":            len(results),
		"index_generation": identity.IndexGeneration,
	}
	response := map[string]any{
		"project":               projectName,
		"qualified_name":        focal.QualifiedName,
		"direction":             direction,
		"relationships":         results,
		"total":                 len(results),
		"skipped_unaddressable": skippedUnaddressable,
		"index_identity":        identity,
		"_metadata":             metadata,
	}
	return jsonResult(response), nil
}
