package tools

// Sticky per-project report preference (2026-06-11; write_report/skip_report).
//
// Incident: a repo indexed with skip_report=true on every explicit call
// grew an ARCHITECTURE_REPORT.md anyway — some later index_repository
// call omitted the flag and the default silently reverted to
// report-writing, and the write was unattributable after the fact.
// These tests pin the sticky contract: an explicit choice persists to
// the config store; omitted-arg calls inherit it; an explicit opposite
// choice overwrites it; and the no-preference default stays
// report-writing (upstream behavior unchanged).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brandyn-s/code-graph/internal/pipeline"
	"github.com/brandyn-s/code-graph/internal/store"
)

func indexArgs(repo string, extra map[string]any) map[string]any {
	args := map[string]any{"repo_path": repo}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func TestReportPreferenceIsSticky(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	cfg, err := store.OpenConfigInDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenConfigInDir: %v", err)
	}
	t.Cleanup(func() { _ = cfg.Close() })
	srv := NewServer(router, WithConfig(cfg))

	repo := writeFixtureRepo(t)
	project := pipeline.ProjectNameFromPath(repo)
	cachedReport := filepath.Join(srv.reportsDir(project), ReportFileName)
	repoReport := filepath.Join(repo, ReportFileName)
	exists := func(path string) bool {
		_, statErr := os.Stat(path)
		return statErr == nil
	}

	// 1. Explicit opt-in (preferred spelling): report under the cache dir,
	// never in the checkout, preference recorded.
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, map[string]any{"write_report": true, "force": true}))
	if !exists(cachedReport) {
		t.Fatal("write_report=true did not write a report under the cache dir")
	}
	if exists(repoReport) {
		t.Fatal("write_report=true wrote into the checkout without report_path")
	}

	// 2. Arg OMITTED: the recorded want-report choice holds (refresh).
	if err := os.Remove(cachedReport); err != nil {
		t.Fatalf("remove report: %v", err)
	}
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, map[string]any{"force": true}))
	if !exists(cachedReport) {
		t.Fatal("omitted arguments did not inherit the persisted want-report preference")
	}

	// 3. Legacy spelling flips it off and is persisted.
	if err := os.Remove(cachedReport); err != nil {
		t.Fatalf("remove report: %v", err)
	}
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, map[string]any{"skip_report": true, "force": true}))
	if exists(cachedReport) {
		t.Fatal("explicit skip_report=true wrote a report")
	}

	// 4. Omitted again: now inherits the skip choice — the original incident case.
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, map[string]any{"force": true}))
	if exists(cachedReport) || exists(repoReport) {
		t.Fatal("omitted arguments reverted to report-writing despite persisted skip preference")
	}

	// 5. Legacy skip_report=false still means "write" and lands in the cache dir.
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, map[string]any{"skip_report": false, "force": true}))
	if !exists(cachedReport) {
		t.Fatal("explicit skip_report=false did not write a report")
	}
	if exists(repoReport) {
		t.Fatal("skip_report=false wrote into the checkout")
	}
}

func TestReportDefaultWritesNothingWithoutPreference(t *testing.T) {
	// No config store attached (CLI/test shape) + omitted args = no report
	// anywhere: not in the checkout, not in the cache dir.
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)

	repo := writeFixtureRepo(t)
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, nil))
	if _, statErr := os.Stat(filepath.Join(repo, ReportFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("default index wrote a report into the checkout (stat err=%v)", statErr)
	}
	project := pipeline.ProjectNameFromPath(repo)
	if _, statErr := os.Stat(filepath.Join(srv.reportsDir(project), ReportFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("default index wrote a report without being asked (stat err=%v)", statErr)
	}
}
