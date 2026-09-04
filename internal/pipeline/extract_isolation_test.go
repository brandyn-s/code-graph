package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/brandyn-s/code-graph/internal/cbm/isolate"
	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/store"
)

// A file that crashes the native extractor must be skipped and reported while
// the rest of the repository is indexed (upstream fb334f78 in spirit). The
// crash is injected in the worker via CBM_TEST_CRASH_ON so the test exercises
// the real supervisor path, not a fixture that might stop faulting.
func TestIndexingSurvivesACrashingFileAndReportsIt(t *testing.T) {
	if os.Getenv("CI_NO_SUBPROCESS") != "" {
		t.Skip("subprocess tests disabled")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_GRAPH_EXTRACT_ISOLATION", "on")
	t.Setenv("CODE_GRAPH_EXTRACT_FILE_TIMEOUT_S", "20")
	cacheDir := t.TempDir()
	t.Setenv("CODE_GRAPH_CACHE_DIR", cacheDir)
	ResetIsolationForTests()
	SetIsolationCommandFactory(func() *exec.Cmd {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "GO_ISOLATE_WORKER=1", isolate.EnvTestCrashOn+"=crashme")
		cmd.Stderr = os.Stderr
		return cmd
	})
	t.Cleanup(ResetIsolationForTests)

	// Fixture under the repo-root _fixtures directory: the indexer refuses
	// system temp roots on macOS.
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "_fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fixtureRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	repo, err := os.MkdirTemp(fixtureRoot, "isolation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture\n\ngo 1.22\n")
	write("ok.go", "package fixture\n\nfunc Keep() int { return 1 }\n\nfunc Also() int { return Keep() + 1 }\n")
	write("crashme.go", "package fixture\n\nfunc Boom() int { return 2 }\n")

	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p := New(context.Background(), st, repo, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("indexing must survive a crashing file, got: %v", err)
	}

	skipped := p.SkippedFiles()
	if len(skipped) != 1 || skipped[0].Path != "crashme.go" || skipped[0].Reason != string(isolate.SkipCrash) {
		t.Fatalf("skipped = %+v, want exactly crashme.go as a crash", skipped)
	}

	// The healthy file was indexed.
	nodes, err := st.FindNodesByName(p.ProjectName, "Keep")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Fatal("Keep from ok.go was not indexed")
	}
	boom, err := st.FindNodesByName(p.ProjectName, "Boom")
	if err != nil {
		t.Fatal(err)
	}
	if len(boom) != 0 {
		t.Fatal("Boom from the crashing file should not have been indexed")
	}

	// The sidecar reports it for index_health and doctor.
	fromSidecar, err := store.ReadSkips(p.ProjectName)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromSidecar) != 1 || fromSidecar[0].Path != "crashme.go" {
		t.Fatalf("sidecar = %+v", fromSidecar)
	}
	if st := IsolationStats(); st.Crashes != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestIsolationAutoIsOffUnderGoTest(t *testing.T) {
	t.Setenv("CODE_GRAPH_EXTRACT_ISOLATION", "")
	if IsolationEnabled() {
		t.Fatal("auto mode must resolve to off inside a test binary")
	}
	t.Setenv("CODE_GRAPH_EXTRACT_ISOLATION", "on")
	if !IsolationEnabled() {
		t.Fatal("explicit on must win")
	}
	t.Setenv("CODE_GRAPH_EXTRACT_FILE_TIMEOUT_S", "2.5")
	if got := IsolationFileTimeout(); got.Seconds() != 2.5 {
		t.Fatalf("timeout = %s", got)
	}
}
