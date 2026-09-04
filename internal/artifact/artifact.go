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

// Export writes the project's database from st to outPath.
func Export(ctx context.Context, st *store.Store, project, outPath, codeGraphVersion string) (*Header, error) {
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
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
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
	if string(buf) != string(magic) {
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

// Import installs the artifact as the local project database for RepoPath.
func Import(ctx context.Context, path string, opts ImportOptions) (*Report, error) {
	if opts.ProjectName == nil {
		return nil, errors.New("import: ProjectName is required")
	}
	capture := opts.Capture
	if capture == nil {
		capture = indexidentity.Capture
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

	repo := opts.RepoPath
	if repo == "" {
		repo = h.RootPath
	}
	repo, err = filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(repo); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("import: repository path %s is not a directory (pass --repo)", repo)
	}
	newProject := opts.ProjectName(repo)

	// Staleness against the local checkout.
	stale, staleReason := false, ""
	var local *indexidentity.Envelope
	switch {
	case h.Identity == nil:
		stale, staleReason = true, "artifact carries no checkout identity ("+h.IdentityStatus+": "+h.IdentityReason+")"
	default:
		local, err = capture(repo)
		if err != nil {
			stale, staleReason = true, "local checkout identity unavailable: "+err.Error()
		} else if local.RepositoryID != h.Identity.RepositoryID {
			return nil, fmt.Errorf("import: artifact was built from a different repository (repository_id %s, local %s)", short(h.Identity.RepositoryID), short(local.RepositoryID))
		} else {
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
			if len(reasons) > 0 {
				stale, staleReason = true, strings.Join(reasons, "; ")
			}
		}
	}
	if stale && !opts.AllowStale {
		return nil, fmt.Errorf("%w: %s (use --allow-stale to import anyway)", ErrStale, staleReason)
	}

	cacheDir := opts.CacheDir
	if cacheDir == "" {
		cacheDir, err = store.CacheDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, err
	}
	finalPath := filepath.Join(cacheDir, newProject+".db")
	if _, err := os.Stat(finalPath); err == nil && !opts.Force {
		return nil, fmt.Errorf("import: %s already exists (use --force to replace the local index)", finalPath)
	}

	// Decompress next to the destination so the final rename is atomic.
	tmp, err := os.CreateTemp(cacheDir, ".cgraph-import-*.db")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	dec, err := zstd.NewReader(f)
	if err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("import: zstd: %w", err)
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), dec)
	dec.Close()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("import: decompress: %w", err)
	}
	if n != h.PayloadBytes || hex.EncodeToString(hasher.Sum(nil)) != h.PayloadSHA256 {
		return nil, fmt.Errorf("import: payload checksum mismatch (%d bytes, want %d): artifact is corrupt", n, h.PayloadBytes)
	}

	st, err := store.OpenPath(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("import: open payload: %w", err)
	}
	renamed := newProject != h.Project
	if err := st.RewriteProject(ctx, h.Project, newProject, repo); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("import: %w", err)
	}
	switch {
	case stale:
		reason := "imported artifact does not match this checkout: " + staleReason + "; re-run index_repository for a coherent index"
		if err := st.SetIndexIdentityState(newProject, indexidentity.StatusStaleSource, reason); err != nil {
			_ = st.Close()
			return nil, err
		}
	case local != nil:
		env := *h.Identity
		env.CheckoutID = local.CheckoutID
		env.CapturedAt = time.Now().UTC().Format(time.RFC3339)
		if err := st.SetIndexIdentity(newProject, &env); err != nil {
			_ = st.Close()
			return nil, err
		}
	}
	if err := st.Close(); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(tmpPath + suffix)
		_ = os.Remove(finalPath + suffix)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return nil, fmt.Errorf("import: install database: %w", err)
	}
	return &Report{Header: h, Project: newProject, RepoPath: repo, DBPath: finalPath, Stale: stale, StaleReason: staleReason, Renamed: renamed}, nil
}

func hashFile(path string) (string, int64, error) {
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
