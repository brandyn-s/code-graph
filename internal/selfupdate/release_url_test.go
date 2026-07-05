package selfupdate

import (
	"strings"
	"testing"
)

// TestReleaseURLPointsAtFork pins the self-update endpoint to the redacted
// fork. `codebase-memory-mcp update` replaces the running binary with
// whatever this URL serves — an upstream (DeusData) URL would silently
// swap in a build without any redacted additions.
func TestReleaseURLPointsAtFork(t *testing.T) {
	if !strings.Contains(ReleaseURL, "redacted-org/code-graph") {
		t.Errorf("ReleaseURL must point at the redacted fork, got %q", ReleaseURL)
	}
	if strings.Contains(ReleaseURL, "DeusData") {
		t.Errorf("ReleaseURL must not point at upstream DeusData, got %q", ReleaseURL)
	}
}
