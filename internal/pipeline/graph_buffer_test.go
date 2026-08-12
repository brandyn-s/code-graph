package pipeline

import (
	"fmt"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func TestGraphBufferFindNodesByQNSuffixUsesSegmentBoundary(t *testing.T) {
	buf := newGraphBuffer("project")
	for _, qn := range []string{
		"project.src.flask.ctx",
		"project.vendor.flask.ctx",
		"project.src.other_ctx",
		"ctx",
	} {
		buf.UpsertNode(&store.Node{
			Label:         "Module",
			QualifiedName: qn,
		})
	}

	hits := buf.FindNodesByQNSuffix("flask.ctx")
	got := make(map[string]bool, len(hits))
	for _, hit := range hits {
		got[hit.QualifiedName] = true
	}
	want := map[string]bool{
		"project.src.flask.ctx":    true,
		"project.vendor.flask.ctx": true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for qn := range want {
		if !got[qn] {
			t.Fatalf("missing suffix hit %q in %v", qn, got)
		}
	}

	exact := buf.FindNodesByQNSuffix("ctx")
	if len(exact) != 3 {
		t.Fatalf("got %d exact-tail hits, want 3", len(exact))
	}
	if partial := buf.FindNodesByQNSuffix("ask.ctx"); len(partial) != 0 {
		t.Fatalf("partial QN segment unexpectedly matched: %v", partial)
	}
}

func BenchmarkGraphBufferFindNodesByQNSuffix(b *testing.B) {
	buf := newGraphBuffer("project")
	for i := range 100_000 {
		buf.UpsertNode(&store.Node{
			Label:         "Module",
			QualifiedName: fmt.Sprintf("project.package%d.module%d", i, i),
		})
	}
	buf.UpsertNode(&store.Node{
		Label:         "Module",
		QualifiedName: "project.src.flask.ctx",
	})

	b.ResetTimer()
	for range b.N {
		if hits := buf.FindNodesByQNSuffix("flask.ctx"); len(hits) != 1 {
			b.Fatalf("got %d suffix hits, want 1", len(hits))
		}
	}
}
