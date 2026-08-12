package tools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
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
		Description: "Return generation-bound evidence for graph relationships adjacent to one exact qualified symbol. Preserves the edge's resolver strategy/rule, immutable compiler-artifact digest when present, confidence tier, and OpenTelemetry confirmation (`validated_by_trace`, call count) in canonical relationship/evidence/observation references. Use this after search_graph or get_code_snippet when a PROVE or change-impact workflow needs to distinguish compiler/LSP-resolved, AST-extracted, inferred, ambiguous, and runtime-observed relationships. Fails closed when the live checkout no longer matches the indexed generation.",
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

func relationshipResolutionArtifact(edge *store.Edge, resolutionSource string) string {
	if edge == nil {
		return ""
	}
	value, _ := edge.Properties["resolution_artifact_sha256"].(string)
	digest := strings.TrimSpace(value)
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || strings.ToLower(digest) != digest {
		return ""
	}
	if !slices.Contains(strings.Split(resolutionSource, "+"), "scip-ingest") {
		return ""
	}
	return digest
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
	resolutionArtifactSHA256 := relationshipResolutionArtifact(
		edge,
		resolutionSource,
	)
	confidenceBand := relationshipConfidenceBand(edge)
	runtimeWasObserved := runtimeObserved(edge.Properties)
	observationCount := relationshipObservationCount(edge.Properties)
	relationshipRef := evidence.NewRelationshipRefWithArtifact(
		identity.RepositoryID,
		identity.SourceRevision,
		identity.IndexGeneration,
		edge.Type,
		sourceRef,
		targetRef,
		resolutionSource,
		resolutionArtifactSHA256,
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
	if resolutionArtifactSHA256 != "" {
		entry["resolution_artifact_sha256"] = resolutionArtifactSHA256
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

type relationshipQuery struct {
	qualifiedName string
	direction     string
	allowedTypes  map[string]bool
	relatedQN     string
	limit         int
}

func parseRelationshipQuery(args map[string]any) (relationshipQuery, error) {
	query := relationshipQuery{
		qualifiedName: strings.TrimSpace(getStringArg(args, "qualified_name")),
		direction:     strings.ToLower(strings.TrimSpace(getStringArg(args, "direction"))),
		allowedTypes:  relationshipTypeSet(args),
		relatedQN:     strings.TrimSpace(getStringArg(args, "related_qualified_name")),
		limit:         getIntArg(args, "limit", 100),
	}
	if query.qualifiedName == "" {
		return relationshipQuery{}, fmt.Errorf("qualified_name is required")
	}
	if query.direction == "" {
		query.direction = "both"
	}
	if query.direction != "outbound" && query.direction != "inbound" && query.direction != "both" {
		return relationshipQuery{}, fmt.Errorf("direction must be outbound, inbound, or both")
	}
	if query.limit < 1 {
		query.limit = 1
	}
	if query.limit > 500 {
		query.limit = 500
	}
	return query, nil
}

type relationshipCandidate struct {
	edge   *store.Edge
	source *store.Node
	target *store.Node
	other  *store.Node
}

func appendRelationshipCandidates(
	st *store.Store,
	candidates *[]relationshipCandidate,
	seen map[int64]bool,
	edges []*store.Edge,
	inbound bool,
) {
	for _, edge := range edges {
		if seen[edge.ID] {
			continue
		}
		seen[edge.ID] = true
		source, err := st.FindNodeByID(edge.SourceID)
		if err != nil || source == nil {
			continue
		}
		target, err := st.FindNodeByID(edge.TargetID)
		if err != nil || target == nil {
			continue
		}
		other := target
		if inbound {
			other = source
		}
		*candidates = append(*candidates, relationshipCandidate{
			edge: edge, source: source, target: target, other: other,
		})
	}
}

func collectRelationshipCandidates(
	st *store.Store,
	focal *store.Node,
	direction string,
) ([]relationshipCandidate, error) {
	seen := map[int64]bool{}
	candidates := []relationshipCandidate{}
	if direction == "outbound" || direction == "both" {
		edges, err := st.FindEdgesBySource(focal.ID)
		if err != nil {
			return nil, fmt.Errorf("find outbound relationships: %w", err)
		}
		appendRelationshipCandidates(st, &candidates, seen, edges, false)
	}
	if direction == "inbound" || direction == "both" {
		edges, err := st.FindEdgesByTarget(focal.ID)
		if err != nil {
			return nil, fmt.Errorf("find inbound relationships: %w", err)
		}
		appendRelationshipCandidates(st, &candidates, seen, edges, true)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].edge.Type != candidates[j].edge.Type {
			return candidates[i].edge.Type < candidates[j].edge.Type
		}
		return candidates[i].other.QualifiedName < candidates[j].other.QualifiedName
	})
	return candidates, nil
}

func relationshipResults(
	identity *indexidentity.Envelope,
	query relationshipQuery,
	candidates []relationshipCandidate,
) (results []map[string]any, skippedUnaddressable int) {
	results = make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		if len(results) >= query.limit {
			break
		}
		if len(query.allowedTypes) > 0 && !query.allowedTypes[strings.ToUpper(candidate.edge.Type)] {
			continue
		}
		if query.relatedQN != "" && candidate.other.QualifiedName != query.relatedQN {
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
	return results, skippedUnaddressable
}

func (s *Server) handleGetRelationshipEvidence(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}
	query, err := parseRelationshipQuery(args)
	if err != nil {
		return errResult(err.Error()), nil
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
	if err != nil {
		return errResult(fmt.Sprintf("project metadata: %v", err)), nil
	}
	if project == nil {
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

	focal, err := st.FindNodeByQN(projectName, query.qualifiedName)
	if err != nil {
		return errResult(fmt.Sprintf("find symbol: %v", err)), nil
	}
	if focal == nil {
		return errResult("qualified_name was not found in the indexed graph"), nil
	}

	candidates, err := collectRelationshipCandidates(st, focal, query.direction)
	if err != nil {
		return errResult(err.Error()), nil
	}

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

	results, skippedUnaddressable := relationshipResults(identity, query, candidates)

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
		"direction":             query.direction,
		"relationships":         results,
		"total":                 len(results),
		"skipped_unaddressable": skippedUnaddressable,
		"index_identity":        identity,
		"_metadata":             metadata,
	}
	return jsonResult(response), nil
}
