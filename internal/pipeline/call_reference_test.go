package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/store"
)

// CALL_REFERENCE vs USAGE (aligned with upstream codebase-memory-mcp): a
// callable referenced at a value site with one proven target is a
// CALL_REFERENCE; a reference to a non-callable, or an unproven resolution, is
// a USAGE. Both carry resolver provenance in properties.
func TestValueSiteReferencesSplitIntoCallReferenceAndUsage(t *testing.T) {
	t.Setenv("CODE_GRAPH_EXTRACT_ISOLATION", "off")
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "_fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fixtureRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	repo, err := os.MkdirTemp(fixtureRoot, "callref-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	src := "LIMIT = 3\n\n\ndef handler():\n    return 1\n\n\ndef configure():\n    chosen = handler\n    return chosen\n\n\ndef budget():\n    return LIMIT\n"
	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := New(context.Background(), st, repo, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}

	nodeQN := func(name string) (int64, string) {
		nodes, err := st.FindNodesByName(p.ProjectName, name)
		if err != nil || len(nodes) == 0 {
			t.Fatalf("node %q not found: %v", name, err)
		}
		return nodes[0].ID, nodes[0].QualifiedName
	}
	handlerID, _ := nodeQN("handler")
	configureID, _ := nodeQN("configure")
	limitID, _ := nodeQN("LIMIT")
	budgetID, _ := nodeQN("budget")

	refs, err := st.FindEdgesByType(p.ProjectName, store.EdgeCallReference)
	if err != nil {
		t.Fatal(err)
	}
	foundRef := false
	for _, e := range refs {
		if e.SourceID == configureID && e.TargetID == handlerID {
			foundRef = true
			if e.Properties["resolver_rule"] == nil || e.Properties["confidence_band"] == nil {
				t.Errorf("CALL_REFERENCE lacks resolver provenance: %+v", e.Properties)
			}
		}
	}
	if !foundRef {
		t.Fatalf("expected CALL_REFERENCE configure -> handler; got %d CALL_REFERENCE edges", len(refs))
	}

	usages, err := st.FindEdgesByType(p.ProjectName, store.EdgeUsage)
	if err != nil {
		t.Fatal(err)
	}
	foundUsage := false
	for _, e := range usages {
		if e.SourceID == budgetID && e.TargetID == limitID {
			foundUsage = true
		}
		if e.SourceID == configureID && e.TargetID == handlerID {
			t.Error("callable reference with a proven target must not also be a USAGE")
		}
	}
	if !foundUsage {
		t.Fatalf("expected USAGE budget -> LIMIT; got %d USAGE edges", len(usages))
	}
}
