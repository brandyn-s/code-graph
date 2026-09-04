package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureDB copies the committed format-v1 database (built from
// bench/accuracy/synthetic/go-minimal by the release it was captured with)
// into a temp dir so tests can mutate it.
func fixtureDB(t *testing.T) string {
	t.Helper()
	src := filepath.Join("testdata", "format-v1", "go-minimal.db")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), "go-minimal.db")
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dst
}

func setUserVersion(t *testing.T, path string, version int) {
	t.Helper()
	s, err := OpenPath(path)
	if err != nil {
		t.Fatalf("open to stamp: %v", err)
	}
	if err := stampFormatVersion(t.Context(), s.db, version); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFormatVersion_CurrentFixtureOpensAndIsStamped(t *testing.T) {
	path := fixtureDB(t)
	s, err := OpenPath(path)
	if err != nil {
		t.Fatalf("open current-format fixture: %v", err)
	}
	defer s.Close()
	version, err := s.FormatVersionOf()
	if err != nil {
		t.Fatal(err)
	}
	if version != FormatVersion {
		t.Fatalf("fixture format = %d, want %d", version, FormatVersion)
	}
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("fixture projects = %v, want exactly one", projects)
	}
	count, err := s.CountNodes(projects[0].Name)
	if err != nil || count == 0 {
		t.Fatalf("fixture nodes = %d, err = %v", count, err)
	}
}

func TestFormatVersion_FreshDatabaseIsStamped(t *testing.T) {
	s, err := OpenPath(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	version, err := s.FormatVersionOf()
	if err != nil {
		t.Fatal(err)
	}
	if version != FormatVersion {
		t.Fatalf("fresh format = %d, want %d", version, FormatVersion)
	}
}

func TestFormatVersion_LegacyUnstampedDatabaseIsAdopted(t *testing.T) {
	path := fixtureDB(t)
	setUserVersion(t, path, 0)
	s, err := OpenPath(path)
	if err != nil {
		t.Fatalf("legacy unstamped database must open: %v", err)
	}
	defer s.Close()
	version, err := s.FormatVersionOf()
	if err != nil {
		t.Fatal(err)
	}
	if version != FormatVersion {
		t.Fatalf("adopted format = %d, want %d", version, FormatVersion)
	}
}

func TestFormatVersion_NewerDatabaseIsRefusedWithUpgradeHint(t *testing.T) {
	path := fixtureDB(t)
	setUserVersion(t, path, FormatVersion+7)
	_, err := OpenPath(path)
	if !errors.Is(err, ErrIndexFormatTooNew) {
		t.Fatalf("err = %v, want ErrIndexFormatTooNew", err)
	}
	for _, want := range []string{"newer code-graph", "upgrade code-graph", "re-index"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestFormatVersion_UnsupportedOlderDatabaseAsksForRebuild(t *testing.T) {
	path := fixtureDB(t)
	setUserVersion(t, path, -1)
	_, err := OpenPath(path)
	if !errors.Is(err, ErrIndexFormatUnsupported) {
		t.Fatalf("err = %v, want ErrIndexFormatUnsupported", err)
	}
	if !strings.Contains(err.Error(), "rebuild the index") {
		t.Errorf("error %q lacks rebuild guidance", err)
	}
}
