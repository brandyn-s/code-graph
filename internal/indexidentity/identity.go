package indexidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = 1

const (
	StatusCaptured    = "captured"
	StatusPending     = "pending"
	StatusError       = "error"
	StatusMissing     = "missing"
	StatusStaleSource = "stale_source"
)

// Envelope identifies the exact checkout state used to build an index.
type Envelope struct {
	SchemaVersion    int    `json:"schema_version"`
	RepositoryID     string `json:"repository_id"`
	CheckoutID       string `json:"checkout_id"`
	SourceRevision   string `json:"source_revision"`
	DirtyFingerprint string `json:"dirty_fingerprint"`
	IndexGeneration  string `json:"index_generation"`
	CapturedAt       string `json:"captured_at"`
}

// Validate rejects persisted envelopes that cannot identify a coherent index.
func (e *Envelope) Validate() error {
	if e == nil {
		return fmt.Errorf("incomplete identity: envelope is nil")
	}
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("incomplete identity: schema_version=%d, want %d", e.SchemaVersion, SchemaVersion)
	}
	required := map[string]string{
		"repository_id":     e.RepositoryID,
		"checkout_id":       e.CheckoutID,
		"source_revision":   e.SourceRevision,
		"dirty_fingerprint": e.DirtyFingerprint,
		"index_generation":  e.IndexGeneration,
		"captured_at":       e.CapturedAt,
	}
	for field, value := range required {
		if value == "" {
			return fmt.Errorf("incomplete identity: %s is empty", field)
		}
	}
	for field, value := range map[string]string{
		"repository_id":    e.RepositoryID,
		"checkout_id":      e.CheckoutID,
		"index_generation": e.IndexGeneration,
	} {
		if !isLowerHexSHA256(value) {
			return fmt.Errorf("invalid identity: %s is not a lowercase SHA-256 digest", field)
		}
	}
	if e.DirtyFingerprint != "clean" && !isLowerHexSHA256(e.DirtyFingerprint) {
		return fmt.Errorf("invalid identity: dirty_fingerprint is neither clean nor a lowercase SHA-256 digest")
	}
	if !isGitSourceRevision(e.SourceRevision) {
		return fmt.Errorf(
			"invalid identity: source_revision is neither unborn nor a lowercase 40- or 64-character Git object ID",
		)
	}
	expectedGeneration := ComputeIndexGeneration(e.RepositoryID, e.SourceRevision, e.DirtyFingerprint)
	if e.IndexGeneration != expectedGeneration {
		return fmt.Errorf(
			"invalid identity: index_generation %s does not match computed generation %s",
			e.IndexGeneration,
			expectedGeneration,
		)
	}
	capturedAt, err := time.Parse(time.RFC3339, e.CapturedAt)
	if err != nil {
		return fmt.Errorf("invalid identity: captured_at is not RFC3339: %w", err)
	}
	_, offset := capturedAt.Zone()
	if offset != 0 {
		return fmt.Errorf("invalid identity: captured_at is not UTC")
	}
	return nil
}

// Record couples an optional captured envelope with its persistence state.
type Record struct {
	Identity *Envelope
	Status   string
	Reason   string
}

// ComputeIndexGeneration derives the cross-engine generation identifier.
func ComputeIndexGeneration(repositoryID, sourceRevision, dirtyFingerprint string) string {
	return sha256Hex(repositoryID + "\x00" + sourceRevision + "\x00" + dirtyFingerprint)
}

// Capture records the identity of a clean Git checkout.
func Capture(root string) (*Envelope, error) {
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve checkout path: %w", err)
	}
	resolvedRoot, err = filepath.EvalSymlinks(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve checkout symlinks: %w", err)
	}
	pathID := "path:" + filepath.ToSlash(resolvedRoot)
	// Distinguish "git could not be RUN" from "this is not a repository".
	// Collapsing both into "not a Git repository" sent operators to inspect a
	// checkout that was perfectly valid: under a sandbox that denies process
	// execution, every capture reported "not a Git repository: <path>" for
	// paths with a .git/ directory and a resolvable HEAD. The index still built
	// at full coverage — only the identity record was lost — so the misleading
	// message was the entire diagnostic surface. Observed 2026-07-27 across 19
	// projects re-indexed inside the Claude Code Bash sandbox.
	insideWorktree, err := gitOutput(resolvedRoot, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if execErr := classifyGitExecFailure(err); execErr != nil {
			return nil, fmt.Errorf("cannot run git at %s: %w", filepath.ToSlash(resolvedRoot), execErr)
		}
		// git ran and exited non-zero — the genuine not-a-repository signal.
		return nil, fmt.Errorf("not a Git repository: %s", filepath.ToSlash(resolvedRoot))
	}
	if strings.TrimSpace(string(insideWorktree)) != "true" {
		return nil, fmt.Errorf("not a Git repository: %s", filepath.ToSlash(resolvedRoot))
	}

	origin := ""
	if rawOrigin, originErr := gitOutput(resolvedRoot, "remote", "get-url", "origin"); originErr == nil {
		origin = normalizeOrigin(string(rawOrigin))
	}
	repositorySeed := pathID
	if origin != "" {
		repositorySeed = "remote:" + origin
	}

	revisionBytes, revisionErr := gitOutput(resolvedRoot, "rev-parse", "HEAD")
	revision := "unborn"
	if revisionErr == nil {
		revision = strings.TrimSpace(string(revisionBytes))
	}

	status, err := gitOutput(resolvedRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("capture Git status: %w", err)
	}
	diffBase := revision
	if revision == "unborn" {
		emptyTree, emptyTreeErr := gitOutput(
			resolvedRoot,
			"hash-object",
			"-t",
			"tree",
			"--stdin",
		)
		if emptyTreeErr != nil {
			return nil, fmt.Errorf("capture native Git empty tree: %w", emptyTreeErr)
		}
		diffBase = strings.TrimSpace(string(emptyTree))
		if !isGitObjectID(diffBase) {
			return nil, fmt.Errorf("capture native Git empty tree: invalid object ID %q", diffBase)
		}
	}
	dirtyFingerprint, err := captureDirtyFingerprint(resolvedRoot, status, diffBase)
	if err != nil {
		return nil, err
	}

	repositoryID := sha256Hex(repositorySeed)
	checkoutID := sha256Hex(pathID)
	indexGeneration := ComputeIndexGeneration(repositoryID, revision, dirtyFingerprint)

	return &Envelope{
		SchemaVersion:    SchemaVersion,
		RepositoryID:     repositoryID,
		CheckoutID:       checkoutID,
		SourceRevision:   revision,
		DirtyFingerprint: dirtyFingerprint,
		IndexGeneration:  indexGeneration,
		CapturedAt:       time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func normalizeOrigin(raw string) string {
	origin := strings.TrimSpace(raw)
	if origin == "" {
		return ""
	}
	if !strings.Contains(origin, "://") {
		if colon := strings.IndexByte(origin, ':'); colon > 0 && colon < len(origin)-1 {
			hostPart := origin[:colon]
			isWindowsDrive := len(hostPart) == 1 &&
				((hostPart[0] >= 'A' && hostPart[0] <= 'Z') || (hostPart[0] >= 'a' && hostPart[0] <= 'z'))
			if !isWindowsDrive && !strings.ContainsAny(hostPart, `/\`) {
				if at := strings.LastIndexByte(hostPart, '@'); at > 0 && at < len(hostPart)-1 {
					hostPart = hostPart[at+1:]
				}
				origin = "https://" + strings.ToLower(hostPart) + "/" + strings.TrimLeft(origin[colon+1:], "/")
			}
		}
	}
	if parsed, err := url.Parse(origin); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		parsed.RawFragment = ""
		origin = parsed.String()
	}
	origin = strings.TrimRight(origin, "/")
	origin = strings.TrimSuffix(origin, ".git")
	return strings.TrimRight(origin, "/")
}

func captureDirtyFingerprint(root string, status []byte, diffBase string) (string, error) {
	submodulePayloads, err := captureSubmodulePayloads(root)
	if err != nil {
		return "", err
	}
	if len(status) == 0 && len(submodulePayloads) == 0 {
		return "clean", nil
	}

	worktreeDiff, err := gitOutput(root, "diff", "--binary", "--no-ext-diff", diffBase, "--")
	if err != nil {
		return "", fmt.Errorf("capture worktree diff: %w", err)
	}
	cachedDiff, err := gitOutput(root, "diff", "--binary", "--no-ext-diff", "--cached", diffBase, "--")
	if err != nil {
		return "", fmt.Errorf("capture cached diff: %w", err)
	}
	untrackedPaths := untrackedPathsFromStatus(status)
	untrackedRoot := root
	if len(untrackedPaths) != 0 {
		topLevel, err := gitOutput(root, "rev-parse", "--show-toplevel")
		if err != nil {
			return "", fmt.Errorf("resolve Git repository root: %w", err)
		}
		untrackedRoot = strings.TrimSuffix(string(topLevel), "\n")
		if untrackedRoot == "" {
			return "", fmt.Errorf("resolve Git repository root: empty path")
		}
		untrackedRoot, err = filepath.EvalSymlinks(untrackedRoot)
		if err != nil {
			return "", fmt.Errorf("resolve Git repository root symlinks: %w", err)
		}
	}

	var framed bytes.Buffer
	writeFingerprintFrame(&framed, "STATUS", status)
	writeFingerprintFrame(&framed, "WORKTREE_DIFF", worktreeDiff)
	writeFingerprintFrame(&framed, "CACHED_DIFF", cachedDiff)
	for _, relPath := range untrackedPaths {
		payload, err := untrackedPayload(untrackedRoot, relPath)
		if err != nil {
			return "", err
		}
		writeFingerprintFrame(&framed, "UNTRACKED", payload)
	}
	for _, payload := range submodulePayloads {
		writeFingerprintFrame(&framed, "SUBMODULE", payload)
	}

	sum := sha256.Sum256(framed.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

type gitlink struct {
	relativePath     []byte
	expectedObjectID []byte
}

func captureGitlinks(root string) ([]gitlink, error) {
	entries, err := gitOutput(
		root,
		"ls-files",
		"--stage",
		"--full-name",
		"-z",
	)
	if err != nil {
		return nil, fmt.Errorf("capture Git index entries: %w", err)
	}

	var gitlinks []gitlink
	for _, entry := range bytes.Split(entries, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		separator := bytes.IndexByte(entry, '\t')
		if separator < 0 {
			return nil, fmt.Errorf("capture Git index entries: malformed entry")
		}
		fields := bytes.Fields(entry[:separator])
		if len(fields) != 3 {
			return nil, fmt.Errorf("capture Git index entries: malformed metadata")
		}
		mode, objectID, stage := fields[0], fields[1], fields[2]
		if !bytes.Equal(mode, []byte("160000")) || !bytes.Equal(stage, []byte("0")) {
			continue
		}
		if !isGitObjectID(string(objectID)) {
			return nil, fmt.Errorf("capture Git submodule: invalid object ID %q", objectID)
		}
		gitlinks = append(gitlinks, gitlink{
			relativePath:     bytes.Clone(entry[separator+1:]),
			expectedObjectID: bytes.Clone(objectID),
		})
	}
	sort.Slice(gitlinks, func(i, j int) bool {
		return bytes.Compare(gitlinks[i].relativePath, gitlinks[j].relativePath) < 0
	})
	return gitlinks, nil
}

func submodulePayload(
	relativePath, expectedObjectID, currentObjectID []byte,
	nestedFingerprint string,
) []byte {
	payload := make(
		[]byte,
		0,
		len(relativePath)+len(expectedObjectID)+len(currentObjectID)+
			len(nestedFingerprint)+3,
	)
	payload = append(payload, relativePath...)
	payload = append(payload, 0)
	payload = append(payload, expectedObjectID...)
	payload = append(payload, 0)
	payload = append(payload, currentObjectID...)
	payload = append(payload, 0)
	payload = append(payload, nestedFingerprint...)
	return payload
}

func captureSubmodulePayloads(root string) ([][]byte, error) {
	topLevelBytes, err := gitOutput(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("resolve Git repository root: %w", err)
	}
	topLevel := strings.TrimSuffix(string(topLevelBytes), "\n")
	if topLevel == "" {
		return nil, fmt.Errorf("resolve Git repository root: empty path")
	}
	topLevel, err = filepath.EvalSymlinks(topLevel)
	if err != nil {
		return nil, fmt.Errorf("resolve Git repository root symlinks: %w", err)
	}
	gitlinks, err := captureGitlinks(root)
	if err != nil {
		return nil, err
	}

	var payloads [][]byte
	for _, link := range gitlinks {
		submoduleRoot := filepath.Join(
			topLevel,
			filepath.FromSlash(string(link.relativePath)),
		)
		if _, err := os.Lstat(filepath.Join(submoduleRoot, ".git")); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf(
				"inspect Git submodule metadata %q: %w",
				string(link.relativePath),
				err,
			)
		}

		currentObjectID, err := gitOutput(
			submoduleRoot,
			"rev-parse",
			"--verify",
			"HEAD",
		)
		if err != nil {
			return nil, fmt.Errorf(
				"capture Git submodule current object %q: %w",
				string(link.relativePath),
				err,
			)
		}
		currentObjectID = bytes.TrimSpace(currentObjectID)
		if !isGitObjectID(string(currentObjectID)) {
			return nil, fmt.Errorf(
				"capture Git submodule current object %q: invalid object ID %q",
				string(link.relativePath),
				currentObjectID,
			)
		}
		status, err := gitOutput(
			submoduleRoot,
			"status",
			"--porcelain=v1",
			"-z",
			"--untracked-files=all",
		)
		if err != nil {
			return nil, fmt.Errorf(
				"capture Git submodule status %q: %w",
				string(link.relativePath),
				err,
			)
		}
		nestedFingerprint, err := captureDirtyFingerprint(
			submoduleRoot,
			status,
			string(currentObjectID),
		)
		if err != nil {
			return nil, fmt.Errorf(
				"capture Git submodule fingerprint %q: %w",
				string(link.relativePath),
				err,
			)
		}
		if bytes.Equal(currentObjectID, link.expectedObjectID) &&
			nestedFingerprint == "clean" {
			continue
		}
		payloads = append(
			payloads,
			submodulePayload(
				link.relativePath,
				link.expectedObjectID,
				currentObjectID,
				nestedFingerprint,
			),
		)
	}
	return payloads, nil
}

func untrackedPathsFromStatus(status []byte) [][]byte {
	var paths [][]byte
	for _, record := range bytes.Split(status, []byte{0}) {
		if len(record) > 3 && bytes.HasPrefix(record, []byte("?? ")) {
			paths = append(paths, bytes.Clone(record[3:]))
		}
	}
	sortRawPaths(paths)
	return paths
}

func sortRawPaths(paths [][]byte) {
	sort.Slice(paths, func(i, j int) bool {
		return bytes.Compare(paths[i], paths[j]) < 0
	})
}

func untrackedPayload(root string, relPath []byte) ([]byte, error) {
	fullPath := filepath.Join(root, filepath.FromSlash(string(relPath)))
	kind := "file"
	var contentDigest [sha256.Size]byte

	info, err := os.Lstat(fullPath)
	switch {
	case os.IsNotExist(err):
		kind = "missing"
	case err != nil:
		return nil, fmt.Errorf("inspect untracked path %q: %w", string(relPath), err)
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
		target, err := os.Readlink(fullPath)
		if err != nil {
			return nil, fmt.Errorf("read untracked symlink %q: %w", string(relPath), err)
		}
		contentDigest = sha256.Sum256([]byte(target))
	default:
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("read untracked file %q: %w", string(relPath), err)
		}
		contentDigest = sha256.Sum256(content)
	}

	payload := make([]byte, 0, len(relPath)+1+len(kind)+1+sha256.Size)
	payload = append(payload, relPath...)
	payload = append(payload, 0)
	payload = append(payload, kind...)
	payload = append(payload, 0)
	payload = append(payload, contentDigest[:]...)
	return payload, nil
}

func writeFingerprintFrame(buf *bytes.Buffer, label string, payload []byte) {
	buf.WriteString(label)
	buf.WriteByte(0)
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(payload)))
	buf.Write(length[:])
	buf.Write(payload)
}

func gitOutput(root string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"-C", root}, args...)
	cmd := exec.CommandContext(context.Background(), "git", cmdArgs...)
	cmd.Env = withCEnvironment(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// classifyGitExecFailure reports whether err means git could not be EXECUTED
// (binary absent from PATH, sandbox/seccomp denial, permission error) as
// opposed to git having run and exited non-zero.
//
// The distinction is the whole point: an *exec.ExitError means git ran and
// answered, so a non-zero exit on `rev-parse --is-inside-work-tree` genuinely
// indicates a non-repository. Every other error class means we never got an
// answer, and reporting that as "not a Git repository" points the operator at
// the wrong thing entirely.
//
// Returns nil when err is an ordinary non-zero exit (caller should treat it as
// a real repository-shape answer), and a descriptive error otherwise.
func classifyGitExecFailure(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// git ran and exited non-zero: a real answer, not an exec failure.
		return nil
	}
	var pathErr *exec.Error
	if errors.As(err, &pathErr) {
		// Includes exec.ErrNotFound (git absent from PATH) and permission
		// errors from a sandbox that blocks process execution.
		return fmt.Errorf("git could not be executed (%w) — if this is running "+
			"under a sandbox that blocks subprocesses, identity capture needs "+
			"an unsandboxed environment; the index itself is unaffected", pathErr.Err)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("git not found on PATH: %w", err)
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("permission denied executing git: %w", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("git invocation did not complete: %w", err)
	}
	// Unknown class: surface it rather than mislabeling it a non-repository.
	return err
}

func withCEnvironment(env []string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, "LC_ALL=") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "LC_ALL=C")
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func isLowerHexSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isGitSourceRevision(value string) bool {
	if value == "unborn" {
		return true
	}
	return isGitObjectID(value)
}

func isGitObjectID(value string) bool {
	if (len(value) != 40 && len(value) != 64) || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
