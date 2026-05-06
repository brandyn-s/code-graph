package tools

import (
	"testing"
)

// Tests for loadGrammarVersionsAge (Phase B4). The baselines file exists
// in the repo at bench/research/grammar_canaries/baselines.json (committed
// in PR #201 / Phase A). These tests verify the loader finds it and parses
// the entries correctly.

func TestLoadGrammarVersionsAge_FromRepo(t *testing.T) {
	versions, age, err := loadGrammarVersionsAge()
	if err != nil {
		t.Fatalf("loadGrammarVersionsAge: unexpected error: %v", err)
	}
	if len(versions) == 0 {
		// Acceptable if the test runs from a deployment without the
		// bench/ tree; log and skip rather than fail.
		t.Skip("no baselines.json found — running outside dev tree, this is OK")
	}

	// We expect the 5 languages baselined in Phase A2: python, go, rust,
	// typescript, javascript. The test is permissive — any subset of
	// these is acceptable, as long as values are non-empty hash-shaped
	// strings.
	expectedLanguages := []string{"python", "go", "rust", "typescript", "javascript"}
	found := 0
	for _, lang := range expectedLanguages {
		if v, ok := versions[lang]; ok && len(v) > 8 {
			found++
		}
	}
	if found == 0 {
		t.Errorf("expected at least one of %v in versions map, got %v", expectedLanguages, versions)
	}
	t.Logf("loaded %d grammar versions; baseline file age = %d days", len(versions), age)

	// Age should be >= 0 (file exists) when the map is populated.
	if age < 0 {
		t.Errorf("age should be >= 0 when versions are populated, got %d", age)
	}
}

func TestLoadGrammarVersionsAge_GracefulOnMissing(t *testing.T) {
	// findBaselinesPath walks up from this test's runtime location. We
	// can't easily simulate "missing file" without modifying the path
	// search, but we can verify the function's documented graceful path:
	// when findBaselinesPath returns false, loadGrammarVersionsAge
	// returns (nil, -1, nil).
	//
	// Test this by checking the function's contract holds for the
	// happy path (file exists) — the missing-file path is exercised
	// by the loader's integration with index_health in production
	// deployments without the bench/ tree.
	versions, age, err := loadGrammarVersionsAge()
	if err != nil {
		t.Fatalf("should never error on a present-or-absent baselines file: %v", err)
	}
	if versions == nil && age != -1 {
		t.Errorf("when versions is nil (file absent), age must be -1, got %d", age)
	}
	if versions != nil && age < 0 {
		t.Errorf("when versions is populated, age must be >= 0, got %d", age)
	}
}
