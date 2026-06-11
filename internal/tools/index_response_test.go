package tools

// index_repository response correctness — added with the 2026-06-11
// mid-index eviction fix.
//
// Two distinct response bugs are pinned here:
//
//  1. action_outcome inversion: GetProject was read AFTER Run(), and
//     runPasses upserts the project record at its start — so every
//     successful first-time index reported "updated". "created" only
//     ever appeared when the post-Run read FAILED (evictor closed the
//     pool mid-index), which simultaneously zeroed the counts. Both
//     observed values were artifacts.
//
//  2. counts: the response must carry the pipeline's real node/edge
//     counts. The eviction half of the regression (store closed mid-run)
//     is pinned at the router layer in
//     internal/store/router_evict_test.go; this test pins the handler
//     wiring on the happy path.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func writeFixtureRepo(t *testing.T) string {
	t.Helper()
	// NOT t.TempDir(): on macOS that resolves to /var/folders/..., and
	// isForbiddenIndexPath rejects everything under /var. A temp dir under
	// the package directory (repo checkout) passes the forbidden-path check
	// on every platform.
	repo, err := os.MkdirTemp(".", "indexfixture-")
	if err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	src := `def helper():
    return 1


def caller():
    return helper()
`
	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return repo
}

func indexOutcome(t *testing.T, resp map[string]any) string {
	t.Helper()
	md, ok := resp["_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("response missing _metadata map; keys=%v", mapKeys(resp))
	}
	outcome, _ := md["action_outcome"].(string)
	return outcome
}

func TestIndexRepositoryFirstIndexReportsCreatedWithCounts(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	srv := NewServer(router)
	repo := writeFixtureRepo(t)

	args := map[string]any{"repo_path": repo, "skip_report": true}

	// First index: a project that did not exist before this call.
	resp := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository", args)

	nodes, _ := resp["nodes"].(float64)
	if nodes <= 0 {
		t.Errorf("first index reported nodes=%v, want > 0 (full response: %v)", resp["nodes"], resp)
	}
	if _, ok := resp["edges"]; !ok {
		t.Error("first index response missing edges field")
	}
	if got := indexOutcome(t, resp); got != string(ActionOutcomeCreated) {
		t.Errorf("first index action_outcome = %q, want %q", got, ActionOutcomeCreated)
	}

	// Re-index of the same repo: the project record already exists.
	resp2 := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository", args)
	if got := indexOutcome(t, resp2); got != string(ActionOutcomeUpdated) {
		t.Errorf("re-index action_outcome = %q, want %q", got, ActionOutcomeUpdated)
	}
	nodes2, _ := resp2["nodes"].(float64)
	if nodes2 != nodes {
		t.Errorf("re-index reported nodes=%v, want %v (no-op incremental must re-report full counts)", nodes2, nodes)
	}
}
