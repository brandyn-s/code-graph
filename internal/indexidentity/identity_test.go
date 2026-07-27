package indexidentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type identityVectors struct {
	OriginNormalization []struct {
		Input      string `json:"input"`
		Normalized string `json:"normalized"`
	} `json:"origin_normalization"`
	IndexGeneration []struct {
		RepositoryID     string `json:"repository_id"`
		SourceRevision   string `json:"source_revision"`
		DirtyFingerprint string `json:"dirty_fingerprint"`
		IndexGeneration  string `json:"index_generation"`
	} `json:"index_generation"`
	SubmoduleFraming []struct {
		RelativePathHex   string `json:"relative_path_hex"`
		ExpectedObjectID  string `json:"expected_object_id"`
		CurrentObjectID   string `json:"current_object_id"`
		NestedFingerprint string `json:"nested_dirty_fingerprint"`
		PayloadHex        string `json:"payload_hex"`
		DirtyFingerprint  string `json:"dirty_fingerprint"`
	} `json:"submodule_framing"`
}

func readIdentityVectors(t *testing.T) identityVectors {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "index_identity_vectors.json"))
	if err != nil {
		t.Fatalf("read identity vectors: %v", err)
	}
	var vectors identityVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("parse identity vectors: %v", err)
	}
	return vectors
}

func runGit(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = withCEnvironment(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func newCommittedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatalf("write tracked.txt: %v", err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-m", "base")
	return root
}

func writeFrame(buf *bytes.Buffer, label string, payload []byte) {
	buf.WriteString(label)
	buf.WriteByte(0)
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(payload)))
	buf.Write(length[:])
	buf.Write(payload)
}

func expectedDirtyFingerprint(t *testing.T, root, diffBase string, untrackedPaths []string) string {
	t.Helper()
	var framed bytes.Buffer
	writeFrame(&framed, "STATUS", runGit(t, root,
		"status", "--porcelain=v1", "-z", "--untracked-files=all"))
	writeFrame(&framed, "WORKTREE_DIFF", runGit(t, root,
		"diff", "--binary", "--no-ext-diff", diffBase, "--"))
	writeFrame(&framed, "CACHED_DIFF", runGit(t, root,
		"diff", "--binary", "--no-ext-diff", "--cached", diffBase, "--"))

	sort.Strings(untrackedPaths)
	for _, relPath := range untrackedPaths {
		fullPath := filepath.Join(root, filepath.FromSlash(relPath))
		info, err := os.Lstat(fullPath)
		if err != nil {
			t.Fatalf("lstat %q: %v", relPath, err)
		}
		kind := "file"
		var content []byte
		if info.Mode()&os.ModeSymlink != 0 {
			kind = "symlink"
			target, err := os.Readlink(fullPath)
			if err != nil {
				t.Fatalf("readlink %q: %v", relPath, err)
			}
			content = []byte(target)
		} else {
			content, err = os.ReadFile(fullPath)
			if err != nil {
				t.Fatalf("read %q: %v", relPath, err)
			}
		}
		contentSum := sha256.Sum256(content)
		payload := append([]byte(relPath+"\x00"+kind+"\x00"), contentSum[:]...)
		writeFrame(&framed, "UNTRACKED", payload)
	}

	sum := sha256.Sum256(framed.Bytes())
	return hex.EncodeToString(sum[:])
}

func TestCaptureDirtyCheckoutUsesFramedGitState(t *testing.T) {
	root := newCommittedRepo(t)
	runGit(t, root, "remote", "add", "origin", "git@GitHub.com:Example/Repo.git")

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("worktree\n"), 0o600); err != nil {
		t.Fatalf("modify tracked.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatalf("write staged.txt: %v", err)
	}
	runGit(t, root, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatalf("write alpha.txt: %v", err)
	}
	if err := os.Symlink("alpha.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink link.txt: %v", err)
	}

	wantFingerprint := expectedDirtyFingerprint(t, root, "HEAD", []string{"alpha.txt", "link.txt"})
	identity, err := Capture(root)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if identity.DirtyFingerprint != wantFingerprint {
		t.Errorf("dirty fingerprint = %q, want %q", identity.DirtyFingerprint, wantFingerprint)
	}
	repositoryID := sha256Hex("remote:https://github.com/Example/Repo")
	revision := string(bytes.TrimSpace(runGit(t, root, "rev-parse", "HEAD")))
	wantGeneration := sha256Hex(repositoryID + "\x00" + revision + "\x00" + wantFingerprint)
	if identity.IndexGeneration != wantGeneration {
		t.Errorf("index generation = %q, want %q", identity.IndexGeneration, wantGeneration)
	}

	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatalf("change alpha.txt: %v", err)
	}
	changed, err := Capture(root)
	if err != nil {
		t.Fatalf("Capture after untracked change: %v", err)
	}
	if changed.DirtyFingerprint == identity.DirtyFingerprint {
		t.Error("dirty fingerprint did not change with untracked file content")
	}
}

func TestCaptureSubdirectoryHashesUntrackedContentFromRepositoryRoot(t *testing.T) {
	root := newCommittedRepo(t)
	indexedRoot := filepath.Join(root, "subdir")
	if err := os.MkdirAll(indexedRoot, 0o700); err != nil {
		t.Fatalf("mkdir indexed subdirectory: %v", err)
	}
	untrackedPath := filepath.Join(indexedRoot, "new.go")
	if err := os.WriteFile(untrackedPath, []byte("package subdir\n"), 0o600); err != nil {
		t.Fatalf("write untracked source: %v", err)
	}

	first, err := Capture(indexedRoot)
	if err != nil {
		t.Fatalf("Capture(first): %v", err)
	}
	if err := os.WriteFile(
		untrackedPath,
		[]byte("package subdir\n\nfunc Changed() {}\n"),
		0o600,
	); err != nil {
		t.Fatalf("change untracked source: %v", err)
	}
	changed, err := Capture(indexedRoot)
	if err != nil {
		t.Fatalf("Capture(changed): %v", err)
	}

	if changed.DirtyFingerprint == first.DirtyFingerprint {
		t.Fatalf(
			"subdirectory dirty fingerprint stayed %q after untracked source content changed",
			first.DirtyFingerprint,
		)
	}
	if changed.IndexGeneration == first.IndexGeneration {
		t.Fatalf(
			"subdirectory index generation stayed %q after untracked source content changed",
			first.IndexGeneration,
		)
	}
}

func TestCaptureDirtySubmoduleTrackedContentChangesIdentity(t *testing.T) {
	submoduleOrigin := newCommittedRepo(t)
	parent := newCommittedRepo(t)
	runGit(
		t,
		parent,
		"-c",
		"protocol.file.allow=always",
		"submodule",
		"add",
		submoduleOrigin,
		"vendor/dependency",
	)
	runGit(t, parent, "commit", "-m", "add dependency")
	dependencySource := filepath.Join(
		parent,
		"vendor",
		"dependency",
		"tracked.txt",
	)

	if err := os.WriteFile(
		dependencySource,
		[]byte("first dirty contents\n"),
		0o600,
	); err != nil {
		t.Fatalf("write first dirty submodule contents: %v", err)
	}
	firstStatus := runGit(
		t,
		parent,
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
	)
	firstDiff := runGit(
		t,
		parent,
		"diff",
		"--binary",
		"--no-ext-diff",
		"HEAD",
		"--",
	)
	first, err := Capture(parent)
	if err != nil {
		t.Fatalf("Capture(first): %v", err)
	}

	if err := os.WriteFile(
		dependencySource,
		[]byte("second dirty contents\n"),
		0o600,
	); err != nil {
		t.Fatalf("write second dirty submodule contents: %v", err)
	}
	secondStatus := runGit(
		t,
		parent,
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
	)
	secondDiff := runGit(
		t,
		parent,
		"diff",
		"--binary",
		"--no-ext-diff",
		"HEAD",
		"--",
	)
	changed, err := Capture(parent)
	if err != nil {
		t.Fatalf("Capture(changed): %v", err)
	}

	if first.SourceRevision != changed.SourceRevision {
		t.Fatalf(
			"source revision changed from %q to %q",
			first.SourceRevision,
			changed.SourceRevision,
		)
	}
	if !bytes.Equal(firstStatus, secondStatus) {
		t.Fatalf("parent status changed: first=%q second=%q", firstStatus, secondStatus)
	}
	if !bytes.Equal(firstDiff, secondDiff) {
		t.Fatalf("parent diff changed: first=%q second=%q", firstDiff, secondDiff)
	}
	if changed.DirtyFingerprint == first.DirtyFingerprint {
		t.Fatalf(
			"dirty fingerprint stayed %q after submodule content changed",
			first.DirtyFingerprint,
		)
	}
	if changed.IndexGeneration == first.IndexGeneration {
		t.Fatalf(
			"index generation stayed %q after submodule content changed",
			first.IndexGeneration,
		)
	}
}

func TestCaptureNestedDirtySubmoduleTrackedContentChangesIdentity(t *testing.T) {
	leafOrigin := newCommittedRepo(t)
	middleOrigin := newCommittedRepo(t)
	runGit(
		t,
		middleOrigin,
		"-c",
		"protocol.file.allow=always",
		"submodule",
		"add",
		leafOrigin,
		"nested/leaf",
	)
	runGit(t, middleOrigin, "commit", "-m", "add nested dependency")
	parent := newCommittedRepo(t)
	runGit(
		t,
		parent,
		"-c",
		"protocol.file.allow=always",
		"submodule",
		"add",
		middleOrigin,
		"vendor/middle",
	)
	runGit(
		t,
		parent,
		"-c",
		"protocol.file.allow=always",
		"submodule",
		"update",
		"--init",
		"--recursive",
	)
	runGit(t, parent, "commit", "-m", "add dependency")
	leafSource := filepath.Join(
		parent,
		"vendor",
		"middle",
		"nested",
		"leaf",
		"tracked.txt",
	)

	if err := os.WriteFile(
		leafSource,
		[]byte("first nested dirty contents\n"),
		0o600,
	); err != nil {
		t.Fatalf("write first nested dirty contents: %v", err)
	}
	first, err := Capture(parent)
	if err != nil {
		t.Fatalf("Capture(first): %v", err)
	}
	if err := os.WriteFile(
		leafSource,
		[]byte("second nested dirty contents\n"),
		0o600,
	); err != nil {
		t.Fatalf("write second nested dirty contents: %v", err)
	}
	changed, err := Capture(parent)
	if err != nil {
		t.Fatalf("Capture(changed): %v", err)
	}

	if first.SourceRevision != changed.SourceRevision {
		t.Fatalf(
			"source revision changed from %q to %q",
			first.SourceRevision,
			changed.SourceRevision,
		)
	}
	if changed.DirtyFingerprint == first.DirtyFingerprint {
		t.Fatalf(
			"dirty fingerprint stayed %q after nested submodule content changed",
			first.DirtyFingerprint,
		)
	}
	if changed.IndexGeneration == first.IndexGeneration {
		t.Fatalf(
			"index generation stayed %q after nested submodule content changed",
			first.IndexGeneration,
		)
	}
}

func TestCaptureFailsClosedWhenInitializedSubmoduleCannotBeInspected(t *testing.T) {
	submoduleOrigin := newCommittedRepo(t)
	parent := newCommittedRepo(t)
	runGit(
		t,
		parent,
		"-c",
		"protocol.file.allow=always",
		"submodule",
		"add",
		submoduleOrigin,
		"vendor/dependency",
	)
	runGit(t, parent, "commit", "-m", "add dependency")
	submoduleRoot, err := filepath.EvalSymlinks(
		filepath.Join(parent, "vendor", "dependency"),
	)
	if err != nil {
		t.Fatalf("resolve submodule root: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "git")
	const wrapper = `#!/bin/sh
if [ "$1" = "-C" ] && [ "$2" = "$SUBMODULE_FAILURE_ROOT" ] && [ "$3" = "rev-parse" ] && [ "$4" = "--verify" ] && [ "$5" = "HEAD" ]; then
  exit 97
fi
exec "$SUBMODULE_FAILURE_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("SUBMODULE_FAILURE_ROOT", submoduleRoot)
	t.Setenv("SUBMODULE_FAILURE_REAL_GIT", realGit)
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err = Capture(parent)

	if err == nil {
		t.Fatal("Capture succeeded with unreadable initialized submodule metadata")
	}
	if !strings.Contains(err.Error(), "capture Git submodule current object") {
		t.Fatalf("Capture error = %q, want submodule inspection context", err)
	}
}

func TestSubmoduleFramingVectors(t *testing.T) {
	vectors := readIdentityVectors(t)
	if len(vectors.SubmoduleFraming) == 0 {
		t.Fatal("submodule framing vectors are empty")
	}
	for _, vector := range vectors.SubmoduleFraming {
		relativePath, err := hex.DecodeString(vector.RelativePathHex)
		if err != nil {
			t.Fatalf("decode relative path: %v", err)
		}
		payload := submodulePayload(
			relativePath,
			[]byte(vector.ExpectedObjectID),
			[]byte(vector.CurrentObjectID),
			vector.NestedFingerprint,
		)
		if got := hex.EncodeToString(payload); got != vector.PayloadHex {
			t.Errorf("submodule payload = %q, want %q", got, vector.PayloadHex)
		}

		var framed bytes.Buffer
		writeFingerprintFrame(&framed, "STATUS", nil)
		writeFingerprintFrame(&framed, "WORKTREE_DIFF", nil)
		writeFingerprintFrame(&framed, "CACHED_DIFF", nil)
		writeFingerprintFrame(&framed, "SUBMODULE", payload)
		sum := sha256.Sum256(framed.Bytes())
		if got := hex.EncodeToString(sum[:]); got != vector.DirtyFingerprint {
			t.Errorf(
				"submodule dirty fingerprint = %q, want %q",
				got,
				vector.DirtyFingerprint,
			)
		}
	}
}

func TestCaptureDirtyCheckoutDiffsAgainstCapturedRevision(t *testing.T) {
	root := newCommittedRepo(t)
	firstRevision := strings.TrimSpace(string(runGit(t, root, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatalf("write second revision: %v", err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-m", "second")
	secondRevision := strings.TrimSpace(string(runGit(t, root, "rev-parse", "HEAD")))
	runGit(t, root, "checkout", "--detach", firstRevision)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("write dirty worktree: %v", err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "git")
	const wrapper = `#!/bin/sh
if [ "$1" = "-C" ] && [ "$2" = "$CAPTURE_TEST_ROOT" ] && [ "$3" = "status" ] && [ ! -e "$CAPTURE_TEST_MARKER" ]; then
  : > "$CAPTURE_TEST_MARKER"
  "$CAPTURE_TEST_REAL_GIT" -C "$CAPTURE_TEST_ROOT" update-ref HEAD "$CAPTURE_TEST_NEXT_REVISION" || exit $?
fi
exec "$CAPTURE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve test root: %v", err)
	}
	t.Setenv("CAPTURE_TEST_ROOT", resolvedRoot)
	t.Setenv("CAPTURE_TEST_MARKER", filepath.Join(wrapperDir, "head-moved"))
	t.Setenv("CAPTURE_TEST_REAL_GIT", realGit)
	t.Setenv("CAPTURE_TEST_NEXT_REVISION", secondRevision)
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	identity, err := Capture(root)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if identity.SourceRevision != firstRevision {
		t.Fatalf("source revision = %q, want initially captured %q", identity.SourceRevision, firstRevision)
	}
	if current := strings.TrimSpace(string(runGit(t, root, "rev-parse", "HEAD"))); current != secondRevision {
		t.Fatalf("test wrapper left HEAD at %q, want %q", current, secondRevision)
	}

	wantFingerprint := expectedDirtyFingerprint(t, root, firstRevision, nil)
	if identity.DirtyFingerprint != wantFingerprint {
		t.Errorf(
			"dirty fingerprint = %q, want diff framed against captured revision %q (%q)",
			identity.DirtyFingerprint,
			firstRevision,
			wantFingerprint,
		)
	}
}

func TestCaptureDirtyFingerprintPreservesMissingUntrackedPathFromStatus(t *testing.T) {
	root := newCommittedRepo(t)
	const relPath = "vanished.txt"
	if err := os.WriteFile(filepath.Join(root, relPath), []byte("temporary\n"), 0o600); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	status := runGit(t, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if !bytes.Contains(status, []byte("?? "+relPath+"\x00")) {
		t.Fatalf("status %q does not contain untracked path %q", status, relPath)
	}
	if err := os.Remove(filepath.Join(root, relPath)); err != nil {
		t.Fatalf("remove untracked file: %v", err)
	}
	revision := strings.TrimSpace(string(runGit(t, root, "rev-parse", "HEAD")))

	got, err := captureDirtyFingerprint(root, status, revision)
	if err != nil {
		t.Fatalf("captureDirtyFingerprint: %v", err)
	}

	var framed bytes.Buffer
	writeFrame(&framed, "STATUS", status)
	writeFrame(&framed, "WORKTREE_DIFF", nil)
	writeFrame(&framed, "CACHED_DIFF", nil)
	missingPayload := append([]byte(relPath+"\x00missing\x00"), make([]byte, sha256.Size)...)
	writeFrame(&framed, "UNTRACKED", missingPayload)
	wantSum := sha256.Sum256(framed.Bytes())
	want := hex.EncodeToString(wantSum[:])
	if got != want {
		t.Errorf("dirty fingerprint = %q, want missing untracked path preserved from status (%q)", got, want)
	}
}

func TestUntrackedPathsFromStatusPreservesRawByteOrdering(t *testing.T) {
	status := append([]byte(" M tracked.txt\x00?? zeta.txt\x00?? "), 0xff)
	status = append(status, []byte(".txt\x00?? alpha.txt\x00")...)

	got := untrackedPathsFromStatus(status)
	want := [][]byte{
		[]byte("alpha.txt"),
		[]byte("zeta.txt"),
		{0xff, '.', 't', 'x', 't'},
	}
	if len(got) != len(want) {
		t.Fatalf("untracked path count = %d, want %d (%q)", len(got), len(want), got)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("untracked path %d = %q, want raw bytes %q", i, got[i], want[i])
		}
	}
}

func TestNormalizeOriginVectors(t *testing.T) {
	vectors := readIdentityVectors(t)
	if len(vectors.OriginNormalization) == 0 {
		t.Fatal("origin normalization vectors are empty")
	}
	for _, vector := range vectors.OriginNormalization {
		t.Run(vector.Input, func(t *testing.T) {
			if got := normalizeOrigin(vector.Input); got != vector.Normalized {
				t.Errorf("normalizeOrigin(%q) = %q, want %q", vector.Input, got, vector.Normalized)
			}
		})
	}
}

func TestComputeIndexGenerationVectors(t *testing.T) {
	vectors := readIdentityVectors(t)
	if len(vectors.IndexGeneration) == 0 {
		t.Fatal("index generation vectors are empty")
	}
	for _, vector := range vectors.IndexGeneration {
		got := ComputeIndexGeneration(
			vector.RepositoryID,
			vector.SourceRevision,
			vector.DirtyFingerprint,
		)
		if got != vector.IndexGeneration {
			t.Errorf(
				"ComputeIndexGeneration(%q, %q, %q) = %q, want %q",
				vector.RepositoryID,
				vector.SourceRevision,
				vector.DirtyFingerprint,
				got,
				vector.IndexGeneration,
			)
		}
	}
}

func TestEnvelopeValidateEnforcesGitSourceRevisionGrammar(t *testing.T) {
	newEnvelope := func(revision string) *Envelope {
		repositoryID := strings.Repeat("a", 64)
		dirtyFingerprint := "clean"
		return &Envelope{
			SchemaVersion:    SchemaVersion,
			RepositoryID:     repositoryID,
			CheckoutID:       strings.Repeat("b", 64),
			SourceRevision:   revision,
			DirtyFingerprint: dirtyFingerprint,
			IndexGeneration: ComputeIndexGeneration(
				repositoryID,
				revision,
				dirtyFingerprint,
			),
			CapturedAt: "2026-07-26T12:00:00Z",
		}
	}

	for _, revision := range []string{
		"HEAD",
		strings.Repeat("c", 39),
		strings.Repeat("c", 41),
		strings.Repeat("C", 40),
		strings.Repeat("d", 63),
		strings.Repeat("d", 65),
	} {
		t.Run(revision, func(t *testing.T) {
			err := newEnvelope(revision).Validate()
			if err == nil {
				t.Fatalf("Validate(source_revision=%q) succeeded, want error", revision)
			}
			if !strings.Contains(err.Error(), "source_revision") {
				t.Fatalf("Validate(source_revision=%q) error = %q, want source_revision", revision, err)
			}
		})
	}

	for _, revision := range []string{
		"unborn",
		strings.Repeat("c", 40),
		strings.Repeat("d", 64),
	} {
		t.Run("valid_"+revision, func(t *testing.T) {
			if err := newEnvelope(revision).Validate(); err != nil {
				t.Fatalf("Validate(source_revision=%q): %v", revision, err)
			}
		})
	}
}

func TestCaptureInitializedUnbornRepositoryUsesEmptyTreeDiffBase(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatalf("write staged.txt: %v", err)
	}
	runGit(t, root, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("worktree\n"), 0o600); err != nil {
		t.Fatalf("modify staged.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatalf("write untracked.txt: %v", err)
	}

	const emptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	wantFingerprint := expectedDirtyFingerprint(t, root, emptyTreeHash, []string{"untracked.txt"})
	identity, err := Capture(root)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if identity.SourceRevision != "unborn" {
		t.Errorf("source revision = %q, want unborn", identity.SourceRevision)
	}
	if identity.DirtyFingerprint != wantFingerprint {
		t.Errorf("dirty fingerprint = %q, want %q", identity.DirtyFingerprint, wantFingerprint)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root): %v", err)
	}
	repositoryID := sha256Hex("path:" + filepath.ToSlash(resolvedRoot))
	wantGeneration := sha256Hex(repositoryID + "\x00unborn\x00" + wantFingerprint)
	if identity.IndexGeneration != wantGeneration {
		t.Errorf("index generation = %q, want %q", identity.IndexGeneration, wantGeneration)
	}
}

func TestCaptureInitializedUnbornSHA256RepositoryUsesNativeEmptyTreeDiffBase(t *testing.T) {
	root := t.TempDir()
	initCmd := exec.Command("git", "init", "--object-format=sha256")
	initCmd.Dir = root
	initCmd.Env = withCEnvironment(os.Environ())
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("Git SHA-256 repositories are unavailable: %v (%s)", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatalf("write staged.txt: %v", err)
	}
	runGit(t, root, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("worktree\n"), 0o600); err != nil {
		t.Fatalf("modify staged.txt: %v", err)
	}

	emptyTreeHash := strings.TrimSpace(string(runGit(
		t,
		root,
		"hash-object",
		"-t",
		"tree",
		"--stdin",
	)))
	if len(emptyTreeHash) != 64 {
		t.Fatalf("SHA-256 empty tree object = %q, want 64 hex characters", emptyTreeHash)
	}
	wantFingerprint := expectedDirtyFingerprint(t, root, emptyTreeHash, nil)

	identity, err := Capture(root)
	if err != nil {
		t.Fatalf("Capture SHA-256 unborn repository: %v", err)
	}
	if identity.SourceRevision != "unborn" {
		t.Errorf("source revision = %q, want unborn", identity.SourceRevision)
	}
	if identity.DirtyFingerprint != wantFingerprint {
		t.Errorf("dirty fingerprint = %q, want %q", identity.DirtyFingerprint, wantFingerprint)
	}
	if err := identity.Validate(); err != nil {
		t.Errorf("captured SHA-256 identity is invalid: %v", err)
	}
}

func TestCaptureRejectsNonGitDirectoryBeforeUnbornFallback(t *testing.T) {
	_, err := Capture(t.TempDir())
	if err == nil {
		t.Fatal("Capture(non-Git directory) succeeded, want explicit error")
	}
	if !strings.Contains(err.Error(), "not a Git repository") {
		t.Fatalf("Capture(non-Git directory) error = %q, want explicit not-a-Git error", err)
	}
}
