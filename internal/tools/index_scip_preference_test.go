package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func writeFixtureSCIPIndex(t *testing.T, repo string) string {
	t.Helper()
	helper := "scip-python python fixture 0 `fixture`/helper()."
	caller := "scip-python python fixture 0 `fixture`/caller()."
	index := &scip.Index{Documents: []*scip.Document{{
		RelativePath: "app.py",
		Occurrences: []*scip.Occurrence{
			{Range: []int32{0, 4, 10}, Symbol: helper, SymbolRoles: int32(scip.SymbolRole_Definition)},
			{Range: []int32{4, 4, 10}, Symbol: caller, SymbolRoles: int32(scip.SymbolRole_Definition)},
			{Range: []int32{5, 11, 17}, Symbol: helper},
		},
	}}}
	raw, err := proto.Marshal(index)
	if err != nil {
		t.Fatalf("marshal SCIP fixture: %v", err)
	}
	path := filepath.Join(repo, "index.scip")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write SCIP fixture: %v", err)
	}
	return path
}

func TestSCIPPrecisionTierIsExplicitStickyAndObservable(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.CloseAll)
	cfg, err := store.OpenConfigInDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cfg.Close() })
	srv := NewServer(router, WithConfig(cfg))
	repo := writeFixtureRepo(t)
	writeFixtureSCIPIndex(t, repo)

	first := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository", indexArgs(repo, map[string]any{
		"precision_tier": "scip",
		"skip_report":    true,
		"force":          true,
	}))
	assertSCIPPrecision(t, first, "scip", "scip", "applied")

	// Omitted on the next index: the explicit per-project choice persists.
	second := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository", indexArgs(repo, map[string]any{
		"skip_report": true,
		"force":       true,
	}))
	assertSCIPPrecision(t, second, "scip", "scip", "applied")

	status := metadataResponseFromHandler(t, srv.handleIndexStatus, "index_status", map[string]any{
		"project": first["project"],
	})
	assertSCIPPrecision(t, status, "scip", "scip", "applied")

	// An explicit downgrade is equally sticky and cannot be confused with
	// compiler-index coverage.
	third := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository", indexArgs(repo, map[string]any{
		"precision_tier": "heuristic",
		"skip_report":    true,
		"force":          true,
	}))
	assertSCIPPrecision(t, third, "heuristic", "heuristic", "disabled")
}

func TestRequestedSCIPWithoutIndexFailsClosedAsDegraded(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.CloseAll)
	cfg, err := store.OpenConfigInDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cfg.Close() })
	srv := NewServer(router, WithConfig(cfg))
	repo := writeFixtureRepo(t)

	indexed := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository", indexArgs(repo, map[string]any{
		"precision_tier": "scip",
		"skip_report":    true,
	}))
	assertSCIPPrecision(t, indexed, "scip", "heuristic", "failed")
	if got := indexed["status"]; got != "degraded" {
		t.Fatalf("index status = %v, want degraded when requested SCIP is unavailable", got)
	}

	status := metadataResponseFromHandler(t, srv.handleIndexStatus, "index_status", map[string]any{
		"project": indexed["project"],
	})
	assertSCIPPrecision(t, status, "scip", "heuristic", "failed")
	if got := status["status"]; got != "degraded" {
		t.Fatalf("persisted index status = %v, want degraded", got)
	}
}

func assertSCIPPrecision(t *testing.T, response map[string]any, requested, effective, state string) {
	t.Helper()
	precision, ok := response["graph_precision"].(map[string]any)
	if !ok {
		t.Fatalf("graph_precision missing or wrong type: %T (%v)", response["graph_precision"], response)
	}
	if got := precision["requested_tier"]; got != requested {
		t.Fatalf("requested_tier = %v, want %s", got, requested)
	}
	if got := precision["effective_tier"]; got != effective {
		t.Fatalf("effective_tier = %v, want %s", got, effective)
	}
	status, ok := precision["scip_status"].(map[string]any)
	if !ok {
		t.Fatalf("scip_status missing or wrong type: %T (%v)", precision["scip_status"], precision)
	}
	if got := status["state"]; got != state {
		t.Fatalf("scip_status.state = %v, want %s (%v)", got, state, status)
	}
}
