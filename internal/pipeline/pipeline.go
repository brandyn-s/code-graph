package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/brandyn-s/code-graph/internal/config"
	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/store"
)

// EnrichmentVersion tracks the version of post-flush enrichment passes.
// Bump this manually whenever enrichment logic changes (new security roles,
// community detection params, HTTP link patterns, etc.). The index_status
// tool compares the stored version against this constant to detect stale enrichment.
const EnrichmentVersion = "2026.03.16.1"

// ProgressCallback is called between pipeline phases to report indexing progress.
// phase: "discover", "structure", "definitions", "calls", "flush", "tests",
//
//	"communities", "http_links", "security_tags", "opa_linker", "lockfile_deps", "complete"
//
// pct: 0-100 overall percent estimate. detail: human-readable status string.
type ProgressCallback func(phase string, pct int, detail string)

// IndexDelta describes which lifecycle path the most recent Run selected.
// It reports source classification, not inferred timing, so callers can
// distinguish a true no-op from a fast incremental or full rebuild.
type IndexDelta struct {
	Mode            string
	FilesDiscovered int
	FilesChanged    int
	FilesDeleted    int
	FilesUnchanged  int
}

// Pipeline orchestrates the 3-pass indexing of a repository.
type Pipeline struct {
	ctx         context.Context
	Store       *store.Store
	RepoPath    string
	ProjectName string
	Mode        discover.IndexMode
	// Progress is called between pipeline phases to report indexing progress.
	// May be nil if no progress reporting is needed.
	Progress ProgressCallback
	// LastNodeCount and LastEdgeCount carry the post-write node and edge
	// counts populated by Run(). Callers (e.g. handleIndexRepository) read
	// these instead of issuing a fresh CountNodes/CountEdges query —
	// post-bulk-write / post-WAL-checkpoint reads via a fresh `st` reference
	// were observed to return 0 even when 5,640 nodes had committed (code-
	// search 2026-05-26). The inner CountNodes inside Run() returns the
	// real counts because it reuses the same connection state as the writes;
	// exposing those values via fields propagates the truth instead of
	// re-querying. Zero when Run() has not been called (or returned early
	// on a no-op incremental).
	LastNodeCount  int
	LastEdgeCount  int
	LastIndexDelta IndexDelta
	// SCIPStatus reports whether the optional compiler-index precision tier was
	// applied and how much of the project's function graph it covered. It is
	// populated by passSCIPIngest and intentionally kept separate from the
	// generic node/edge counts so callers cannot mistake a partially-covered
	// SCIP index for compiler-grade coverage of the whole project.
	SCIPStatus SCIPIngestStatus
	// scipConfigured distinguishes an explicit per-project precision choice
	// (including an explicit heuristic/off choice) from the legacy process-wide
	// environment fallback.
	scipConfigured bool
	scipPath       string
	scipSource     string
	// buf holds all nodes/edges in memory during full-index passes 1-14.
	// nil during incremental mode and post-flush passes 15-18.
	buf *GraphBuffer
	// extractionCache maps file rel_path -> CBM extraction result for all post-definition passes
	extractionCache map[string]*cachedExtraction
	// registry indexes all Function/Method/Class nodes for call resolution
	registry *FunctionRegistry
	// importMaps stores per-module import maps: moduleQN -> localName -> resolvedQN
	importMaps map[string]map[string]string
	// importBindings stores per-module bare-name → full-import-path bindings
	// (Phase 3b of the registry.Resolve consolidation). For Rust
	// `use futures_util::future::ready;`, this map carries
	// "ready" → "futures_util::future::ready". The Tier 3 discriminator in
	// applyImportBindingFilter consults it to drop internal bare-name
	// candidates when the import target is external (or to prefer the
	// matching internal candidate when one exists).
	importBindings map[string]map[string]string
	// returnTypes maps function QN -> return type QN for return-type-based type inference
	returnTypes ReturnTypeMap
	// fieldTypes maps "<structQN>.<fieldName>" -> field type's class QN.
	// Populated globally from struct/enum field declarations in every file
	// before passCalls runs, then consulted by resolveCallWithTypes to walk
	// receiver chains like `obj.field.method()` (2026-05-02 plateau-diagnose
	// THEME F: ~87% of assetman residual is receiver-resolution failure).
	fieldTypes FieldTypeMap
	// traitImpls maps trait QN -> list of struct QNs that implement it
	// (Rust `impl Trait for Struct`). When a call resolves to `Trait.method`
	// and exactly one impl struct exists with `Struct.method`, the resolver
	// prefers the impl. Closes the trait/Impl naming-disagreement residual
	// (Pattern D in the 2026-05-02 categorization, ~27 method residuals).
	traitImpls map[string][]string
	// goLSPIdx indexes Go cross-file definitions for LSP resolution in pass3
	goLSPIdx *goLSPDefIndex
	// envReaders maps env var key -> list of function QNs that read it.
	// Built during semantic passes (pre-flush) for use in post-flush config linking.
	envReaders map[string][]string
	// unresolvedCallCounts maps caller QN -> number of CBM call sites that
	// the resolver did NOT successfully emit. Populated by passCalls; written
	// to Function/Method node properties by passWriteUnresolvedCounts in the
	// post-flush phase (must run AFTER buffer flush to avoid being clobbered).
	unresolvedCallCounts map[string]int
	// rustCrateMap: Rust crate name (with `-` -> `_` normalized) -> the
	// path dotted-segment prefix where that crate's src/ lives. Built at
	// the start of pass2 by scanning Cargo.toml files under RepoPath.
	// Used in passImports to resolve `use foo_crate::Y::Z` to the actual
	// Module node at `<project>.<foo_crate_dir>.src.Y.Z`.
	rustCrateMap map[string]string

	// externalCrates / workspaceMembers — Tier-2 v0.1 (2026-05-24).
	// Populated by populateCargoMetadata at index start (via `cargo
	// metadata --no-deps`). Consumed by the chain walker in
	// resolveCallWithTypes: when a chain root names a crate in
	// externalCrates AND NOT in workspaceMembers, the walker emits
	// chainReceiverType="_external.<crate>" and the resolver drops the
	// edge instead of fuzzy-matching the bare callee into an in-graph
	// candidate (the PR #341 dominant FP shape — diesel external-crate
	// dispatch matched to AssetIntrospectImpl.get_result).
	//
	// Nil sets are valid: on cargo failure (no toolchain, no Cargo.toml,
	// timeout, parse error), populateCargoMetadata leaves these nil and
	// the chain walker's external check degrades to a no-op. Non-Rust
	// indexing skips the populate call entirely.
	externalCrates   map[string]bool
	workspaceMembers map[string]bool
}

// reportProgress safely calls the progress callback if set.
func (p *Pipeline) reportProgress(phase string, pct int, detail string) {
	if p.Progress != nil {
		p.Progress(phase, pct, detail)
	}
}

// New creates a new Pipeline.
func New(ctx context.Context, s *store.Store, repoPath string, mode discover.IndexMode) *Pipeline {
	if mode == "" {
		mode = discover.ModeFull
	}
	projectName := ProjectNameFromPath(repoPath)
	return &Pipeline{
		ctx:             ctx,
		Store:           s,
		RepoPath:        repoPath,
		ProjectName:     projectName,
		Mode:            mode,
		extractionCache: make(map[string]*cachedExtraction),
		registry:        NewFunctionRegistry(),
		importMaps:      make(map[string]map[string]string),
		importBindings:  make(map[string]map[string]string),
		rustCrateMap:    make(map[string]string),
	}
}

// ConfigureSCIP binds a per-project compiler index to this pipeline run. An
// empty path explicitly selects the heuristic tier and suppresses the legacy
// process-wide environment fallback.
func (p *Pipeline) ConfigureSCIP(path, source string) {
	p.scipConfigured = true
	p.scipPath = path
	p.scipSource = source
}

// buildRustCrateMap scans Cargo.toml files under RepoPath, parses
// `[package] name = "..."`, and maps the crate name (with `-` -> `_`
// applied to match Rust's use-path convention) to the dotted rel-path
// of the crate's `src/` directory.
//
// For a repo with layout:
//
//	canstatd/Cargo.toml           (name = "canstatd")
//	canstatd/src/main.rs
//	canstatd/types/Cargo.toml     (name = "canstatd-types")
//	canstatd/types/src/lib.rs
//
// When indexed with RepoPath = canstatd/, we build:
//
//	"canstatd"       -> "src"
//	"canstatd_types" -> "types.src"
//
// Then in passImports, `use canstatd_types::CanError` gets rewritten to
// `types.src.CanError` (dot-prefixed) for suffix-matching against the
// actual node `<project>.types.src.lib.CanError`.
func (p *Pipeline) buildRustCrateMap() {
	_ = filepath.WalkDir(p.RepoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Skip target/ — contains build artifacts, not source.
			if d.Name() == "target" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "Cargo.toml" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		name := parseCargoPackageName(string(data))
		if name == "" {
			return nil // workspace-only Cargo.toml
		}
		crateDir := filepath.Dir(path)
		// Check if src/ exists; if not, skip (virtual manifest).
		srcDir := filepath.Join(crateDir, "src")
		if info, serr := os.Stat(srcDir); serr != nil || !info.IsDir() {
			return nil
		}
		relDir, rerr := filepath.Rel(p.RepoPath, srcDir)
		if rerr != nil {
			return nil
		}
		relDotted := strings.ReplaceAll(filepath.ToSlash(relDir), "/", ".")
		// Rust: `use foo-bar::...` is illegal; the crate name in code is
		// always `foo_bar`. Normalize both directions.
		key := strings.ReplaceAll(name, "-", "_")
		if _, exists := p.rustCrateMap[key]; !exists {
			p.rustCrateMap[key] = relDotted
		}
		return nil
	})
	if len(p.rustCrateMap) > 0 {
		slog.Info("pipeline.rust_crate_map", "count", len(p.rustCrateMap))
	}
}

// parseCargoPackageName extracts `[package] name = "..."` from Cargo.toml
// text. Returns "" if there's no [package] section (workspace root) or
// no name field. Deliberately minimal — doesn't need a full TOML parser
// to grab a single field under a specific section.
func parseCargoPackageName(text string) string {
	idx := strings.Index(text, "[package]")
	if idx < 0 {
		return ""
	}
	// Bound the search to end at the next [section] header.
	rest := text[idx+len("[package]"):]
	if nextIdx := strings.Index(rest, "\n["); nextIdx >= 0 {
		rest = rest[:nextIdx]
	}
	// Find `name = "X"` within the section.
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "name") {
			continue
		}
		// `name = "foo"` or `name="foo"`.
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		v := strings.TrimSpace(line[eq+1:])
		v = strings.Trim(v, `"'`)
		return v
	}
	return ""
}

// ProjectNameFromPath derives a unique project name from an absolute path
// by replacing path separators with dashes and trimming the leading dash.
func ProjectNameFromPath(absPath string) string {
	// Clean and normalize separators (backslash is not a separator on non-Windows)
	cleaned := filepath.ToSlash(filepath.Clean(absPath))
	cleaned = strings.ReplaceAll(cleaned, "\\", "/")
	// Normalize Windows drive letter casing: "D:/foo" → "d:/foo"
	// Prevents duplicate DBs for same path with different drive letter case.
	if len(cleaned) >= 2 && cleaned[1] == ':' {
		cleaned = strings.ToLower(cleaned[:1]) + cleaned[1:]
	}
	// Replace slashes and colons with dashes
	name := strings.ReplaceAll(cleaned, "/", "-")
	name = strings.ReplaceAll(name, ":", "-")
	// Collapse consecutive dashes (e.g. C:/ → C--)
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	// Trim leading dash (from leading /)
	name = strings.TrimLeft(name, "-")
	if name == "" {
		return "root"
	}
	return name
}

// ErrHeapPressure is returned by checkCancel when the in-memory graph
// buffer + extraction caches push HeapAlloc past CODE_GRAPH_HEAP_LIMIT_MB.
// Pass orchestration treats it like a context cancellation — the pipeline
// aborts cleanly between passes rather than racing toward OOM. The
// returned error includes the configured limit and the observed
// allocation so operators can size the limit appropriately.
var ErrHeapPressure = errors.New("pipeline aborted: heap pressure exceeded CODE_GRAPH_HEAP_LIMIT_MB")

// heapLimitBytes resolves the operator-configured heap limit. Returns 0
// when CODE_GRAPH_HEAP_LIMIT_MB is unset or unparseable, which disables
// the check (the production default). Read each call so tests can flip
// the env without restarting the binary.
func heapLimitBytes() uint64 {
	raw := config.Get(config.HeapLimitMB)
	if raw == "" {
		return 0
	}
	mb, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || mb == 0 {
		return 0
	}
	return mb << 20
}

// heapPressureExceeded compares HeapAlloc to the configured limit and
// returns (heapAlloc, true) when the limit fired. Factored out so tests
// can drive the same decision without spinning up a full pipeline.
func heapPressureExceeded() (uint64, uint64, bool) {
	limit := heapLimitBytes()
	if limit == 0 {
		return 0, 0, false
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc, limit, m.HeapAlloc > limit
}

// checkCancel returns a non-nil error if the pipeline should abort the
// current pass. Two reasons: the caller's context was cancelled, or
// HeapAlloc has crossed the CODE_GRAPH_HEAP_LIMIT_MB threshold. Pass
// boundaries already call this between passes so the heap check inherits
// the existing abort-cleanly machinery without new call sites.
func (p *Pipeline) checkCancel() error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if alloc, limit, over := heapPressureExceeded(); over {
		slog.Warn("pipeline.heap_pressure",
			"heap_alloc_mb", alloc/(1<<20),
			"limit_mb", limit/(1<<20),
			"action", "aborting pass cleanly",
		)
		return fmt.Errorf("%w (heap=%dMB, limit=%dMB)", ErrHeapPressure, alloc/(1<<20), limit/(1<<20))
	}
	return nil
}

// --- Bridge methods: dispatch to in-memory buffer or SQLite store ---

func (p *Pipeline) upsertNode(n *store.Node) error {
	if p.buf != nil {
		p.buf.UpsertNode(n)
		return nil
	}
	_, err := p.Store.UpsertNode(n)
	return err
}

func (p *Pipeline) upsertNodeBatch(nodes []*store.Node) (map[string]int64, error) {
	if p.buf != nil {
		return p.buf.UpsertNodeBatch(nodes), nil
	}
	return p.Store.UpsertNodeBatch(nodes)
}

func (p *Pipeline) insertEdge(e *store.Edge) error {
	if p.buf != nil {
		p.buf.InsertEdge(e)
		return nil
	}
	_, err := p.Store.InsertEdge(e)
	return err
}

func (p *Pipeline) insertEdgeBatch(edges []*store.Edge) error {
	if p.buf != nil {
		p.buf.InsertEdgeBatch(edges)
		return nil
	}
	return p.Store.InsertEdgeBatch(edges)
}

func (p *Pipeline) findNodesByLabel(project, label string) ([]*store.Node, error) {
	if p.buf != nil {
		return p.buf.FindNodesByLabel(label), nil
	}
	return p.Store.FindNodesByLabel(project, label)
}

func (p *Pipeline) findNodeByQN(project, qn string) (*store.Node, error) {
	if p.buf != nil {
		return p.buf.FindNodeByQN(qn), nil
	}
	return p.Store.FindNodeByQN(project, qn)
}

func (p *Pipeline) findNodeByID(id int64) (*store.Node, error) {
	if p.buf != nil {
		return p.buf.FindNodeByID(id), nil
	}
	return p.Store.FindNodeByID(id)
}

func (p *Pipeline) findNodeIDsByQNs(project string, qns []string) (map[string]int64, error) {
	if p.buf != nil {
		return p.buf.FindNodeIDsByQNs(qns), nil
	}
	return p.Store.FindNodeIDsByQNs(project, qns)
}

func (p *Pipeline) findNodeLabelsByQNs(project string, qns []string) (map[string]string, error) {
	if p.buf != nil {
		return p.buf.FindNodeLabelsByQNs(qns), nil
	}
	return p.Store.FindNodeLabelsByQNs(project, qns)
}

func (p *Pipeline) findNodesByQNSuffix(project, suffix string) ([]*store.Node, error) {
	if p.buf != nil {
		return p.buf.FindNodesByQNSuffix(suffix), nil
	}
	return p.Store.FindNodesByQNSuffix(project, suffix)
}

func (p *Pipeline) findEdgesBySourceAndType(sourceID int64, edgeType string) ([]*store.Edge, error) {
	if p.buf != nil {
		return p.buf.FindEdgesBySourceAndType(sourceID, edgeType), nil
	}
	return p.Store.FindEdgesBySourceAndType(sourceID, edgeType)
}
