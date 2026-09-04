package tools

import (
	"strings"
	"testing"
)

// TestReleaseURLPointsAtFork pins the update-check endpoint to this fork.
// An upstream (DeusData) URL is a footgun: upstream's tags compare newer than
// the pre-public internal tags, so the notice would tell operators to run
// `code-graph update`, replacing the fork binary with an upstream build that
// lacks every addition made in this fork.
func TestReleaseURLPointsAtFork(t *testing.T) {
	if !strings.Contains(releaseURL, "brandyn-s/code-graph") {
		t.Errorf("releaseURL must point at this fork, got %q", releaseURL)
	}
	if strings.Contains(releaseURL, "DeusData") {
		t.Errorf("releaseURL must not point at upstream DeusData, got %q", releaseURL)
	}
}
