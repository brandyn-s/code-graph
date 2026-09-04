package selfupdate

import (
	"strings"
	"testing"
)

// TestReleaseURLPointsAtFork pins the self-update endpoint to this fork.
// `code-graph update` replaces the running binary with whatever this URL
// serves — an upstream (DeusData) URL would silently swap in a build without
// any of this fork's additions.
func TestReleaseURLPointsAtFork(t *testing.T) {
	if !strings.Contains(ReleaseURL, "brandyn-s/code-graph") {
		t.Errorf("ReleaseURL must point at this fork, got %q", ReleaseURL)
	}
	if strings.Contains(ReleaseURL, "DeusData") {
		t.Errorf("ReleaseURL must not point at upstream DeusData, got %q", ReleaseURL)
	}
}
