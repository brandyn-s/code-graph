package pipeline

// SCIP index-path resolution tests.
//
// WHY AUTO-DISCOVERY EXISTS: CBM_SCIP_INDEX_PATH is a SINGLE global path
// consumed by passSCIPIngest for whatever repo is currently being indexed. That
// makes it unusable as a persistent setting once more than one repo is indexed —
// exporting it in a launcher would point every repo at one repo's index (a
// silent no-op, since the drift guard excludes documents whose definition sites
// don't match). So the measured precision gain evaporated on the next re-index
// unless the operator happened to re-export the variable in that shell.
//
// The per-repo <repo>/index.scip convention makes the tier persistent. That name
// is not invented here: it is what the validation runbook and CLAUDE.md have
// used since the tier shipped (`rust-analyzer scip . --output index.scip`).

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSCIPStub(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	// Contents are irrelevant to path resolution; runSCIPIngest parses it and
	// degrades to a warning on garbage.
	if err := os.WriteFile(path, []byte("not-a-real-scip-index"), 0o600); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestSCIPIndexPath_EnvWins(t *testing.T) {
	repo := t.TempDir()
	inRepo := writeSCIPStub(t, repo, scipIndexDefaultName)
	other := writeSCIPStub(t, t.TempDir(), "explicit.scip")

	t.Setenv(scipIndexPathEnv, other)

	path, source := scipIndexPath(repo)
	if path != other {
		t.Errorf("path = %q, want the env override %q (in-repo was %q)", path, other, inRepo)
	}
	if source != "env:"+scipIndexPathEnv {
		t.Errorf("source = %q, want env provenance", source)
	}
}

func TestSCIPIndexPath_RepoDefaultDiscovered(t *testing.T) {
	t.Setenv(scipIndexPathEnv, "")
	t.Setenv(scipAutoDiscoverEnv, "1")
	repo := t.TempDir()
	want := writeSCIPStub(t, repo, scipIndexDefaultName)

	path, source := scipIndexPath(repo)
	if path != want {
		t.Errorf("path = %q, want the in-repo default %q", path, want)
	}
	if source != "repo-default:"+scipIndexDefaultName {
		t.Errorf("source = %q, want repo-default provenance", source)
	}
}

// TestSCIPIndexPath_DiscoveryOffByDefault is THE backward-compatibility guard.
// An index.scip sitting in a repo is NOT consent: without the discovery gate a
// stale index would silently start rewriting CALLS edges on the next re-index
// with no operator action. This pins that the pre-existing contract (env var is
// the only switch) still holds — the same property TestSCIPIngestInertWithoutEnv
// asserts from the pass side, which broke when discovery was on-by-default.
func TestSCIPIndexPath_DiscoveryOffByDefault(t *testing.T) {
	t.Setenv(scipIndexPathEnv, "")
	t.Setenv(scipAutoDiscoverEnv, "")
	repo := t.TempDir()
	writeSCIPStub(t, repo, scipIndexDefaultName) // index PRESENT but unrequested

	if path, source := scipIndexPath(repo); path != "" || source != "" {
		t.Errorf("discovery fired without opt-in: path=%q source=%q", path, source)
	}
}

// TestSCIPIndexPath_OffByDefault: no index and no env var keeps the pass inert.
func TestSCIPIndexPath_OffByDefault(t *testing.T) {
	t.Setenv(scipIndexPathEnv, "")
	t.Setenv(scipAutoDiscoverEnv, "1")
	path, source := scipIndexPath(t.TempDir()) // empty repo, no index.scip

	if path != "" {
		t.Errorf("path = %q, want empty (pass must stay off)", path)
	}
	if source != "" {
		t.Errorf("source = %q, want empty", source)
	}
}

func TestSCIPIndexPath_EmptyRepoPathIsSafe(t *testing.T) {
	t.Setenv(scipIndexPathEnv, "")
	t.Setenv(scipAutoDiscoverEnv, "1")
	if path, _ := scipIndexPath(""); path != "" {
		t.Errorf("path = %q, want empty for an unset repo root", path)
	}
}

// TestSCIPIndexPath_DirectoryNotMistakenForIndex: a directory named index.scip
// must not be treated as a readable index (os.ReadFile on it would error and
// only surface as a warning, so catching it here keeps the log clean).
func TestSCIPIndexPath_DirectoryNotMistakenForIndex(t *testing.T) {
	t.Setenv(scipIndexPathEnv, "")
	t.Setenv(scipAutoDiscoverEnv, "1")
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, scipIndexDefaultName), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if path, _ := scipIndexPath(repo); path != "" {
		t.Errorf("path = %q, want empty when index.scip is a directory", path)
	}
}
