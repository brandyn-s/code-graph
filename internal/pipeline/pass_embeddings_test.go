package pipeline

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

// fakeVoyage serves a minimal /v1/embeddings response: one distinctive
// vector per input text. Returns the server and a counter of texts embedded.
func fakeVoyage(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	embedded := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		type item struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		resp := struct {
			Data []item `json:"data"`
		}{}
		for i := range req.Input {
			resp.Data = append(resp.Data, item{Embedding: []float32{1, 0, 0}, Index: i})
		}
		embedded += len(req.Input)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return srv, &embedded
}

func insertEmbeddableNode(t *testing.T, s *store.Store, project, qn string) int64 {
	t.Helper()
	id, err := s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Function",
		Name:          qn,
		QualifiedName: fmt.Sprintf("%s.%s", project, qn),
		FilePath:      "a.go",
		Properties:    map[string]any{"signature": "()"},
	})
	if err != nil {
		t.Fatalf("UpsertNode %s: %v", qn, err)
	}
	return id
}

func countEmbeddings(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.Q().QueryRow(`SELECT COUNT(*) FROM node_embeddings`).Scan(&n); err != nil {
		t.Fatalf("count embeddings: %v", err)
	}
	return n
}

// TestPassEmbeddingsMissing_BackfillsOnlyGaps pins the incremental-path
// contract: after some nodes already have embeddings, passEmbeddingsMissing
// embeds exactly the nodes without rows — the shape runIncrementalPasses
// produces when DeleteNodesByFile cascades away a changed file's embeddings.
func TestPassEmbeddingsMissing_BackfillsOnlyGaps(t *testing.T) {
	srv, embedded := fakeVoyage(t)
	defer srv.Close()
	oldURL := voyageEmbedURL
	voyageEmbedURL = srv.URL
	defer func() { voyageEmbedURL = oldURL }()
	t.Setenv("VOYAGE_API_KEY", "test-key")
	t.Setenv("CODE_GRAPH_SKIP_EMBEDDINGS", "")

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()
	if err := s.UpsertProject("p", "/tmp/p"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	idA := insertEmbeddableNode(t, s, "p", "alreadyEmbedded")
	insertEmbeddableNode(t, s, "p", "missingOne")
	insertEmbeddableNode(t, s, "p", "missingTwo")

	// Pre-embed node A so the missing-only pass must skip it.
	if err := s.UpsertEmbedding(idA, "voyage-code-3", []float32{0, 1, 0}); err != nil {
		t.Fatalf("pre-embed: %v", err)
	}

	p := &Pipeline{Store: s, ProjectName: "p"}
	p.passEmbeddingsMissing()

	if got := countEmbeddings(t, s); got != 3 {
		t.Errorf("expected 3 embeddings after backfill, got %d", got)
	}
	if *embedded != 2 {
		t.Errorf("expected exactly 2 texts sent to Voyage (missing only), got %d", *embedded)
	}
}

// TestPassEmbeddingsMissing_NoopWhenComplete verifies the pass makes zero
// API calls when every embeddable node already has an embedding.
func TestPassEmbeddingsMissing_NoopWhenComplete(t *testing.T) {
	srv, embedded := fakeVoyage(t)
	defer srv.Close()
	oldURL := voyageEmbedURL
	voyageEmbedURL = srv.URL
	defer func() { voyageEmbedURL = oldURL }()
	t.Setenv("VOYAGE_API_KEY", "test-key")
	t.Setenv("CODE_GRAPH_SKIP_EMBEDDINGS", "")

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()
	if err := s.UpsertProject("p", "/tmp/p"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	id := insertEmbeddableNode(t, s, "p", "f")
	if err := s.UpsertEmbedding(id, "voyage-code-3", []float32{0, 1, 0}); err != nil {
		t.Fatalf("pre-embed: %v", err)
	}

	p := &Pipeline{Store: s, ProjectName: "p"}
	p.passEmbeddingsMissing()

	if *embedded != 0 {
		t.Errorf("expected no Voyage calls, got %d texts embedded", *embedded)
	}
}

// TestPassEmbeddings_FullStillEmbedsAll pins that the full-index pass keeps
// its embed-everything behavior (re-embeds even nodes with existing rows).
func TestPassEmbeddings_FullStillEmbedsAll(t *testing.T) {
	srv, embedded := fakeVoyage(t)
	defer srv.Close()
	oldURL := voyageEmbedURL
	voyageEmbedURL = srv.URL
	defer func() { voyageEmbedURL = oldURL }()
	t.Setenv("VOYAGE_API_KEY", "test-key")
	t.Setenv("CODE_GRAPH_SKIP_EMBEDDINGS", "")

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()
	if err := s.UpsertProject("p", "/tmp/p"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	idA := insertEmbeddableNode(t, s, "p", "a")
	insertEmbeddableNode(t, s, "p", "b")
	if err := s.UpsertEmbedding(idA, "voyage-code-3", []float32{0, 1, 0}); err != nil {
		t.Fatalf("pre-embed: %v", err)
	}

	p := &Pipeline{Store: s, ProjectName: "p"}
	p.passEmbeddings()

	if *embedded != 2 {
		t.Errorf("full pass should embed all embeddable nodes, got %d", *embedded)
	}
}
