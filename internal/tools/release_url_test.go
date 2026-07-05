package tools

import (
	"strings"
	"testing"
)

// TestReleaseURLPointsAtFork pins the update-check endpoint to the redacted
// fork. An upstream (DeusData) URL is a footgun: upstream's 0.8.x tags
// compare newer than 0.7.0-redacted.x, so the notice would tell operators to
// run `codebase-memory-mcp update`, replacing the fork binary with an
// upstream build that lacks every redacted addition.
func TestReleaseURLPointsAtFork(t *testing.T) {
	if !strings.Contains(releaseURL, "redacted-org/code-graph") {
		t.Errorf("releaseURL must point at the redacted fork, got %q", releaseURL)
	}
	if strings.Contains(releaseURL, "DeusData") {
		t.Errorf("releaseURL must not point at upstream DeusData, got %q", releaseURL)
	}
}
