package pipeline

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func TestPassDataflowCreatesParameterNodes(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("upsert project: %v", err)
	}

	// Insert a function node with param_names and param_types
	funcID, err := s.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "authenticate",
		QualifiedName: "test.main.authenticate", FilePath: "main.py",
		StartLine: 1, EndLine: 5,
		Properties: map[string]any{
			"param_names": []string{"username", "password"},
			"param_types": []string{"str", "str"},
		},
	})
	if err != nil {
		t.Fatalf("insert func: %v", err)
	}
	if funcID == 0 {
		t.Fatal("expected non-zero func ID")
	}

	// Create a pipeline that reads from the store (no in-memory buffer)
	p := &Pipeline{
		Store:           s,
		ProjectName:     "test",
		extractionCache: make(map[string]*cachedExtraction),
	}

	p.passDataflow()

	// Check that Parameter nodes were created
	params, err := s.FindNodesByLabel("test", "Parameter")
	if err != nil {
		t.Fatalf("find params: %v", err)
	}
	if len(params) != 2 {
		t.Fatalf("expected 2 Parameter nodes, got %d", len(params))
	}

	// Check names and QNs
	names := map[string]bool{}
	for _, param := range params {
		names[param.Name] = true
		if param.FilePath != "main.py" {
			t.Errorf("expected file_path=main.py, got %s", param.FilePath)
		}
		if param.StartLine != 1 {
			t.Errorf("expected start_line=1, got %d", param.StartLine)
		}
	}
	if !names["username"] || !names["password"] {
		t.Errorf("expected username and password parameters, got %v", names)
	}

	// Check PARAMETER_OF edges exist
	edges, err := s.FindEdgesByTargetAndType(funcID, "PARAMETER_OF")
	if err != nil {
		t.Fatalf("find edges: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 PARAMETER_OF edges, got %d", len(edges))
	}

	// Verify edge index properties
	indices := map[float64]bool{}
	for _, e := range edges {
		idx, ok := e.Properties["index"].(float64)
		if !ok {
			t.Errorf("edge missing index property: %v", e.Properties)
			continue
		}
		indices[idx] = true
	}
	if !indices[0] || !indices[1] {
		t.Errorf("expected indices 0 and 1, got %v", indices)
	}
}

func TestPassDataflowSkipsNoParams(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("upsert project: %v", err)
	}

	// Insert a function node with NO param_names
	_, err = s.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "noop",
		QualifiedName: "test.main.noop", FilePath: "main.py",
		StartLine: 1, EndLine: 3,
		Properties: map[string]any{},
	})
	if err != nil {
		t.Fatalf("insert func: %v", err)
	}

	p := &Pipeline{
		Store:           s,
		ProjectName:     "test",
		extractionCache: make(map[string]*cachedExtraction),
	}

	p.passDataflow()

	// Should have zero Parameter nodes
	params, err := s.FindNodesByLabel("test", "Parameter")
	if err != nil {
		t.Fatalf("find params: %v", err)
	}
	if len(params) != 0 {
		t.Errorf("expected 0 Parameter nodes, got %d", len(params))
	}
}

func TestPassDataflowWithGraphBuffer(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// Simulate the in-memory buffer path (full index)
	buf := newGraphBuffer("test")
	buf.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "process",
		QualifiedName: "test.handler.process", FilePath: "handler.go",
		StartLine: 10, EndLine: 25,
		Properties: map[string]any{
			"param_names": []string{"ctx", "req", "opts"},
			"param_types": []string{"context.Context", "Request", "Options"},
		},
	})

	p := &Pipeline{
		Store:           s,
		ProjectName:     "test",
		buf:             buf,
		extractionCache: make(map[string]*cachedExtraction),
	}

	p.passDataflow()

	// Verify in-buffer results
	params := buf.FindNodesByLabel("Parameter")
	if len(params) != 3 {
		t.Fatalf("expected 3 Parameter nodes in buffer, got %d", len(params))
	}

	// Verify edges exist in buffer
	for _, param := range params {
		edges := buf.FindEdgesBySourceAndType(param.ID, "PARAMETER_OF")
		if len(edges) != 1 {
			t.Errorf("expected 1 PARAMETER_OF edge for param %s, got %d", param.Name, len(edges))
		}
	}
}

func TestPassDataflowParamTypes(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("upsert project: %v", err)
	}

	// Function with more param_names than param_types
	_, err = s.UpsertNode(&store.Node{
		Project: "test", Label: "Method", Name: "handle",
		QualifiedName: "test.svc.Handler.handle", FilePath: "svc.py",
		StartLine: 5, EndLine: 20,
		Properties: map[string]any{
			"param_names": []string{"self", "request", "timeout"},
			"param_types": []string{"", "HttpRequest"},
			// timeout has no type (index 2 exceeds param_types length)
		},
	})
	if err != nil {
		t.Fatalf("insert method: %v", err)
	}

	p := &Pipeline{
		Store:           s,
		ProjectName:     "test",
		extractionCache: make(map[string]*cachedExtraction),
	}

	p.passDataflow()

	params, err := s.FindNodesByLabel("test", "Parameter")
	if err != nil {
		t.Fatalf("find params: %v", err)
	}
	if len(params) != 3 {
		t.Fatalf("expected 3 Parameter nodes, got %d", len(params))
	}

	// Find the "request" param and verify it has type
	for _, param := range params {
		if param.Name == "request" {
			typ, ok := param.Properties["type"].(string)
			if !ok || typ != "HttpRequest" {
				t.Errorf("expected request param type=HttpRequest, got %v", param.Properties["type"])
			}
		}
		if param.Name == "timeout" {
			// timeout should have no "type" property (or empty)
			if typ, ok := param.Properties["type"]; ok && typ != "" {
				t.Errorf("expected no type for timeout, got %v", typ)
			}
		}
	}
}

// Silence unused import warning — cbm is used for cachedExtraction type reference.
var _ = cbm.FileResult{}
