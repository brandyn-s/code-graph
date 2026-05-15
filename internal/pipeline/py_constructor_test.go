// Reproduction harness for the flask-adversarial F1=0.573 investigation.
// flask FN: Flask.app_context() returns AppContext(self) — no CALLS edge
// emitted to the AppContext class node. This test isolates the call shape
// to determine whether the bug is in extraction, resolution, or attribution.
package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// TestPyConstructorCallEmitsEdge verifies a Python method that instantiates
// a class via `return ClassName(args)` emits a CALLS edge from the method
// to the class node. Mirrors flask.app.Flask.app_context -> flask.ctx.AppContext.
func TestPyConstructorCallEmitsEdge(t *testing.T) {
	dir, err := os.MkdirTemp("", "cgm-py-ctor-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	writeFile(t, filepath.Join(dir, "ctx.py"), `
class AppContext:
    def __init__(self, app):
        self.app = app
`)

	writeFile(t, filepath.Join(dir, "app.py"), `
from ctx import AppContext

class Flask:
    def app_context(self):
        return AppContext(self)
`)

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}

	// Verify AppContext class node exists
	classes, _ := s.FindNodesByName(p.ProjectName, "AppContext")
	if len(classes) == 0 {
		t.Fatal("AppContext class not found in store")
	}
	classQN := classes[0].QualifiedName
	t.Logf("AppContext QN: %s (label=%s)", classQN, classes[0].Label)

	// Verify Flask.app_context method exists
	callers, _ := s.FindNodesByName(p.ProjectName, "app_context")
	if len(callers) == 0 {
		t.Fatal("Flask.app_context method not found in store")
	}
	t.Logf("Flask.app_context QN: %s", callers[0].QualifiedName)

	// Check CALLS edges from app_context — expecting at least one to AppContext
	edges, _ := s.FindEdgesBySourceAndType(callers[0].ID, "CALLS")
	t.Logf("CALLS edges from Flask.app_context: %d", len(edges))
	for _, e := range edges {
		t.Logf("  edge -> target_id=%d type=%s", e.TargetID, e.Type)
	}

	found := false
	for _, e := range edges {
		if e.TargetID == classes[0].ID {
			found = true
			break
		}
	}
	if !found {
		// Also check CALLS_PSEUDO from module to see where the call went
		moduleNodes, _ := s.FindNodesByName(p.ProjectName, "app")
		for _, m := range moduleNodes {
			if m.Label != "Module" {
				continue
			}
			pseudoEdges, _ := s.FindEdgesBySourceAndType(m.ID, "CALLS_PSEUDO")
			t.Logf("CALLS_PSEUDO from app module: %d", len(pseudoEdges))
			for _, e := range pseudoEdges {
				if e.TargetID == classes[0].ID {
					t.Logf("  CALLS_PSEUDO points to AppContext (call attributed to module, not method)")
				}
			}
		}
		t.Error("expected CALLS edge from Flask.app_context to AppContext, but none found")
	}
}
