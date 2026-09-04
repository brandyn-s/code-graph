package artifact

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/indexidentity"
	"github.com/brandyn-s/code-graph/internal/store"
)

func projectName(abs string) string {
	name := strings.ReplaceAll(filepath.ToSlash(abs), "/", "-")
	return strings.TrimLeft(name, "-")
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", "https://example.com/team/repo.git")
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("def f():\n    return 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "app.py")
	run("commit", "-q", "-m", "init")
	return dir
}

// seed builds a small indexed database for repo in cacheDir and returns the
// project name.
func seed(t *testing.T, cacheDir, repo string, withIdentity bool) string {
	t.Helper()
	project := projectName(repo)
	st, err := store.OpenInDir(cacheDir, project)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertProject(project, repo); err != nil {
		t.Fatal(err)
	}
	mod, err := st.UpsertNode(&store.Node{Project: project, Label: "Module", Name: "app", QualifiedName: project + ".app", FilePath: "app.py"})
	if err != nil {
		t.Fatal(err)
	}
	fn, err := st.UpsertNode(&store.Node{Project: project, Label: "Function", Name: "f", QualifiedName: project + ".app.f", FilePath: "app.py",
		Properties: map[string]any{"parent_qn": project + ".app", "unresolved_call_count": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertEdge(&store.Edge{Project: project, SourceID: mod, TargetID: fn, Type: "CONTAINS", Properties: map[string]any{"target_qn": project + ".app.f"}}); err != nil {
		t.Fatal(err)
	}
	if withIdentity {
		env, err := indexidentity.Capture(repo)
		if err != nil {
			t.Skipf("identity capture: %v", err)
		}
		if err := st.SetIndexIdentity(project, env); err != nil {
			t.Fatal(err)
		}
	} else if err := st.SetIndexIdentityState(project, indexidentity.StatusMissing, "legacy"); err != nil {
		t.Fatal(err)
	}
	return project
}

func TestExportImportRoundTripRenamesProjectForTheLocalCheckout(t *testing.T) {
	repoA := gitRepo(t)
	cacheA := t.TempDir()
	projectA := seed(t, cacheA, repoA, true)

	st, err := store.OpenInDir(cacheA, projectA)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "team"+Extension)
	h, err := Export(context.Background(), st, projectA, out, "test-version")
	_ = st.Close()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if h.NodeCount != 2 || h.EdgeCount != 1 || h.Identity == nil || h.IdentityStatus != indexidentity.StatusCaptured {
		t.Fatalf("header = %+v", h)
	}
	if h2, _, err := ReadHeader(out); err != nil || h2.PayloadSHA256 != h.PayloadSHA256 || h2.Project != projectA {
		t.Fatalf("ReadHeader = %+v, %v", h2, err)
	}

	// A teammate's clone of the same repository at a different path.
	repoB := t.TempDir()
	repoB, _ = filepath.EvalSymlinks(repoB)
	cp := exec.Command("git", "clone", "-q", repoA, repoB)
	if outB, err := cp.CombinedOutput(); err != nil {
		t.Skipf("git clone: %v (%s)", err, outB)
	}
	// Point origin at the same remote so repository_id matches.
	setURL := exec.Command("git", "-C", repoB, "remote", "set-url", "origin", "https://example.com/team/repo.git")
	if outB, err := setURL.CombinedOutput(); err != nil {
		t.Fatalf("set-url: %v (%s)", err, outB)
	}
	cacheB := t.TempDir()
	report, err := Import(context.Background(), out, ImportOptions{RepoPath: repoB, CacheDir: cacheB, ProjectName: projectName})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	projectB := projectName(repoB)
	if report.Project != projectB || !report.Renamed || report.Stale {
		t.Fatalf("report = %+v", report)
	}
	stB, err := store.OpenInDir(cacheB, projectB)
	if err != nil {
		t.Fatal(err)
	}
	defer stB.Close()
	proj, err := stB.GetProject(projectB)
	if err != nil || proj == nil || proj.RootPath != repoB {
		t.Fatalf("project row = %+v, %v", proj, err)
	}
	if old, _ := stB.GetProject(projectA); old != nil {
		t.Fatalf("old project row survived: %+v", old)
	}
	nodes, err := stB.FindNodesByName(projectB, "f")
	if err != nil || len(nodes) != 1 {
		t.Fatalf("f nodes = %v, %v", nodes, err)
	}
	if nodes[0].QualifiedName != projectB+".app.f" || nodes[0].Properties["parent_qn"] != projectB+".app" {
		t.Errorf("qualified names not rewritten: %s %v", nodes[0].QualifiedName, nodes[0].Properties)
	}
	edges, err := stB.FindEdgesByType(projectB, "CONTAINS")
	if err != nil || len(edges) != 1 || edges[0].Properties["target_qn"] != projectB+".app.f" {
		t.Fatalf("edges = %+v, %v", edges, err)
	}
	rec, err := stB.GetIndexIdentity(projectB)
	if err != nil || rec.Status != indexidentity.StatusCaptured || rec.Identity == nil {
		t.Fatalf("identity = %+v, %v", rec, err)
	}
	if rec.Identity.CheckoutID == h.Identity.CheckoutID || rec.Identity.SourceRevision != h.Identity.SourceRevision {
		t.Errorf("imported identity should keep the revision and take the local checkout id: %+v", rec.Identity)
	}

	// Importing again without --force is refused; with it, replaced.
	if _, err := Import(context.Background(), out, ImportOptions{RepoPath: repoB, CacheDir: cacheB, ProjectName: projectName}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second import err = %v", err)
	}
	_ = stB.Close()
	if _, err := Import(context.Background(), out, ImportOptions{RepoPath: repoB, CacheDir: cacheB, ProjectName: projectName, Force: true}); err != nil {
		t.Fatalf("forced import: %v", err)
	}
}

func TestImportRefusesStaleUnlessAllowed(t *testing.T) {
	repo := gitRepo(t)
	cache := t.TempDir()
	project := seed(t, cache, repo, true)
	st, err := store.OpenInDir(cache, project)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "stale"+Extension)
	if _, err := Export(context.Background(), st, project, out, "v"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	// Local tree moves on: an uncommitted edit makes the checkout dirty.
	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte("def f():\n    return 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	_, err = Import(context.Background(), out, ImportOptions{RepoPath: repo, CacheDir: target, ProjectName: projectName})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("expected ErrStale, got %v", err)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("refused import must leave nothing behind: %v", entries)
	}
	report, err := Import(context.Background(), out, ImportOptions{RepoPath: repo, CacheDir: target, ProjectName: projectName, AllowStale: true})
	if err != nil {
		t.Fatalf("allow-stale import: %v", err)
	}
	if !report.Stale || !strings.Contains(report.StaleReason, "uncommitted") {
		t.Fatalf("report = %+v", report)
	}
	st2, err := store.OpenInDir(target, project)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	rec, err := st2.GetIndexIdentity(project)
	if err != nil || rec.Status != indexidentity.StatusStaleSource {
		t.Fatalf("identity after stale import = %+v, %v", rec, err)
	}
}

func TestImportWithoutIdentityNeedsAllowStaleAndRejectsCorruption(t *testing.T) {
	repo := t.TempDir()
	cache := t.TempDir()
	project := seed(t, cache, repo, false)
	st, err := store.OpenInDir(cache, project)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "legacy"+Extension)
	if _, err := Export(context.Background(), st, project, out, "v"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if _, err := Import(context.Background(), out, ImportOptions{RepoPath: repo, CacheDir: t.TempDir(), ProjectName: projectName}); !errors.Is(err, ErrStale) {
		t.Fatalf("legacy artifact without identity: err = %v", err)
	}
	if _, err := Import(context.Background(), out, ImportOptions{RepoPath: repo, CacheDir: t.TempDir(), ProjectName: projectName, AllowStale: true}); err != nil {
		t.Fatalf("legacy artifact with --allow-stale: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-20] ^= 0xff
	corrupt := filepath.Join(t.TempDir(), "corrupt"+Extension)
	if err := os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), corrupt, ImportOptions{RepoPath: repo, CacheDir: t.TempDir(), ProjectName: projectName, AllowStale: true}); err == nil {
		t.Fatal("corrupt payload must be rejected")
	}
	if _, _, err := ReadHeader(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file must error")
	}
}
