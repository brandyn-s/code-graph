package httplink

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefault(t *testing.T) {
	cfg := LoadConfig("/nonexistent/path")
	if cfg.EffectiveMinConfidence() != 0.36 {
		t.Errorf("expected default min_confidence 0.36 (Phase E PSM-tuned), got %f", cfg.EffectiveMinConfidence())
	}
	if !cfg.EffectiveFuzzyMatching() {
		t.Error("expected default fuzzy_matching true")
	}
	paths := cfg.AllExcludePaths()
	if len(paths) != len(defaultExcludePaths) {
		t.Errorf("expected %d default paths, got %d", len(defaultExcludePaths), len(paths))
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	configContent := `
http_linker:
  exclude_paths:
    - /debug
    - /internal/status
  min_confidence: 0.5
  fuzzy_matching: false
`
	if err := os.WriteFile(filepath.Join(dir, ".cgrconfig"), []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig(dir)
	if cfg.EffectiveMinConfidence() != 0.5 {
		t.Errorf("expected min_confidence 0.5, got %f", cfg.EffectiveMinConfidence())
	}
	if cfg.EffectiveFuzzyMatching() {
		t.Error("expected fuzzy_matching false")
	}
	paths := cfg.AllExcludePaths()
	expectedLen := len(defaultExcludePaths) + 2
	if len(paths) != expectedLen {
		t.Errorf("expected %d total paths, got %d", expectedLen, len(paths))
	}
}

// Phase E (2026-05-08): pin the 0.36 default. PSM hand-labeling at
// Phase C state (98 HTTP_CALLS) showed the previous 0.25 default
// admitted 14+ partial-match FPs targeting catch-all functions
// (`get_payload`, `userCertHandler`). Raising the floor cuts the FP
// band at a 7:1 (FP-drop / TP-loss) ratio. Drift in either direction
// (back to 0.25 = readmit FPs; up past ~0.45 = drop real low-signal
// matches) breaks this test.
func TestEffectiveMinConfidence_PSMTunedDefault(t *testing.T) {
	cfg := &LinkerConfig{}
	got := cfg.EffectiveMinConfidence()
	if got != 0.36 {
		t.Errorf("default min_confidence = %f, want 0.36 (Phase E PSM-tuned)", got)
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".cgrconfig"), []byte("not: [valid: yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(dir)
	// Should fall back to defaults
	if cfg.EffectiveMinConfidence() != 0.36 {
		t.Errorf("expected default on invalid yaml, got %f", cfg.EffectiveMinConfidence())
	}
}

func TestIsPathExcluded(t *testing.T) {
	paths := []string{"/health", "/debug", "/internal/status"}

	tests := []struct {
		path string
		want bool
	}{
		{"/health", true},
		{"/health/", true},
		{"/HEALTH", true},
		{"/debug", true},
		{"/internal/status", true},
		{"/api/orders", false},
		{"/healthcheck", false},
	}
	for _, tt := range tests {
		got := isPathExcluded(tt.path, paths)
		if got != tt.want {
			t.Errorf("isPathExcluded(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestAllExcludePathsMerge(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HTTPLinker.ExcludePaths = []string{"/custom1", "/custom2"}

	paths := cfg.AllExcludePaths()

	// Should contain all defaults + custom
	if len(paths) != len(defaultExcludePaths)+2 {
		t.Errorf("expected %d paths, got %d", len(defaultExcludePaths)+2, len(paths))
	}

	// Verify defaults are first
	for i, dp := range defaultExcludePaths {
		if paths[i] != dp {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], dp)
		}
	}

	// Verify custom paths are appended
	customStart := len(defaultExcludePaths)
	if paths[customStart] != "/custom1" {
		t.Errorf("paths[%d] = %q, want /custom1", customStart, paths[customStart])
	}
	if paths[customStart+1] != "/custom2" {
		t.Errorf("paths[%d] = %q, want /custom2", customStart+1, paths[customStart+1])
	}
}
