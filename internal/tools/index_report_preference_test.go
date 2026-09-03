package tools

// Sticky per-project skip_report preference (2026-06-11).
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

	"github.com/brandyn-s/code-graph/internal/store"
)

func indexArgs(repo string, extra map[string]any) map[string]any {
	args := map[string]any{"repo_path": repo}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func TestSkipReportPreferenceIsSticky(t *testing.T) {
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
	reportPath := filepath.Join(repo, "ARCHITECTURE_REPORT.md")
	reportExists := func() bool {
		_, statErr := os.Stat(reportPath)
		return statErr == nil
	}

	// 1. Explicit skip: no report, preference recorded.
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, map[string]any{"skip_report": true, "force": true}))
	if reportExists() {
		t.Fatal("explicit skip_report=true wrote a report")
	}

	// 2. Arg OMITTED: the recorded skip must hold — this is the incident case.
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, map[string]any{"force": true}))
	if reportExists() {
		t.Fatal("omitted skip_report reverted to report-writing despite persisted skip preference")
	}

	// 3. Explicit opposite: report written, preference flipped.
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, map[string]any{"skip_report": false, "force": true}))
	if !reportExists() {
		t.Fatal("explicit skip_report=false did not write a report")
	}

	// 4. Omitted again: now inherits the want-report choice (refresh).
	if err := os.Remove(reportPath); err != nil {
		t.Fatalf("remove report: %v", err)
	}
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, map[string]any{"force": true}))
	if !reportExists() {
		t.Fatal("omitted skip_report did not inherit the persisted want-report preference")
	}
}

func TestSkipReportDefaultUnchangedWithoutPreference(t *testing.T) {
	// No config store attached (CLI/test shape) + omitted arg = the
	// pre-existing default: write the report.
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)

	repo := writeFixtureRepo(t)
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		indexArgs(repo, nil))
	if _, statErr := os.Stat(filepath.Join(repo, "ARCHITECTURE_REPORT.md")); statErr != nil {
		t.Fatalf("default (no preference, nil config) should write the report: %v", statErr)
	}
}
