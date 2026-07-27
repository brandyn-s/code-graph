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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/indexidentity"
	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeFixtureRepo(t *testing.T) string {
	t.Helper()
	// NOT t.TempDir(): on macOS that resolves to /var/folders/..., and
	// isForbiddenIndexPath rejects everything under /var. Use the repo-root
	// gitignored _fixtures directory so the approved-path fixture never
	// dirties git status while a test is running.
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "_fixtures"))
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	rootExisted := true
	if _, statErr := os.Stat(fixtureRoot); os.IsNotExist(statErr) {
		rootExisted = false
	}
	if err := os.MkdirAll(fixtureRoot, 0o750); err != nil {
		t.Fatalf("mkdir fixture root: %v", err)
	}
	repo, err := os.MkdirTemp(fixtureRoot, "indexfixture-")
	if err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(repo)
		if !rootExisted {
			_ = os.Remove(fixtureRoot)
		}
	})
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

func initCommittedFixtureRepo(t *testing.T, repo string) string {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	run("remote", "add", "origin", "https://github.com/example/fixture.git")
	run("add", "app.py")
	run("commit", "-m", "fixture")
	return string([]byte(run("rev-parse", "HEAD"))[:40])
}

func hexSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func requireCleanIndexIdentity(t *testing.T, resp map[string]any, repo, revision string) {
	t.Helper()

	if got, _ := resp["identity_status"].(string); got != "captured" {
		t.Fatalf("identity_status = %q, want captured (response=%v)", got, resp)
	}
	if reason, _ := resp["identity_reason"].(string); reason != "" {
		t.Fatalf("identity_reason = %q, want empty for captured identity", reason)
	}
	raw, ok := resp["index_identity"].(map[string]any)
	if !ok {
		t.Fatalf("index_identity missing or wrong type: %T (response=%v)", resp["index_identity"], resp)
	}

	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatalf("Abs(repo): %v", err)
	}
	repositoryID := hexSHA256("remote:https://github.com/example/fixture")
	checkoutID := hexSHA256("path:" + filepath.ToSlash(absRepo))
	generation := hexSHA256(repositoryID + "\x00" + revision + "\x00clean")

	want := map[string]any{
		"schema_version":    float64(1),
		"repository_id":     repositoryID,
		"checkout_id":       checkoutID,
		"source_revision":   revision,
		"dirty_fingerprint": "clean",
		"index_generation":  generation,
	}
	for key, expected := range want {
		if got := raw[key]; got != expected {
			t.Errorf("index_identity.%s = %v, want %v", key, got, expected)
		}
	}
	capturedAt, ok := raw["captured_at"].(string)
	if !ok || capturedAt == "" {
		t.Fatalf("index_identity.captured_at = %v, want RFC3339 UTC timestamp", raw["captured_at"])
	}
	parsed, err := time.Parse(time.RFC3339, capturedAt)
	if err != nil {
		t.Fatalf("index_identity.captured_at = %q: %v", capturedAt, err)
	}
	if parsed.Location() != time.UTC {
		t.Errorf("index_identity.captured_at location = %v, want UTC", parsed.Location())
	}
}

func callIndexRepositoryWithContext(
	t *testing.T,
	ctx context.Context,
	srv *Server,
	args map[string]any,
) *mcp.CallToolResult {
	t.Helper()
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal index arguments: %v", err)
	}
	result, err := srv.handleIndexRepository(ctx, &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "index_repository",
			Arguments: arguments,
		},
	})
	if err != nil {
		t.Fatalf("handleIndexRepository: %v", err)
	}
	return result
}

func TestIndexRepositoryPersistsCleanIndexIdentityForStatus(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	revision := initCommittedFixtureRepo(t, repo)

	indexResp := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	requireCleanIndexIdentity(t, indexResp, repo, revision)

	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatalf("Abs(repo): %v", err)
	}
	project := pipeline.ProjectNameFromPath(absRepo)
	statusResp := metadataResponseFromHandler(t, srv.handleIndexStatus, "index_status",
		map[string]any{"project": project})
	requireCleanIndexIdentity(t, statusResp, repo, revision)
	if got, _ := statusResp["status"].(string); got != "ready" {
		t.Errorf("index_status.status = %q, want ready for captured coherent identity", got)
	}
}

func TestIndexRepositoryReportsObservedEmbeddingInventory(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	indexResp := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})

	if got, ok := indexResp["embedding_count"].(float64); !ok || got != 0 {
		t.Fatalf("embedding_count = %v (%T), want observed zero", indexResp["embedding_count"], indexResp["embedding_count"])
	}
	models, ok := indexResp["embedding_models"].(map[string]any)
	if !ok {
		t.Fatalf("embedding_models = %v (%T), want an observed model-count map", indexResp["embedding_models"], indexResp["embedding_models"])
	}
	if len(models) != 0 {
		t.Fatalf("embedding_models = %v, want empty without a Voyage credential", models)
	}
	if got, _ := indexResp["embedding_status"].(string); got != "captured" {
		t.Fatalf("embedding_status = %q, want captured", got)
	}
}

func TestIndexRepositoryDegradesWhenSourceChangesDuringIndex(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	startIdentity, err := indexidentity.Capture(repo)
	if err != nil {
		t.Fatalf("capture fixture identity: %v", err)
	}
	captureCalls := 0
	srv.captureIndexIdentity = func(string) (*indexidentity.Envelope, error) {
		captureCalls++
		identity := *startIdentity
		if captureCalls == 2 {
			identity.IndexGeneration = strings.Repeat("f", 64)
		}
		return &identity, nil
	}

	indexResp := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	if captureCalls != 2 {
		t.Fatalf("identity capture calls = %d, want start and end captures", captureCalls)
	}
	if got, _ := indexResp["status"].(string); got != "degraded" {
		t.Errorf("index status = %q, want degraded", got)
	}
	if got, _ := indexResp["identity_status"].(string); got != indexidentity.StatusError {
		t.Errorf("identity_status = %q, want %q", got, indexidentity.StatusError)
	}
	if reason, _ := indexResp["identity_reason"].(string); !strings.Contains(reason, "source_changed_during_index") {
		t.Errorf("identity_reason = %q, want source_changed_during_index", reason)
	}
	if identity := indexResp["index_identity"]; identity != nil {
		t.Errorf("degraded response exposed incoherent identity: %v", identity)
	}

	project, _ := indexResp["project"].(string)
	statusResp := metadataResponseFromHandler(t, srv.handleIndexStatus, "index_status",
		map[string]any{"project": project})
	if got, _ := statusResp["status"].(string); got != "degraded" {
		t.Errorf("persisted index status = %q, want degraded", got)
	}
	if reason, _ := statusResp["identity_reason"].(string); !strings.Contains(reason, "source_changed_during_index") {
		t.Errorf("persisted identity_reason = %q, want source_changed_during_index", reason)
	}
}

func TestIndexRepositoryDegradesWhenCheckoutChangesWithSameGeneration(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	startIdentity, err := indexidentity.Capture(repo)
	if err != nil {
		t.Fatalf("capture fixture identity: %v", err)
	}
	captureCalls := 0
	srv.captureIndexIdentity = func(string) (*indexidentity.Envelope, error) {
		captureCalls++
		identity := *startIdentity
		if captureCalls == 2 {
			identity.CheckoutID = strings.Repeat("b", 64)
		}
		return &identity, nil
	}

	indexResp := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	if captureCalls != 2 {
		t.Fatalf("identity capture calls = %d, want start and end captures", captureCalls)
	}
	if got, _ := indexResp["status"].(string); got != "degraded" {
		t.Errorf("index status = %q, want degraded", got)
	}
	if got, _ := indexResp["identity_status"].(string); got != indexidentity.StatusError {
		t.Errorf("identity_status = %q, want %q", got, indexidentity.StatusError)
	}
	if reason, _ := indexResp["identity_reason"].(string); !strings.Contains(reason, "source_changed_during_index") {
		t.Errorf("identity_reason = %q, want source_changed_during_index", reason)
	} else {
		for _, detail := range []string{
			"checkout_id",
			startIdentity.CheckoutID,
			strings.Repeat("b", 64),
		} {
			if !strings.Contains(reason, detail) {
				t.Errorf("identity_reason = %q, want mismatch detail %q", reason, detail)
			}
		}
	}
	if identity := indexResp["index_identity"]; identity != nil {
		t.Errorf("response exposed identity from a different checkout: %v", identity)
	}
}

func TestIndexRepositoryIncludesGeneratedReportInEndCoherenceCheck(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	indexResp := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo})
	if _, err := os.Stat(filepath.Join(repo, "ARCHITECTURE_REPORT.md")); err != nil {
		t.Fatalf("default index did not generate ARCHITECTURE_REPORT.md: %v", err)
	}
	if got, _ := indexResp["status"].(string); got != "degraded" {
		t.Errorf("index status = %q, want degraded after report changed source state", got)
	}
	if got, _ := indexResp["identity_status"].(string); got != indexidentity.StatusError {
		t.Errorf("identity_status = %q, want %q", got, indexidentity.StatusError)
	}
	if reason, _ := indexResp["identity_reason"].(string); !strings.Contains(reason, "source_changed_during_index") {
		t.Errorf("identity_reason = %q, want source_changed_during_index", reason)
	}
	if identity := indexResp["index_identity"]; identity != nil {
		t.Errorf("response exposed identity that omitted generated report: %v", identity)
	}
}

func TestWatcherIncrementalReindexRefreshesIndexIdentity(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	first := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	firstIdentity, ok := first["index_identity"].(map[string]any)
	if !ok {
		t.Fatalf("first index identity missing: %v", first)
	}
	firstGeneration, _ := firstIdentity["index_generation"].(string)

	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte(`def changed():
    return 2
`), 0o600); err != nil {
		t.Fatalf("modify app.py: %v", err)
	}
	project, _ := first["project"].(string)
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatalf("Abs(repo): %v", err)
	}
	if err := srv.syncProject(context.Background(), project, absRepo); err != nil {
		t.Fatalf("syncProject: %v", err)
	}

	expected, err := indexidentity.Capture(absRepo)
	if err != nil {
		t.Fatalf("capture changed checkout: %v", err)
	}
	st, err := router.ForProject(project)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	record, err := st.GetIndexIdentity(project)
	if err != nil {
		t.Fatalf("GetIndexIdentity: %v", err)
	}
	if record.Status != indexidentity.StatusCaptured || record.Identity == nil {
		t.Fatalf("watcher identity record = %+v, want captured", record)
	}
	if record.Identity.IndexGeneration != expected.IndexGeneration {
		t.Errorf("watcher generation = %q, want %q", record.Identity.IndexGeneration, expected.IndexGeneration)
	}
	if record.Identity.IndexGeneration == firstGeneration {
		t.Error("watcher incremental reindex left the previous generation attached")
	}
}

func TestIndexRepositoryPersistsTerminalIdentityErrorWhenPipelineFails(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	first := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	project, _ := first["project"].(string)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := callIndexRepositoryWithContext(t, ctx, srv, map[string]any{
		"repo_path":   repo,
		"skip_report": true,
	})
	if result == nil || !result.IsError {
		t.Fatalf("canceled index result = %#v, want tool error", result)
	}

	st, err := router.ForProject(project)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	record, err := st.GetIndexIdentity(project)
	if err != nil {
		t.Fatalf("GetIndexIdentity: %v", err)
	}
	if record.Status != indexidentity.StatusError {
		t.Fatalf("identity status = %q, want %q", record.Status, indexidentity.StatusError)
	}
	if !strings.Contains(record.Reason, "indexing_failed") {
		t.Errorf("identity reason = %q, want indexing_failed", record.Reason)
	}
	if !strings.Contains(record.Reason, context.Canceled.Error()) {
		t.Errorf("identity reason = %q, want %q", record.Reason, context.Canceled)
	}
}

func TestIndexRepositoryPersistsTerminalIdentityErrorWhenForceResetFails(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	first := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	project, _ := first["project"].(string)
	st, err := router.ForProject(project)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if _, err := st.DB().ExecContext(t.Context(), `
		CREATE TRIGGER fail_file_hash_reset
		BEFORE DELETE ON file_hashes
		BEGIN
			SELECT RAISE(FAIL, 'forced file hash reset failure');
		END`); err != nil {
		t.Fatalf("create file hash failure trigger: %v", err)
	}
	srv.sessionProject = project
	srv.indexStatus.Store("ready")

	result := callIndexRepositoryWithContext(
		t,
		context.Background(),
		srv,
		map[string]any{
			"repo_path":   repo,
			"skip_report": true,
			"force":       true,
		},
	)
	if result == nil || !result.IsError {
		t.Fatalf("force-reset failure result = %#v, want tool error", result)
	}

	record, err := st.GetIndexIdentity(project)
	if err != nil {
		t.Fatalf("GetIndexIdentity: %v", err)
	}
	if record.Status != indexidentity.StatusError {
		t.Fatalf("identity status = %q, want %q", record.Status, indexidentity.StatusError)
	}
	if !strings.Contains(record.Reason, "clearing_file_hashes_failed") {
		t.Errorf("identity reason = %q, want clearing_file_hashes_failed", record.Reason)
	}
	if !strings.Contains(record.Reason, "forced file hash reset failure") {
		t.Errorf("identity reason = %q, want trigger failure detail", record.Reason)
	}
	if status, _ := srv.indexStatus.Load().(string); status != "degraded" {
		t.Errorf("session index status = %q, want degraded", status)
	}
}

func TestIndexRepositoryPersistsTerminalIdentityErrorWhenPostPipelineStateWriteFails(
	t *testing.T,
) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	first := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	project, _ := first["project"].(string)
	st, err := router.ForProject(project)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if _, err := st.DB().ExecContext(t.Context(), `
		CREATE TRIGGER fail_completed_pending_state
		BEFORE UPDATE ON index_identity
		WHEN NEW.identity_status = 'pending'
			AND NEW.identity_reason LIKE 'graph indexing completed%'
		BEGIN
			SELECT RAISE(FAIL, 'forced completed-pending failure');
		END`); err != nil {
		t.Fatalf("create pending-state failure trigger: %v", err)
	}

	result := callIndexRepositoryWithContext(
		t,
		context.Background(),
		srv,
		map[string]any{
			"repo_path":   repo,
			"skip_report": true,
		},
	)
	if result == nil || !result.IsError {
		t.Fatalf("post-pipeline state failure result = %#v, want tool error", result)
	}

	record, err := st.GetIndexIdentity(project)
	if err != nil {
		t.Fatalf("GetIndexIdentity: %v", err)
	}
	if record.Status != indexidentity.StatusError {
		t.Fatalf("identity status = %q, want %q", record.Status, indexidentity.StatusError)
	}
	if !strings.Contains(record.Reason, "mark_identity_pending_failed") {
		t.Errorf("identity reason = %q, want mark_identity_pending_failed", record.Reason)
	}
	if !strings.Contains(record.Reason, "forced completed-pending failure") {
		t.Errorf("identity reason = %q, want trigger failure detail", record.Reason)
	}
}

func TestFirstIndexPersistsTerminalIdentityErrorWhenPostPipelineStateWriteFails(
	t *testing.T,
) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatalf("Abs(repo): %v", err)
	}
	project := pipeline.ProjectNameFromPath(absRepo)
	st, err := router.ForProject(project)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if _, err := st.DB().ExecContext(t.Context(), `
		CREATE TRIGGER fail_first_completed_pending_state
		BEFORE INSERT ON index_identity
		WHEN NEW.identity_status = 'pending'
			AND NEW.identity_reason LIKE 'graph indexing completed%'
		BEGIN
			SELECT RAISE(FAIL, 'forced first completed-pending failure');
		END`); err != nil {
		t.Fatalf("create first pending-state failure trigger: %v", err)
	}

	result := callIndexRepositoryWithContext(
		t,
		context.Background(),
		srv,
		map[string]any{
			"repo_path":   repo,
			"skip_report": true,
		},
	)
	if result == nil || !result.IsError {
		t.Fatalf("first post-pipeline state failure result = %#v, want tool error", result)
	}

	record, err := st.GetIndexIdentity(project)
	if err != nil {
		t.Fatalf("GetIndexIdentity: %v", err)
	}
	if record.Status != indexidentity.StatusError {
		t.Fatalf("identity status = %q, want %q", record.Status, indexidentity.StatusError)
	}
	if !strings.Contains(record.Reason, "mark_identity_pending_failed") {
		t.Errorf("identity reason = %q, want mark_identity_pending_failed", record.Reason)
	}
	if !strings.Contains(record.Reason, "forced first completed-pending failure") {
		t.Errorf("identity reason = %q, want trigger failure detail", record.Reason)
	}
}

func TestWatcherPersistsTerminalIdentityErrorWhenPipelineFails(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	first := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	project, _ := first["project"].(string)
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatalf("Abs(repo): %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = srv.syncProject(ctx, project, absRepo)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("syncProject error = %v, want %v", err, context.Canceled)
	}

	st, err := router.ForProject(project)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	record, err := st.GetIndexIdentity(project)
	if err != nil {
		t.Fatalf("GetIndexIdentity: %v", err)
	}
	if record.Status != indexidentity.StatusError {
		t.Fatalf("identity status = %q, want %q", record.Status, indexidentity.StatusError)
	}
	if !strings.Contains(record.Reason, "watcher_indexing_failed") {
		t.Errorf("identity reason = %q, want watcher_indexing_failed", record.Reason)
	}
	if !strings.Contains(record.Reason, context.Canceled.Error()) {
		t.Errorf("identity reason = %q, want %q", record.Reason, context.Canceled)
	}
}

func TestAutoIndexRefreshesIndexIdentityForNewAndExistingProject(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatalf("Abs(repo): %v", err)
	}
	project := pipeline.ProjectNameFromPath(absRepo)
	srv.ctx = context.Background()
	srv.sessionRoot = absRepo
	srv.sessionProject = project

	if err := srv.runAutoIndex(false); err != nil {
		t.Fatalf("new-project runAutoIndex: %v", err)
	}
	st, err := router.ForProject(project)
	if err != nil {
		t.Fatalf("ForProject(new): %v", err)
	}
	firstRecord, err := st.GetIndexIdentity(project)
	if err != nil {
		t.Fatalf("GetIndexIdentity(new): %v", err)
	}
	if firstRecord.Status != indexidentity.StatusCaptured || firstRecord.Identity == nil {
		t.Fatalf("new-project auto-index identity = %+v, want captured", firstRecord)
	}
	firstGeneration := firstRecord.Identity.IndexGeneration

	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte(`def auto_index_changed():
    return 4
`), 0o600); err != nil {
		t.Fatalf("modify app.py: %v", err)
	}
	expected, err := indexidentity.Capture(absRepo)
	if err != nil {
		t.Fatalf("capture changed checkout: %v", err)
	}

	if err := srv.runAutoIndex(true); err != nil {
		t.Fatalf("existing-project runAutoIndex: %v", err)
	}
	secondRecord, err := st.GetIndexIdentity(project)
	if err != nil {
		t.Fatalf("GetIndexIdentity(existing): %v", err)
	}
	if secondRecord.Status != indexidentity.StatusCaptured || secondRecord.Identity == nil {
		t.Fatalf("existing-project auto-index identity = %+v, want captured", secondRecord)
	}
	if got := secondRecord.Identity.IndexGeneration; got != expected.IndexGeneration {
		t.Errorf("refreshed generation = %q, want current checkout generation %q", got, expected.IndexGeneration)
	}
	if secondRecord.Identity.IndexGeneration == firstGeneration {
		t.Error("existing-project auto-index left the previous generation attached")
	}
}

func TestStatusAndListReportStaleSourceWithoutReindex(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	indexResp := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	project, _ := indexResp["project"].(string)
	indexIdentity, ok := indexResp["index_identity"].(map[string]any)
	if !ok {
		t.Fatalf("index identity missing: %v", indexResp)
	}
	indexGeneration, _ := indexIdentity["index_generation"].(string)

	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte(`def stale():
    return 3
`), 0o600); err != nil {
		t.Fatalf("modify app.py: %v", err)
	}

	status := metadataResponseFromHandler(t, srv.handleIndexStatus, "index_status",
		map[string]any{"project": project})
	if got, _ := status["status"].(string); got != "degraded" {
		t.Errorf("index_status.status = %q, want degraded", got)
	}
	if got, _ := status["identity_status"].(string); got != indexidentity.StatusStaleSource {
		t.Errorf("index_status.identity_status = %q, want %q", got, indexidentity.StatusStaleSource)
	}
	if reason, _ := status["identity_reason"].(string); !strings.Contains(reason, "source_changed_since_index") {
		t.Errorf("index_status.identity_reason = %q, want source_changed_since_index", reason)
	}
	persisted, _ := status["index_identity"].(map[string]any)
	if got, _ := persisted["index_generation"].(string); got != indexGeneration {
		t.Errorf("index_status replaced persisted graph generation %q with live generation %q", indexGeneration, got)
	}
	metadata, _ := status["_metadata"].(map[string]any)
	freshness, _ := metadata["freshness"].(map[string]any)
	if got, _ := freshness["state"].(string); got != "stale" {
		t.Errorf("stale source metadata freshness = %q, want stale", got)
	}

	projects := listProjectsResponse(t, srv)
	if len(projects) != 1 {
		t.Fatalf("list_projects returned %d projects, want 1", len(projects))
	}
	entry := projects[0]
	if got, _ := entry["status"].(string); got != "degraded" {
		t.Errorf("list_projects.status = %q, want degraded", got)
	}
	if got, _ := entry["identity_status"].(string); got != indexidentity.StatusStaleSource {
		t.Errorf("list_projects.identity_status = %q, want %q", got, indexidentity.StatusStaleSource)
	}
	listIdentity, _ := entry["index_identity"].(map[string]any)
	if got, _ := listIdentity["index_generation"].(string); got != indexGeneration {
		t.Errorf("list_projects replaced persisted graph generation %q with live generation %q", indexGeneration, got)
	}
}

func TestStatusDegradesWhenLiveCheckoutChangesWithSameGeneration(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	indexResp := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	project, _ := indexResp["project"].(string)
	currentIdentity, err := indexidentity.Capture(repo)
	if err != nil {
		t.Fatalf("capture fixture identity: %v", err)
	}
	currentIdentity.CheckoutID = strings.Repeat("c", 64)
	srv.captureIndexIdentity = func(string) (*indexidentity.Envelope, error) {
		identity := *currentIdentity
		return &identity, nil
	}

	status := metadataResponseFromHandler(t, srv.handleIndexStatus, "index_status",
		map[string]any{"project": project})
	if got, _ := status["status"].(string); got != "degraded" {
		t.Errorf("index_status.status = %q, want degraded", got)
	}
	if got, _ := status["identity_status"].(string); got != indexidentity.StatusStaleSource {
		t.Errorf("index_status.identity_status = %q, want %q", got, indexidentity.StatusStaleSource)
	}
	if reason, _ := status["identity_reason"].(string); !strings.Contains(reason, "source_changed_since_index") {
		t.Errorf("index_status.identity_reason = %q, want source_changed_since_index", reason)
	}
}

func TestIndexRepositoryFirstIndexReportsCreatedWithCounts(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
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
