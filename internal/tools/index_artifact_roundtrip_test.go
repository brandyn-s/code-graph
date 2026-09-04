package tools

import (
	"path/filepath"
	"testing"

	"github.com/brandyn-s/code-graph/internal/artifact"
	"github.com/brandyn-s/code-graph/internal/pipeline"
	"github.com/brandyn-s/code-graph/internal/store"
)

// A default index_repository run must leave the checkout untouched, so the
// exported artifact matches the local checkout and imports coherently
// without --allow-stale. Before reports moved out of the checkout this needed
// skip_report=true or the import was refused as stale.
func TestDefaultIndexExportsArtifactThatImportsWithoutAllowStale(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo := writeFixtureRepo(t)
	initCommittedFixtureRepo(t, repo)

	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo})
	if got := gitPorcelain(t, repo); got != "" {
		t.Fatalf("default index dirtied the checkout:\n%s", got)
	}

	project := pipeline.ProjectNameFromPath(repo)
	st, err := router.ForProject(project)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	out := filepath.Join(t.TempDir(), "graph.cgraph.zst")
	header, err := artifact.Export(t.Context(), st, project, out, "test")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if header.IdentityStatus == "" {
		t.Fatal("exported artifact carries no identity")
	}

	report, err := artifact.Import(t.Context(), out, artifact.ImportOptions{
		RepoPath:    repo,
		CacheDir:    t.TempDir(),
		ProjectName: pipeline.ProjectNameFromPath,
	})
	if err != nil {
		t.Fatalf("import without --allow-stale should succeed for an untouched checkout: %v", err)
	}
	if report.Stale {
		t.Fatalf("import reported stale (%s) for an untouched checkout", report.StaleReason)
	}
}
