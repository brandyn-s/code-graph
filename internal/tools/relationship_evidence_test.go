package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/indexidentity"
	"github.com/brandyn-s/code-graph/internal/store"
)

func seedRelationshipProperties(
	t *testing.T,
	s *Server,
	properties map[string]any,
) {
	t.Helper()
	st, err := s.router.ForProject("test")
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	source, err := st.FindNodeByQN("test", "test.handler.handle_request")
	if err != nil || source == nil {
		t.Fatalf("source node: %v %#v", err, source)
	}
	target, err := st.FindNodeByQN("test", "test.auth.authenticate")
	if err != nil || target == nil {
		t.Fatalf("target node: %v %#v", err, target)
	}
	_, err = st.InsertEdge(&store.Edge{
		Project:    "test",
		SourceID:   source.ID,
		TargetID:   target.ID,
		Type:       "CALLS",
		Properties: properties,
	})
	if err != nil {
		t.Fatalf("InsertEdge: %v", err)
	}
}

func firstRelationship(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	relationships, ok := response["relationships"].([]any)
	if !ok || len(relationships) == 0 {
		t.Fatalf("relationships missing or empty: %#v", response["relationships"])
	}
	entry, ok := relationships[0].(map[string]any)
	if !ok {
		t.Fatalf("first relationship is %T", relationships[0])
	}
	return entry
}

func TestRelationshipEvidencePreservesResolverAndRuntimeProvenance(t *testing.T) {
	s := serverWithReadyEvidenceIdentity(t)
	seedRelationshipProperties(t, s, map[string]any{
		"resolution_strategy": "go_lsp_cross_file",
		"resolver_rule":       "go-lsp",
		"confidence_tier":     store.ConfidenceInferred,
		"validated_by_trace":  true,
		"trace_call_count":    17,
		"p99_latency_ns":      4200000,
	})

	response := metadataResponseFromHandler(
		t,
		s.handleGetRelationshipEvidence,
		"get_relationship_evidence",
		map[string]any{
			"project":                "test",
			"qualified_name":         "test.handler.handle_request",
			"direction":              "outbound",
			"relationship_types":     []any{"CALLS"},
			"related_qualified_name": "test.auth.authenticate",
		},
	)
	entry := firstRelationship(t, response)
	if entry["resolution_source"] != "go_lsp_cross_file+runtime_trace" {
		t.Fatalf("resolution_source = %v", entry["resolution_source"])
	}
	if entry["confidence_band"] != "high" {
		t.Fatalf("confidence_band = %v", entry["confidence_band"])
	}
	if entry["runtime_observed"] != true {
		t.Fatalf("runtime_observed = %v", entry["runtime_observed"])
	}
	if entry["observation_count"] != float64(17) {
		t.Fatalf("observation_count = %v", entry["observation_count"])
	}
	for _, field := range []string{"relationship_ref", "evidence_ref", "observation_ref"} {
		ref, ok := entry[field].(map[string]any)
		if !ok {
			t.Fatalf("%s = %T", field, entry[field])
		}
		id, _ := ref["id"].(string)
		if id == "" {
			t.Fatalf("%s missing id", field)
		}
	}
	relationshipRef := requireMapValue(t, entry["relationship_ref"], "relationship_ref")
	if !strings.HasPrefix(requireStringValue(t, relationshipRef["id"], "relationship_ref.id"), "rel:v1:") {
		t.Fatalf("relationship id = %v", relationshipRef["id"])
	}
	if relationshipRef["runtime_observed"] != true ||
		relationshipRef["observation_count"] != float64(17) {
		t.Fatalf("runtime relationship ref = %#v", relationshipRef)
	}
	evidenceRef := requireMapValue(t, entry["evidence_ref"], "evidence_ref")
	if evidenceRef["index_generation"] != testIndexIdentity("clean").IndexGeneration {
		t.Fatalf("generation = %v", evidenceRef["index_generation"])
	}
	if _, ok := evidenceRef["relationship_ref"].(map[string]any); !ok {
		t.Fatalf("nested relationship ref missing: %#v", evidenceRef)
	}
	metadata := requireMapValue(t, response["_metadata"], "_metadata")
	refs := requireMapValue(t, metadata["evidence_refs"], "evidence_refs")
	if refs["emitted"] != true || refs["count"] != float64(1) {
		t.Fatalf("evidence metadata = %#v", refs)
	}
}

func TestRelationshipEvidenceMapsStaticInferenceConfidence(t *testing.T) {
	s := serverWithReadyEvidenceIdentity(t)
	seedRelationshipProperties(t, s, map[string]any{
		"resolution_strategy": "cross_package_import_map",
		"confidence_tier":     store.ConfidenceInferred,
		"confidence_band":     nil,
		"validated_by_trace":  false,
		"trace_call_count":    nil,
		"p99_latency_ns":      nil,
	})
	response := metadataResponseFromHandler(
		t,
		s.handleGetRelationshipEvidence,
		"get_relationship_evidence",
		map[string]any{
			"project":                "test",
			"qualified_name":         "test.handler.handle_request",
			"direction":              "outbound",
			"related_qualified_name": "test.auth.authenticate",
		},
	)
	entry := firstRelationship(t, response)
	if entry["runtime_observed"] != false {
		t.Fatalf("runtime_observed = %v", entry["runtime_observed"])
	}
	if entry["confidence_band"] != "medium" {
		t.Fatalf("confidence_band = %v", entry["confidence_band"])
	}
	if entry["resolution_source"] != "cross_package_import_map" {
		t.Fatalf("resolution_source = %v", entry["resolution_source"])
	}
}

func TestRelationshipEvidenceBindsSCIPArtifactDigest(t *testing.T) {
	s := serverWithReadyEvidenceIdentity(t)
	digest := strings.Repeat("a", 64)
	seedRelationshipProperties(t, s, map[string]any{
		"resolver_rule":              "scip-ingest",
		"resolution_artifact_sha256": digest,
		"confidence_tier":            store.ConfidenceExtracted,
	})
	response := metadataResponseFromHandler(
		t,
		s.handleGetRelationshipEvidence,
		"get_relationship_evidence",
		map[string]any{
			"project":                "test",
			"qualified_name":         "test.handler.handle_request",
			"direction":              "outbound",
			"related_qualified_name": "test.auth.authenticate",
		},
	)
	entry := firstRelationship(t, response)
	if entry["resolution_artifact_sha256"] != digest {
		t.Fatalf("artifact digest = %v", entry["resolution_artifact_sha256"])
	}
	ref := requireMapValue(t, entry["relationship_ref"], "relationship_ref")
	if ref["resolution_artifact_sha256"] != digest {
		t.Fatalf("relationship artifact digest = %v", ref["resolution_artifact_sha256"])
	}
}

func TestRelationshipEvidenceDoesNotPromoteMalformedOrHeuristicArtifacts(t *testing.T) {
	for name, properties := range map[string]map[string]any{
		"malformed": {
			"resolver_rule":              "scip-ingest",
			"resolution_artifact_sha256": "not-a-digest",
		},
		"heuristic": {
			"resolver_rule":              "fuzzy-resolve",
			"resolution_artifact_sha256": strings.Repeat("a", 64),
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := serverWithReadyEvidenceIdentity(t)
			seedRelationshipProperties(t, s, properties)
			response := metadataResponseFromHandler(
				t,
				s.handleGetRelationshipEvidence,
				"get_relationship_evidence",
				map[string]any{
					"project":                "test",
					"qualified_name":         "test.handler.handle_request",
					"direction":              "outbound",
					"related_qualified_name": "test.auth.authenticate",
				},
			)
			entry := firstRelationship(t, response)
			if _, present := entry["resolution_artifact_sha256"]; present {
				t.Fatalf("untrusted artifact was promoted: %#v", entry)
			}
			ref := requireMapValue(t, entry["relationship_ref"], "relationship_ref")
			if _, present := ref["resolution_artifact_sha256"]; present {
				t.Fatalf("untrusted artifact entered relationship ref: %#v", ref)
			}
		})
	}
}

func TestRelationshipEvidenceFailsClosedForStaleCheckout(t *testing.T) {
	s := serverWithReadyEvidenceIdentity(t)
	stale := testIndexIdentity(strings.Repeat("d", 64))
	s.captureIndexIdentity = func(string) (*indexidentity.Envelope, error) {
		return stale, nil
	}
	response := metadataResponseFromHandler(
		t,
		s.handleGetRelationshipEvidence,
		"get_relationship_evidence",
		map[string]any{
			"project":        "test",
			"qualified_name": "test.handler.handle_request",
		},
	)
	if response["total"] != float64(0) {
		t.Fatalf("total = %v", response["total"])
	}
	metadata := requireMapValue(t, response["_metadata"], "_metadata")
	refs := requireMapValue(t, metadata["evidence_refs"], "evidence_refs")
	if refs["emitted"] != false {
		t.Fatalf("stale evidence metadata = %#v", refs)
	}
}

func TestRelationshipEvidenceFailsClosedWhenCheckoutChangesDuringRetrieval(t *testing.T) {
	s := serverWithReadyEvidenceIdentity(t)
	seedRelationshipProperties(t, s, map[string]any{
		"resolution_strategy": "go_lsp_cross_file",
		"confidence_tier":     store.ConfidenceInferred,
	})
	ready := testIndexIdentity("clean")
	changed := testIndexIdentity(strings.Repeat("d", 64))
	captures := 0
	s.captureIndexIdentity = func(string) (*indexidentity.Envelope, error) {
		captures++
		if captures == 1 {
			return ready, nil
		}
		return changed, nil
	}

	response := metadataResponseFromHandler(
		t,
		s.handleGetRelationshipEvidence,
		"get_relationship_evidence",
		map[string]any{
			"project":        "test",
			"qualified_name": "test.handler.handle_request",
			"direction":      "outbound",
		},
	)
	if captures != 2 {
		t.Fatalf("identity captures = %d, want 2", captures)
	}
	if response["total"] != float64(0) {
		t.Fatalf("total = %v", response["total"])
	}
	metadata := requireMapValue(t, response["_metadata"], "_metadata")
	refs := requireMapValue(t, metadata["evidence_refs"], "evidence_refs")
	if refs["emitted"] != false || refs["count"] != float64(0) {
		t.Fatalf("changed-checkout evidence metadata = %#v", refs)
	}
	reason, _ := refs["reason"].(string)
	if !strings.Contains(reason, indexidentity.StatusStaleSource) {
		t.Fatalf("changed-checkout reason = %q", reason)
	}
}

func TestRelationshipEvidenceToolIsRegistered(t *testing.T) {
	raw, err := RegisteredToolDefinitionsJSON()
	if err != nil {
		t.Fatalf("export registered definitions: %v", err)
	}
	var definitions []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &definitions); err != nil {
		t.Fatalf("decode registered definitions: %v", err)
	}
	for _, definition := range definitions {
		if definition.Name == "get_relationship_evidence" {
			return
		}
	}
	t.Fatal("get_relationship_evidence is not registered")
}
