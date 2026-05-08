package httplink

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LinkerConfig holds user-overridable HTTP linker settings.
// Loaded from .cgrconfig in the project root.
type LinkerConfig struct {
	HTTPLinker HTTPLinkerConfig `yaml:"http_linker"`
}

// HTTPLinkerConfig holds HTTP linker-specific settings.
type HTTPLinkerConfig struct {
	// ExcludePaths are route paths to exclude from HTTP_CALLS matching.
	// These are added to (not replacing) the built-in defaultExcludePaths.
	ExcludePaths []string `yaml:"exclude_paths"`

	// MinConfidence is the minimum confidence score for creating HTTP_CALLS edges.
	// Default: 0.25 (includes speculative matches).
	MinConfidence *float64 `yaml:"min_confidence"`

	// FuzzyMatching enables/disables fuzzy URL matching.
	// Default: true.
	FuzzyMatching *bool `yaml:"fuzzy_matching"`
}

// DefaultConfig returns the default linker configuration.
func DefaultConfig() *LinkerConfig {
	return &LinkerConfig{}
}

// LoadConfig reads .cgrconfig from the given directory.
// Returns default config if the file doesn't exist.
func LoadConfig(dir string) *LinkerConfig {
	cfg := DefaultConfig()

	path := filepath.Join(dir, ".cgrconfig")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg // file not found or unreadable — use defaults
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return DefaultConfig() // invalid YAML — use defaults
	}

	return cfg
}

// EffectiveMinConfidence returns the configured minimum confidence,
// or the default if not set.
//
// Phase E (2026-05-08): default raised from 0.25 to 0.36. PSM
// hand-labeling of all 98 HTTP_CALLS edges (Phase C state) revealed
// that conf < 0.36 is dominated by partial-match FPs targeting
// catch-all functions (`get_payload`, `userCertHandler`) — 14 FPs
// dropped vs 2 TP losses among the 16 below-threshold edges (7:1
// FP-drop / TP-loss ratio). The prior 0.25 default was set when the
// resolver couldn't reach most real handlers (PSM HTTP_CALLS = 17
// pre-Phase-C); now that the actix-builder extractor (PR #261) and
// HandlerRef resolution (PR #257) reach 98 real edges, the
// speculative band is no longer needed to surface load-bearing
// matches.
func (c *LinkerConfig) EffectiveMinConfidence() float64 {
	if c.HTTPLinker.MinConfidence != nil {
		return *c.HTTPLinker.MinConfidence
	}
	return 0.36
}

// EffectiveFuzzyMatching returns the configured fuzzy matching setting,
// or the default (true) if not set.
func (c *LinkerConfig) EffectiveFuzzyMatching() bool {
	if c.HTTPLinker.FuzzyMatching != nil {
		return *c.HTTPLinker.FuzzyMatching
	}
	return true
}

// AllExcludePaths returns the combined list of default + user-configured exclude paths.
func (c *LinkerConfig) AllExcludePaths() []string {
	combined := make([]string, 0, len(defaultExcludePaths)+len(c.HTTPLinker.ExcludePaths))
	combined = append(combined, defaultExcludePaths...)
	combined = append(combined, c.HTTPLinker.ExcludePaths...)
	return combined
}

// isPathExcluded checks if a route path matches any of the given exclusion paths.
func isPathExcluded(path string, excludePaths []string) bool {
	normalized := strings.ToLower(strings.TrimRight(path, "/"))
	for _, excluded := range excludePaths {
		if strings.EqualFold(normalized, strings.TrimRight(excluded, "/")) {
			return true
		}
	}
	return false
}
