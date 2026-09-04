// Package artifact exports an indexed project's graph database as a
// self-describing, zstd-compressed file that teammates can import instead of
// re-indexing, and imports such files with an index-identity check.
//
// File layout: magic, 4-byte big-endian header length, JSON Header, then a
// single zstd frame containing the SQLite database image (VACUUM INTO copy).
// The header carries the index identity captured when the project was
// indexed (repository, revision, dirty fingerprint), so an import can tell
// whether the artifact matches the local checkout.
package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/brandyn-s/code-graph/internal/indexidentity"
	"github.com/brandyn-s/code-graph/internal/store"
)

// Format is the artifact container version. Bump when the layout changes.
const Format = 1

// Extension is the conventional file suffix.
const Extension = ".cgraph.zst"

var magic = []byte("CGRAPH-ARTIFACT\x00")

// Header describes the artifact and the index inside it.
type Header struct {
	Format           int                     `json:"format"`
	CodeGraphVersion string                  `json:"code_graph_version"`
	SchemaVersion    int                     `json:"schema_version"`
	Project          string                  `json:"project"`
	RootPath         string                  `json:"root_path"`
	IndexedAt        string                  `json:"indexed_at"`
	Identity         *indexidentity.Envelope `json:"identity,omitempty"`
	IdentityStatus   string                  `json:"identity_status"`
	IdentityReason   string                  `json:"identity_reason,omitempty"`
	NodeCount        int                     `json:"node_count"`
	EdgeCount        int                     `json:"edge_count"`
	FileCount        int                     `json:"file_count"`
	CreatedAt        string                  `json:"created_at"`
	PayloadSHA256    string                  `json:"payload_sha256"`
	PayloadBytes     int64                   `json:"payload_bytes"`
}

// Export writes the project's database from st to outPath. outPath is an
// operator-chosen location; it is cleaned and made absolute here so every
// later file operation sees a canonical path.
func Export(ctx context.Context, st *store.Store, project, outPath, codeGraphVersion string) (*Header, error) {
	outPath, err := cleanPath(outPath)
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	proj, err := st.GetProject(project)
	if err != nil {
		return nil, fmt.Errorf("export: read project: %w", err)
	}
	if proj == nil {
		return nil, fmt.Errorf("export: project %q not found", project)
	}
	tmpDir, err := os.MkdirTemp(filepath.Dir(outPath), ".cgraph-export-")
	if err != nil {
		return nil, fmt.Errorf("export: temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	snapshot := filepath.Join(tmpDir, "snapshot.db")
	if err := st.SnapshotTo(snapshot); err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	sum, size, err := hashFile(snapshot)
	if err != nil {
		return nil, err
	}
	h := &Header{
		Format:           Format,
		CodeGraphVersion: codeGraphVersion,
		SchemaVersion:    indexidentity.SchemaVersion,
		Project:          project,
		RootPath:         proj.RootPath,
		IndexedAt:        proj.IndexedAt,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		PayloadSHA256:    sum,
		PayloadBytes:     size,
	}
	if rec, err := st.GetIndexIdentity(project); err == nil && rec != nil {
		h.IdentityStatus = rec.Status
		h.IdentityReason = rec.Reason
		h.Identity = rec.Identity
	} else if err != nil {
		h.IdentityStatus = indexidentity.StatusError
		h.IdentityReason = err.Error()
	}
	h.NodeCount, _ = st.CountNodes(project)
	h.EdgeCount, _ = st.CountEdges(project)
	h.FileCount, _ = st.CountFileHashes(project)

	if err := writeArtifact(outPath, h, snapshot); err != nil {
		return nil, err
	}
	return h, nil
}

func writeArtifact(outPath string, h *Header, payloadPath string) error {
	headerJSON, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("export: encode header: %w", err)
	}
	tmp := outPath + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
	if err != nil {
		return fmt.Errorf("export: create %s: %w", tmp, err)
	}
	cleanup := func() { _ = f.Close(); _ = os.Remove(tmp) }
	if _, err := f.Write(magic); err != nil {
		cleanup()
		return err
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(headerJSON)))
	if _, err := f.Write(lenBuf[:]); err != nil {
		cleanup()
		return err
	}
	if _, err := f.Write(headerJSON); err != nil {
		cleanup()
		return err
	}
	enc, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		cleanup()
		return fmt.Errorf("export: zstd: %w", err)
	}
	src, err := os.Open(payloadPath)
	if err != nil {
		cleanup()
		return err
	}
	if _, err := io.Copy(enc, src); err != nil {
		_ = src.Close()
		cleanup()
		return fmt.Errorf("export: compress: %w", err)
	}
	_ = src.Close()
	if err := enc.Close(); err != nil {
		cleanup()
		return fmt.Errorf("export: finish zstd: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, outPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("export: rename: %w", err)
	}
	return nil
}

// ReadHeader parses the header of an artifact without decompressing it.
// It returns the header and the offset at which the zstd frame starts.
func ReadHeader(path string) (*Header, int64, error) {
	path, err := cleanPath(path)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	return readHeader(f)
}

func readHeader(r io.Reader) (*Header, int64, error) {
	buf := make([]byte, len(magic))
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, 0, fmt.Errorf("read artifact magic: %w", err)
	}
	if !bytes.Equal(buf, magic) {
		return nil, 0, errors.New("not a code-graph artifact (bad magic)")
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, 0, fmt.Errorf("read header length: %w", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > 1<<20 {
		return nil, 0, fmt.Errorf("implausible header length %d", n)
	}
	headerJSON := make([]byte, n)
	if _, err := io.ReadFull(r, headerJSON); err != nil {
		return nil, 0, fmt.Errorf("read header: %w", err)
	}
	var h Header
	if err := json.Unmarshal(headerJSON, &h); err != nil {
		return nil, 0, fmt.Errorf("decode header: %w", err)
	}
	if h.Format != Format {
		return nil, 0, fmt.Errorf("unsupported artifact format %d (this binary reads format %d)", h.Format, Format)
	}
	return &h, int64(len(magic) + 4 + int(n)), nil
}

// ImportOptions controls Import.
type ImportOptions struct {
	// RepoPath is the local checkout the artifact should serve. Empty means
	// the artifact's recorded root_path, which must exist locally.
	RepoPath string
	// CacheDir receives <project>.db; empty means store.CacheDir().
	CacheDir string
	// AllowStale imports even when the artifact was built from a different
	// revision or a dirty tree, or carries no identity.
	AllowStale bool
	// Force replaces an existing database for the project.
	Force bool
	// ProjectName derives the local project name from the absolute repo path
	// (pipeline.ProjectNameFromPath); required.
	ProjectName func(absPath string) string
	// Capture overrides identity capture (tests). nil uses indexidentity.Capture.
	Capture func(root string) (*indexidentity.Envelope, error)
}

// Report summarises an import.
type Report struct {
	Header      *Header `json:"header"`
	Project     string  `json:"project"`
	RepoPath    string  `json:"repo_path"`
	DBPath      string  `json:"db_path"`
	Stale       bool    `json:"stale"`
	StaleReason string  `json:"stale_reason,omitempty"`
	Renamed     bool    `json:"renamed"`
}

// ErrStale is returned when the artifact does not match the local checkout
// and AllowStale is false.
var ErrStale = errors.New("artifact does not match the local checkout")

// staleness is the outcome of comparing an artifact with the local checkout.
type staleness struct {
	stale  bool
	reason string
	local  *indexidentity.Envelope
}

// Import installs the artifact as the local project database for RepoPath.
func Import(ctx context.Context, path string, opts ImportOptions) (*Report, error) {
	if opts.ProjectName == nil {
		return nil, errors.New("import: ProjectName is required")
	}
	path, err := cleanPath(path)
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h, _, err := readHeader(f)
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	if h.SchemaVersion != indexidentity.SchemaVersion {
		return nil, fmt.Errorf("import: artifact schema_version %d, this binary expects %d; re-export with a matching code-graph", h.SchemaVersion, indexidentity.SchemaVersion)
	}

	repo, err := resolveRepo(opts.RepoPath, h.RootPath)
	if err != nil {
		return nil, err
	}
	newProject := opts.ProjectName(repo)

	st8, err := assessStaleness(h, repo, opts.Capture)
	if err != nil {
		return nil, err
	}
	if st8.stale && !opts.AllowStale {
		return nil, fmt.Errorf("%w: %s (use --allow-stale to import anyway)", ErrStale, st8.reason)
	}

	cacheDir, finalPath, err := resolveDestination(opts.CacheDir, newProject, opts.Force)
	if err != nil {
		return nil, err
	}

	tmpPath, err := stagePayload(f, h, cacheDir)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpPath)

	if err := prepareStagedDatabase(ctx, tmpPath, h, newProject, repo, st8); err != nil {
		return nil, err
	}
	if err := installDatabase(tmpPath, finalPath); err != nil {
		return nil, fmt.Errorf("import: install database: %w", err)
	}
	return &Report{
		Header:      h,
		Project:     newProject,
		RepoPath:    repo,
		DBPath:      finalPath,
		Stale:       st8.stale,
		StaleReason: st8.reason,
		Renamed:     newProject != h.Project,
	}, nil
}

// resolveRepo picks the checkout the artifact will serve and checks that it
// is a directory.
func resolveRepo(requested, recorded string) (string, error) {
	repo := requested
	if repo == "" {
		repo = recorded
	}
	repo, err := cleanPath(repo)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(repo); err != nil || !info.IsDir() { //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
		return "", fmt.Errorf("import: repository path %s is not a directory (pass --repo)", repo)
	}
	return repo, nil
}

// assessStaleness compares the artifact's recorded identity with the local
// checkout. A repository mismatch is an error; everything else is a
// staleness verdict the caller may override with AllowStale.
func assessStaleness(h *Header, repo string, capture func(string) (*indexidentity.Envelope, error)) (staleness, error) {
	if capture == nil {
		capture = indexidentity.Capture
	}
	if h.Identity == nil {
		return staleness{stale: true, reason: "artifact carries no checkout identity (" + h.IdentityStatus + ": " + h.IdentityReason + ")"}, nil
	}
	local, err := capture(repo)
	if err != nil {
		return staleness{stale: true, reason: "local checkout identity unavailable: " + err.Error()}, nil //nolint:nilerr // a capture failure is a staleness verdict, not an import error
	}
	if local.RepositoryID != h.Identity.RepositoryID {
		return staleness{}, fmt.Errorf("import: artifact was built from a different repository (repository_id %s, local %s)", short(h.Identity.RepositoryID), short(local.RepositoryID))
	}
	var reasons []string
	if local.SourceRevision != h.Identity.SourceRevision {
		reasons = append(reasons, fmt.Sprintf("revision %s in artifact, %s locally", short(h.Identity.SourceRevision), short(local.SourceRevision)))
	}
	if h.Identity.DirtyFingerprint != "clean" {
		reasons = append(reasons, "artifact was indexed from a dirty tree")
	}
	if local.DirtyFingerprint != "clean" {
		reasons = append(reasons, "local tree has uncommitted changes")
	}
	return staleness{stale: len(reasons) > 0, reason: strings.Join(reasons, "; "), local: local}, nil
}

// resolveDestination returns the cache directory and the final database path,
// refusing to overwrite an existing database unless force is set.
func resolveDestination(cacheDir, project string, force bool) (dir, finalPath string, err error) {
	if cacheDir == "" {
		cacheDir, err = store.CacheDir()
		if err != nil {
			return "", "", err
		}
	}
	cacheDir, err = cleanPath(cacheDir)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil { //nolint:gosec // operator-chosen cache directory, cleaned above
		return "", "", err
	}
	finalPath = filepath.Join(cacheDir, project+".db")
	if _, err := os.Stat(finalPath); err == nil && !force { //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
		return "", "", fmt.Errorf("import: %s already exists (use --force to replace the local index)", finalPath)
	}
	return cacheDir, finalPath, nil
}

// stagePayload decompresses the zstd frame that follows the header into a
// temporary file next to the destination (same volume, so installing it is a
// rename) and verifies its size and checksum against the header.
func stagePayload(r io.Reader, h *Header, cacheDir string) (string, error) {
	tmp, err := os.CreateTemp(cacheDir, ".cgraph-import-*.db")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	fail := func(err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	dec, err := zstd.NewReader(r)
	if err != nil {
		return fail(fmt.Errorf("import: zstd: %w", err))
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), dec)
	dec.Close()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmpPath) //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
		return "", fmt.Errorf("import: decompress: %w", err)
	}
	if n != h.PayloadBytes || hex.EncodeToString(hasher.Sum(nil)) != h.PayloadSHA256 {
		_ = os.Remove(tmpPath) //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
		return "", fmt.Errorf("import: payload checksum mismatch (%d bytes, want %d): artifact is corrupt", n, h.PayloadBytes)
	}
	return tmpPath, nil
}

// prepareStagedDatabase renames the project inside the staged database for
// the local checkout and records the identity verdict. The store is closed
// before returning so no handle to the staged file survives into
// installDatabase; on Windows an open handle would make the rename fail.
func prepareStagedDatabase(ctx context.Context, tmpPath string, h *Header, newProject, repo string, st8 staleness) error {
	st, err := store.OpenPath(tmpPath)
	if err != nil {
		return fmt.Errorf("import: open payload: %w", err)
	}
	if err := st.RewriteProject(ctx, h.Project, newProject, repo); err != nil {
		_ = st.Close()
		return fmt.Errorf("import: %w", err)
	}
	switch {
	case st8.stale:
		reason := "imported artifact does not match this checkout: " + st8.reason + "; re-run index_repository for a coherent index"
		if err := st.SetIndexIdentityState(newProject, indexidentity.StatusStaleSource, reason); err != nil {
			_ = st.Close()
			return err
		}
	case st8.local != nil:
		env := *h.Identity
		env.CheckoutID = st8.local.CheckoutID
		env.CapturedAt = time.Now().UTC().Format(time.RFC3339)
		if err := st.SetIndexIdentity(newProject, &env); err != nil {
			_ = st.Close()
			return err
		}
	}
	return st.Close()
}

// installDatabase moves the staged database into place.
//
// Handle lifecycle: by the time this runs every connection to tmpPath has
// been closed (prepareStagedDatabase closes its store before returning), and
// the destination is not opened by this process. Stale WAL/SHM siblings of
// both files are removed first so SQLite does not pair the new image with an
// old journal. The rename is retried briefly because Windows refuses to move
// a file while another process (typically an antivirus scanner that just saw
// a new file appear) still holds it, and if renaming keeps failing the image
// is copied over the destination and fsynced instead, which only needs write
// sharing on the destination.
func installDatabase(tmpPath, finalPath string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(tmpPath + suffix)   //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
		_ = os.Remove(finalPath + suffix) //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
	}
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if err = os.Rename(tmpPath, finalPath); err == nil { //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
			return nil
		}
		time.Sleep(time.Duration(25*(attempt+1)) * time.Millisecond)
	}
	if cerr := copyFile(tmpPath, finalPath); cerr != nil {
		return fmt.Errorf("%w (copy fallback also failed: %v)", err, cerr)
	}
	_ = os.Remove(tmpPath) //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
	return nil
}

// copyFile writes src over dst (truncating) and fsyncs the result.
func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // operator-chosen path, cleaned at the CLI boundary
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // destination inside the cleaned cache directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// cleanPath canonicalises an operator-supplied path once, at the boundary,
// so the rest of the package never handles relative or traversal-shaped
// input (the same discipline internal/store applies to CODE_GRAPH_CACHE_DIR).
func cleanPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", p, err)
	}
	return abs, nil
}

func hashFile(path string) (sum string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	hasher := sha256.New()
	n, err := io.Copy(hasher, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), n, nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
