package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/brandyn-s/code-graph/internal/indexidentity"
)

func testIndexIdentity(dirty string) *indexidentity.Envelope {
	repositoryID := strings.Repeat("a", 64)
	sourceRevision := strings.Repeat("c", 40)
	return &indexidentity.Envelope{
		SchemaVersion:    indexidentity.SchemaVersion,
		RepositoryID:     repositoryID,
		CheckoutID:       strings.Repeat("b", 64),
		SourceRevision:   sourceRevision,
		DirtyFingerprint: dirty,
		IndexGeneration: indexidentity.ComputeIndexGeneration(
			repositoryID,
			sourceRevision,
			dirty,
		),
		CapturedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
}

func serverWithReadyEvidenceIdentity(t *testing.T) *Server {
	t.Helper()
	s := newServerWithSeededProject(t)
	st, err := s.router.ForProject("test")
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	identity := testIndexIdentity("clean")
	if err := st.SetIndexIdentity("test", identity); err != nil {
		t.Fatalf("SetIndexIdentity: %v", err)
	}
	s.captureIndexIdentity = func(string) (*indexidentity.Envelope, error) {
		return identity, nil
	}
	return s
}

func firstSearchResult(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	results, ok := response["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("results missing or empty: %#v", response["results"])
	}
	result, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("first result is %T, want map", results[0])
	}
	return result
}

func requireMapValue(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want map", label, value)
	}
	return result
}

func requireStringValue(t *testing.T, value any, label string) string {
	t.Helper()
	result, ok := value.(string)
	if !ok {
		t.Fatalf("%s is %T, want string", label, value)
	}
	return result
}

func TestSearchGraphIncludeSourceEmitsGenerationBoundEvidence(t *testing.T) {
	s := serverWithReadyEvidenceIdentity(t)
	response := metadataResponseFromHandler(
		t,
		s.handleSearchGraph,
		"search_graph",
		map[string]any{
			"project":        "test",
			"label":          "Function",
			"name_pattern":   "handle_request",
			"include_source": true,
		},
	)
	result := firstSearchResult(t, response)
	for _, field := range []string{"symbol_ref", "evidence_ref", "observation_ref"} {
		if _, ok := result[field].(map[string]any); !ok {
			t.Fatalf("result missing %s: %#v", field, result[field])
		}
	}
	evidenceRef := requireMapValue(t, result["evidence_ref"], "evidence_ref")
	if evidenceRef["index_generation"] != testIndexIdentity("clean").IndexGeneration {
		t.Fatalf("index generation = %v", evidenceRef["index_generation"])
	}
	observationRef := requireMapValue(t, result["observation_ref"], "observation_ref")
	if observationRef["source_engine"] != "code-graph" {
		t.Fatalf("source_engine = %v", observationRef["source_engine"])
	}
	metadata := requireMapValue(t, response["_metadata"], "_metadata")
	refs := requireMapValue(t, metadata["evidence_refs"], "evidence_refs")
	if refs["emitted"] != true || refs["count"] != float64(1) {
		t.Fatalf("evidence refs metadata = %#v", refs)
	}
}

func TestSearchGraphEvidenceFailsClosedWhenLiveCheckoutIsStale(t *testing.T) {
	s := serverWithReadyEvidenceIdentity(t)
	stale := testIndexIdentity(strings.Repeat("d", 64))
	s.captureIndexIdentity = func(string) (*indexidentity.Envelope, error) {
		return stale, nil
	}
	response := metadataResponseFromHandler(
		t,
		s.handleSearchGraph,
		"search_graph",
		map[string]any{
			"project":        "test",
			"label":          "Function",
			"name_pattern":   "handle_request",
			"include_source": true,
		},
	)
	result := firstSearchResult(t, response)
	if _, ok := result["evidence_ref"]; ok {
		t.Fatalf("stale result carried evidence_ref: %#v", result["evidence_ref"])
	}
	metadata := requireMapValue(t, response["_metadata"], "_metadata")
	refs := requireMapValue(t, metadata["evidence_refs"], "evidence_refs")
	if refs["emitted"] != false {
		t.Fatalf("stale evidence metadata = %#v", refs)
	}
	if reason, _ := refs["reason"].(string); !strings.Contains(reason, "stale_source") {
		t.Fatalf("stale reason = %q", reason)
	}
}

func TestSearchGraphCacheDoesNotReplayEvidenceAfterCheckoutChanges(t *testing.T) {
	s := serverWithReadyEvidenceIdentity(t)
	args := map[string]any{
		"project":        "test",
		"label":          "Function",
		"name_pattern":   "handle_request",
		"include_source": true,
	}
	first := metadataResponseFromHandler(
		t,
		s.handleSearchGraph,
		"search_graph",
		args,
	)
	if _, ok := firstSearchResult(t, first)["evidence_ref"]; !ok {
		t.Fatal("ready cache-miss result did not carry evidence")
	}

	stale := testIndexIdentity(strings.Repeat("d", 64))
	s.captureIndexIdentity = func(string) (*indexidentity.Envelope, error) {
		return stale, nil
	}
	second := metadataResponseFromHandler(
		t,
		s.handleSearchGraph,
		"search_graph",
		args,
	)
	if _, ok := firstSearchResult(t, second)["evidence_ref"]; ok {
		t.Fatal("cache hit replayed stale evidence")
	}
}
