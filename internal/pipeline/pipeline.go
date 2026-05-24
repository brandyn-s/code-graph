package pipeline

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zeebo/xxh3"
	"golang.org/x/sync/errgroup"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/fqn"
	"github.com/DeusData/codebase-memory-mcp/internal/httplink"
	"github.com/DeusData/codebase-memory-mcp/internal/lang"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
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

// buildRustCrateMap scans Cargo.toml files under RepoPath, parses
// `[package] name = "..."`, and maps the crate name (with `-` -> `_`
// applied to match Rust's use-path convention) to the dotted rel-path
// of the crate's `src/` directory.
//
// For a repo with layout:
//   canstatd/Cargo.toml           (name = "canstatd")
//   canstatd/src/main.rs
//   canstatd/types/Cargo.toml     (name = "canstatd-types")
//   canstatd/types/src/lib.rs
// When indexed with RepoPath = canstatd/, we build:
//   "canstatd"       -> "src"
//   "canstatd_types" -> "types.src"
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
	raw := os.Getenv("CODE_GRAPH_HEAP_LIMIT_MB")
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

// Run executes the full 3-pass pipeline within a single transaction.
// If file hashes from a previous run exist, only changed files are re-processed.
func (p *Pipeline) Run() error {
	runStart := time.Now()
	slog.Info("pipeline.start", "project", p.ProjectName, "path", p.RepoPath, "mode", string(p.Mode))

	if err := p.checkCancel(); err != nil {
		return err
	}

	// Discover source files (filesystem, no DB — runs outside transaction)
	discoverOpts := &discover.Options{Mode: p.Mode}
	if p.Mode == discover.ModeFast {
		discoverOpts.MaxFileSize = 512 * 1024 // 512KB cutoff in fast mode
	}
	files, err := discover.Discover(p.ctx, p.RepoPath, discoverOpts)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	slog.Info("pipeline.discovered", "files", len(files))
	p.reportProgress("discover", 5, fmt.Sprintf("%d files discovered", len(files)))
	logHeapStats("pre_index")

	// Use MEMORY journal mode during fresh indexing for faster bulk writes.
	p.Store.BeginBulkWrite(p.ctx)

	wroteData := false
	if err := p.Store.WithTransaction(p.ctx, func(txStore *store.Store) error {
		origStore := p.Store
		p.Store = txStore
		defer func() { p.Store = origStore }()
		var passErr error
		wroteData, passErr = p.runPasses(files)
		return passErr
	}); err != nil {
		p.Store.EndBulkWrite(p.ctx)
		return err
	}

	p.Store.EndBulkWrite(p.ctx)

	// Only checkpoint + optimize when actual data was written.
	// No-op incremental reindexes skip this to avoid ANALYZE overhead.
	if wroteData {
		walBefore := p.Store.WALSize()
		p.Store.Checkpoint(p.ctx)
		walAfter := p.Store.WALSize()
		slog.Info("wal.checkpoint", "before_mb", walBefore/(1<<20), "after_mb", walAfter/(1<<20))
	}

	nc, _ := p.Store.CountNodes(p.ProjectName)
	ec, _ := p.Store.CountEdges(p.ProjectName)
	logHeapStats("post_index")
	slog.Info("pipeline.done", "nodes", nc, "edges", ec, "total_elapsed", time.Since(runStart))
	return nil
}

// runPasses executes all indexing passes (called within a transaction).
// Returns (wroteData, error) — wroteData is true if nodes/edges were written.
func (p *Pipeline) runPasses(files []discover.FileInfo) (bool, error) {
	if err := p.Store.UpsertProject(p.ProjectName, p.RepoPath); err != nil {
		return false, fmt.Errorf("upsert project: %w", err)
	}

	// Classify files as changed/unchanged using stored hashes
	changed, unchanged := p.classifyFiles(files)

	// If all files are changed (first index or no hashes), do full pass
	isFullIndex := len(unchanged) == 0
	if isFullIndex {
		if err := p.runFullPasses(files); err != nil {
			return true, err
		}
		_ = p.Store.ResetIncrementalsSinceFull(p.ProjectName)
		return true, nil
	}

	// Periodic-full-reindex sentinel (Plan 1 Phase 1). The incremental
	// dependency-discovery heuristic in findDependentFiles misses some
	// shapes (transitive importers, type-based dependents, stranded
	// route handlers) so silently-stale edges accumulate over many
	// incrementals. Force a full reindex every N incrementals to bound
	// the staleness lifetime. N = CODE_GRAPH_FULL_REINDEX_EVERY (default
	// 50). Set to 0 to disable.
	limit := fullReindexEvery()
	if limit > 0 {
		isf, _ := p.Store.GetIncrementalsSinceFull(p.ProjectName)
		if isf >= limit {
			slog.Info("incremental.force_full",
				"reason", "sentinel",
				"incrementals_since_full", isf,
				"limit", limit,
			)
			if err := p.runFullPasses(files); err != nil {
				return true, err
			}
			_ = p.Store.ResetIncrementalsSinceFull(p.ProjectName)
			return true, nil
		}
	}

	slog.Info("incremental.classify", "changed", len(changed), "unchanged", len(unchanged), "total", len(files))

	// Fast path: nothing changed → skip all heavy passes
	if len(changed) == 0 {
		slog.Info("incremental.noop", "reason", "no_changes")
		return false, nil
	}

	if err := p.runIncrementalPasses(files, changed, unchanged); err != nil {
		return true, err
	}
	_ = p.Store.IncrementIncrementalsSinceFull(p.ProjectName)
	return true, nil
}

// fullReindexEvery resolves the periodic-full-reindex threshold from
// CODE_GRAPH_FULL_REINDEX_EVERY. Default 50; 0 (or unparseable) disables
// the sentinel and lets the incremental path run indefinitely.
func fullReindexEvery() int {
	raw := os.Getenv("CODE_GRAPH_FULL_REINDEX_EVERY")
	if raw == "" {
		return 50
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 50
	}
	return n
}

// runFullPasses runs the complete pipeline (no incremental optimization).
func (p *Pipeline) runFullPasses(files []discover.FileInfo) error {
	// Initialize in-memory graph buffer for passes 1-14.
	// All node/edge writes go to RAM; flushed to SQLite after pass 14.
	p.buf = newGraphBuffer(p.ProjectName)

	t := time.Now()
	if err := p.passStructure(files); err != nil {
		return fmt.Errorf("pass1 structure: %w", err)
	}
	slog.Info("pass.timing", "pass", "structure", "elapsed", time.Since(t))
	p.reportProgress("structure", 10, fmt.Sprintf("%d files structured", len(files)))
	if err := p.checkCancel(); err != nil {
		return err
	}

	t = time.Now()
	p.passDefinitions(files) // includes Variable extraction + enrichment
	slog.Info("pass.timing", "pass", "definitions", "elapsed", time.Since(t))
	p.reportProgress("definitions", 30, fmt.Sprintf("%d files parsed", len(files)))
	logHeapStats("post_definitions")
	if err := p.checkCancel(); err != nil {
		return err
	}

	p.reportProgress("decorator_tags", 32, "discovering decorator tags")
	t = time.Now()
	p.passDecoratorTags() // auto-discover decorator semantic tags
	slog.Info("pass.timing", "pass", "decorator_tags", "elapsed", time.Since(t))

	p.reportProgress("registry", 34, "building symbol registry")
	t = time.Now()
	p.buildRegistry() // includes Variable label
	slog.Info("pass.timing", "pass", "registry", "elapsed", time.Since(t))
	if err := p.checkCancel(); err != nil {
		return err
	}

	p.reportProgress("inherits", 36, "resolving inheritance edges")
	t = time.Now()
	p.passInherits() // INHERITS edges from base_classes
	slog.Info("pass.timing", "pass", "inherits", "elapsed", time.Since(t))

	p.reportProgress("decorates", 38, "resolving decorator edges")
	t = time.Now()
	p.passDecorates() // DECORATES edges from decorators
	slog.Info("pass.timing", "pass", "decorates", "elapsed", time.Since(t))
	if err := p.checkCancel(); err != nil {
		return err
	}

	p.reportProgress("dataflow", 39, "creating parameter nodes")
	t = time.Now()
	p.passDataflow() // Parameter nodes + PARAMETER_OF edges
	slog.Info("pass.timing", "pass", "dataflow", "elapsed", time.Since(t))

	p.reportProgress("imports", 40, "building import maps")
	t = time.Now()
	// Build the Rust crate-name → path map BEFORE passImports so it can
	// use it for resolving `use crate_name::X::Y` paths. No-op for non-Rust
	// projects (Cargo.toml walk returns nothing).
	p.buildRustCrateMap()
	// Tier-2 v0.1: ingest cargo-metadata to classify external vs
	// workspace crates. Gated on Cargo.toml presence inside
	// populateCargoMetadata; on any failure (missing cargo, parse error,
	// timeout) the maps stay nil and the downstream chain-walker check
	// degrades to a no-op. No-op for non-Rust projects (Cargo.toml
	// absent → early return inside runCargoMetadata).
	p.populateCargoMetadata()
	p.passImports()
	slog.Info("pass.timing", "pass", "imports", "elapsed", time.Since(t))
	p.reportProgress("imports", 42, "import maps built")
	if err := p.checkCancel(); err != nil {
		return err
	}

	t = time.Now()
	p.buildReturnTypeMap()
	p.buildFieldTypeMap()
	p.buildTraitImplMap()
	p.goLSPIdx = p.buildGoLSPDefIndex()
	if p.goLSPIdx != nil {
		p.goLSPIdx.integrateThirdPartyDeps(p.RepoPath, p.importMaps)
	}
	p.passCalls()
	slog.Info("pass.timing", "pass", "calls", "elapsed", time.Since(t))
	p.reportProgress("calls", 60, "call targets resolved")
	// Release heavy fields no longer needed after call resolution.
	// Definitions + Calls + TypeAssigns + Imports dominate extractionCache memory
	// (~160 KB/file → 16 GB for 100K-file repos). Nil them to halve peak RSS.
	p.releaseExtractionFields(fieldsPostCalls)
	p.goLSPIdx = nil // no longer needed after call resolution
	logHeapStats("post_calls")
	if err := p.checkCancel(); err != nil {
		return err
	}

	p.reportProgress("usages", 60, "resolving usage edges")
	t = time.Now()
	p.passUsages()
	slog.Info("pass.timing", "pass", "usages", "elapsed", time.Since(t))
	p.reportProgress("usages", 62, "usage edges resolved")
	p.releaseExtractionFields(fieldsPostUsages)
	if err := p.checkCancel(); err != nil {
		return err
	}

	p.runSemanticEdgePasses()
	// All semantic fields consumed — release remaining before implements.
	p.releaseExtractionFields(fieldsPostSemantic)
	if err := p.checkCancel(); err != nil {
		return err
	}

	// passImplements needs extractionCache for Rust impl traits,
	// so it must run before cleanupASTCache.
	p.reportProgress("implements", 72, "resolving interface implementations")
	t = time.Now()
	p.passImplements()
	slog.Info("pass.timing", "pass", "implements", "elapsed", time.Since(t))

	p.cleanupASTCache()
	logHeapStats("post_cleanup")

	// Flush in-memory buffer to SQLite with deferred index creation.
	p.reportProgress("flush", 72, "writing graph to SQLite")
	if err := p.buf.FlushTo(p.ctx, p.Store); err != nil {
		return fmt.Errorf("graph_buffer flush: %w", err)
	}
	p.buf = nil

	// Post-flush passes use Store directly (need indexes).
	return p.runPostFlushPasses(files)
}

// runPostFlushPasses runs passes that require SQLite indexes (post graph-buffer flush).
func (p *Pipeline) runPostFlushPasses(files []discover.FileInfo) error {
	// passWriteUnresolvedCounts writes the unresolved_call_count diagnostic
	// property staged by passCalls. Runs FIRST in post-flush so subsequent
	// passes see the property if they read node properties (none currently
	// do, but ordering invariants are cheap to maintain).
	t := time.Now()
	p.passWriteUnresolvedCounts()
	slog.Info("pass.timing", "pass", "unresolved_counts", "elapsed", time.Since(t))

	p.reportProgress("tests", 75, "resolving test edges")
	t = time.Now()
	p.passTests() // TESTS/TESTS_FILE edges (DB-only)
	slog.Info("pass.timing", "pass", "tests", "elapsed", time.Since(t))

	p.reportProgress("communities", 78, "detecting communities")
	t = time.Now()
	p.passCommunities() // Community nodes + MEMBER_OF edges (DB-only)
	slog.Info("pass.timing", "pass", "communities", "elapsed", time.Since(t))
	if err := p.checkCancel(); err != nil {
		return err
	}

	p.reportProgress("http_links", 82, "linking HTTP routes")
	t = time.Now()
	if err := p.passHTTPLinks(); err != nil {
		slog.Warn("pass.httplink.err", "err", err)
	}
	slog.Info("pass.timing", "pass", "httplinks", "elapsed", time.Since(t))

	p.reportProgress("config_linker", 86, "linking config to code")
	t = time.Now()
	p.passConfigLinker()
	slog.Info("pass.timing", "pass", "configlinker", "elapsed", time.Since(t))

	p.reportProgress("envvar_nodes", 87, "creating env var nodes")
	t = time.Now()
	p.passEnvVarNodes()
	slog.Info("pass.timing", "pass", "envvar_nodes", "elapsed", time.Since(t))

	p.reportProgress("git_history", 89, "analyzing git history")
	t = time.Now()
	p.passGitHistory()
	slog.Info("pass.timing", "pass", "githistory", "elapsed", time.Since(t))

	p.reportProgress("security_tags", 92, "assigning security roles")
	t = time.Now()
	p.passSecurityTags()
	slog.Info("pass.timing", "pass", "security_tags", "elapsed", time.Since(t))

	p.reportProgress("opa_linker", 94, "linking OPA policies")
	t = time.Now()
	p.passOPALinker()
	slog.Info("pass.timing", "pass", "opa_linker", "elapsed", time.Since(t))

	p.reportProgress("zenoh", 95, "extracting Zenoh pub/sub edges")
	t = time.Now()
	p.passZenoh()
	slog.Info("pass.timing", "pass", "zenoh", "elapsed", time.Since(t))

	p.reportProgress("nix_services", 95, "extracting Nix service topic bindings")
	t = time.Now()
	p.passNixServices()
	slog.Info("pass.timing", "pass", "nix_services", "elapsed", time.Since(t))

	p.reportProgress("lockfile_deps", 96, "parsing lockfile deps")
	t = time.Now()
	p.passLockfileDeps()
	slog.Info("pass.timing", "pass", "lockfile_deps", "elapsed", time.Since(t))

	// Rationale extraction is cheap (regex line scan) and runs before
	// embeddings so the Rationale nodes are embeddable if the later
	// pass decides to include them. Scans the same files the extraction
	// cache already tracks.
	p.reportProgress("rationale", 96, "extracting WHY/NOTE/HACK/SAFETY comments")
	t = time.Now()
	p.passRationale()
	slog.Info("pass.timing", "pass", "rationale", "elapsed", time.Since(t))

	p.reportProgress("embeddings", 97, "generating embeddings")
	t = time.Now()
	p.passEmbeddings()
	slog.Info("pass.timing", "pass", "embeddings", "elapsed", time.Since(t))

	// Similarity edges must run after embeddings (needs vectors) and before
	// file_hashes (purely observational). Gated by ENABLE_SIMILARITY_EDGES
	// — see similarity_edges.go for why this is off by default.
	p.reportProgress("similarity", 97, "emitting similarity edges")
	t = time.Now()
	p.passSimilarityEdges()
	slog.Info("pass.timing", "pass", "similarity", "elapsed", time.Since(t))

	p.reportProgress("file_hashes", 98, "updating file hashes")
	t = time.Now()
	p.updateFileHashes(files)
	slog.Info("pass.timing", "pass", "filehashes", "elapsed", time.Since(t))

	// Observability: per-edge-type counts
	p.logEdgeCounts()

	// Record the enrichment version so index_status can detect stale enrichment.
	if err := p.Store.SetEnrichmentVersion(p.ProjectName, EnrichmentVersion); err != nil {
		slog.Warn("pipeline.enrichment_version.err", "err", err)
	}

	p.reportProgress("complete", 100, "indexing complete")
	return nil
}

// runSemanticEdgePasses runs the semantic edge passes (USES_TYPE, THROWS, READS/WRITES, CONFIGURES).
func (p *Pipeline) runSemanticEdgePasses() {
	p.reportProgress("semantic:uses_type", 62, "resolving type references")
	t := time.Now()
	p.passUsesType()
	slog.Info("pass.timing", "pass", "usestype", "elapsed", time.Since(t))

	p.reportProgress("semantic:throws", 65, "resolving throw/raise edges")
	t = time.Now()
	p.passThrows()
	slog.Info("pass.timing", "pass", "throws", "elapsed", time.Since(t))

	p.reportProgress("semantic:readwrite", 67, "resolving read/write edges")
	t = time.Now()
	p.passReadsWrites()
	slog.Info("pass.timing", "pass", "readwrite", "elapsed", time.Since(t))

	p.reportProgress("semantic:configures", 69, "resolving config edges")
	t = time.Now()
	p.passConfigures()
	slog.Info("pass.timing", "pass", "configures", "elapsed", time.Since(t))

	p.reportProgress("semantic:env_readers", 71, "building env reader snapshot")
	// Snapshot env var readers from extraction cache before it's released.
	// Used by passConfigLinker's terraform_env strategy in post-flush phase.
	p.buildEnvReaders()

}

// logEdgeCounts logs the count of each edge type for observability.
func (p *Pipeline) logEdgeCounts() {
	edgeTypes := []string{
		"CALLS", "USAGE", "IMPORTS", "DEFINES", "DEFINES_METHOD",
		"TESTS", "TESTS_FILE", "INHERITS", "DECORATES", "USES_TYPE",
		"THROWS", "RAISES", "READS", "WRITES", "CONFIGURES", "MEMBER_OF",
		"HTTP_CALLS", "HANDLES", "ASYNC_CALLS", "IMPLEMENTS", "OVERRIDE",
		"FILE_CHANGES_WITH", "CONTAINS_FILE", "CONTAINS_FOLDER", "CONTAINS_PACKAGE", "DEPENDS_ON",
		"POLICY_GATES", "PARAMETER_OF",
	}
	for _, edgeType := range edgeTypes {
		count, err := p.Store.CountEdgesByType(p.ProjectName, edgeType)
		if err == nil && count > 0 {
			slog.Info("pipeline.edges", "type", edgeType, "count", count)
		}
	}
}

// runIncrementalPasses re-indexes only changed files + their dependents.
func (p *Pipeline) runIncrementalPasses(
	allFiles []discover.FileInfo,
	changed, unchanged []discover.FileInfo,
) error {
	// Pass 1: Structure always runs on all files (fast, idempotent upserts)
	if err := p.passStructure(allFiles); err != nil {
		return fmt.Errorf("pass1 structure: %w", err)
	}
	if err := p.checkCancel(); err != nil {
		return err
	}

	// Remove stale nodes/edges for deleted files
	p.removeDeletedFiles(allFiles)

	// Delete nodes for changed files (will be re-created in pass 2)
	for _, f := range changed {
		_ = p.Store.DeleteNodesByFile(p.ProjectName, f.RelPath)
	}

	// Pass 2: Parse changed files only
	p.passDefinitions(changed)
	if err := p.checkCancel(); err != nil {
		return err
	}

	// Re-compute decorator tags globally (threshold is across all nodes)
	p.passDecoratorTags()

	// Build full registry: includes nodes from unchanged files (already in DB)
	// plus newly parsed nodes from changed files
	p.buildRegistry()
	if err := p.checkCancel(); err != nil {
		return err
	}

	// Re-build import maps for changed files (already done in passDefinitions)
	// Also load import maps for unchanged files from their AST (not cached)
	// For correctness, we need the full import map, but unchanged files don't
	// have ASTs cached. Rebuild imports only for changed files is sufficient
	// since unchanged file import edges still exist in DB.
	p.passImports()
	if err := p.checkCancel(); err != nil {
		return err
	}

	// Determine which files need call re-resolution. Two sources of
	// dependents:
	//   (a) Direct importers of changed modules (the original heuristic).
	//   (b) Callers of any node defined in a changed file (Plan 1 Phase 3).
	//       Catches transitive callers, type-based dispatch, and stranded
	//       handlers whose resolution depends on a node in a changed file
	//       but whose own module didn't import the changed module
	//       directly. Backed by store.FindCallerFilesOfTargetsInFiles
	//       against the indexed (target_id, type) edge composite.
	//
	// If the union grows past the cap we fall through to a full reindex —
	// at that scale the incremental's "skip unchanged files" win is gone
	// and a full reindex is more correct and roughly the same cost.
	dependents := p.findDependentFiles(changed, unchanged)
	callerDependents := p.findCallerOfTargetDependents(changed, unchanged)
	filesToResolve := mergeFiles(changed, dependents)
	filesToResolve = mergeFiles(filesToResolve, callerDependents)
	slog.Info("incremental.resolve",
		"changed", len(changed),
		"importer_dependents", len(dependents),
		"caller_of_target_dependents", len(callerDependents),
		"total_to_resolve", len(filesToResolve),
	)

	if limit := incrementalCap(); limit > 0 && len(filesToResolve) > limit {
		slog.Info("incremental.fallback_to_full",
			"reason", "files_to_resolve_over_cap",
			"files_to_resolve", len(filesToResolve),
			"cap", limit,
		)
		return p.runFullPasses(allFiles)
	}

	// Delete edges for files being re-resolved (all AST-derived edge types)
	for _, f := range filesToResolve {
		_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "CALLS")
		_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "USAGE")
		_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "USES_TYPE")
		_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "THROWS")
		_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "RAISES")
		_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "READS")
		_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "WRITES")
		_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, "CONFIGURES")
	}

	// Re-resolve calls + usages for changed + dependent files
	p.buildReturnTypeMap()
	p.buildFieldTypeMap()
	p.buildTraitImplMap()
	p.goLSPIdx = p.buildGoLSPDefIndex()
	if p.goLSPIdx != nil {
		p.goLSPIdx.integrateThirdPartyDeps(p.RepoPath, p.importMaps)
	}
	p.passCallsForFiles(filesToResolve)
	p.releaseExtractionFields(fieldsPostCalls)
	p.goLSPIdx = nil
	p.passUsagesForFiles(filesToResolve)
	p.releaseExtractionFields(fieldsPostUsages)
	if err := p.checkCancel(); err != nil {
		return err
	}

	// AST-dependent passes (run on cached files before cleanup)
	p.passUsesType()
	p.passThrows()
	p.passReadsWrites()
	p.passConfigures()
	p.buildEnvReaders()
	p.releaseExtractionFields(fieldsPostSemantic)
	if err := p.checkCancel(); err != nil {
		return err
	}

	p.cleanupASTCache()

	// DB-derived edge types: delete all and re-run (cheap)
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "TESTS")
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "TESTS_FILE")
	p.passTests()

	_ = p.Store.DeleteEdgesByType(p.ProjectName, "INHERITS")
	p.passInherits()

	_ = p.Store.DeleteEdgesByType(p.ProjectName, "DECORATES")
	p.passDecorates()

	_ = p.Store.DeleteEdgesByType(p.ProjectName, "PARAMETER_OF")
	_ = p.Store.DeleteNodesByLabel(p.ProjectName, "Parameter")
	p.passDataflow()

	// Community detection: delete old communities and MEMBER_OF, re-run
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "MEMBER_OF")
	_ = p.Store.DeleteNodesByLabel(p.ProjectName, "Community")
	p.passCommunities()
	if err := p.checkCancel(); err != nil {
		return err
	}

	// HTTP linking, config linking, and implements always run fully (they clean up first)
	if err := p.passHTTPLinks(); err != nil {
		slog.Warn("pass.httplink.err", "err", err)
	}
	p.passConfigLinker()
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "READS_ENV")
	_ = p.Store.DeleteNodesByLabel(p.ProjectName, "EnvVar")
	p.passEnvVarNodes()
	p.passImplements()
	p.passGitHistory()
	p.passOPALinker()
	p.passZenoh()
	p.passNixServices()

	p.updateFileHashes(allFiles)

	// Observability
	p.logEdgeCounts()

	// Record the enrichment version so index_status can detect stale enrichment.
	if err := p.Store.SetEnrichmentVersion(p.ProjectName, EnrichmentVersion); err != nil {
		slog.Warn("pipeline.enrichment_version.err", "err", err)
	}

	return nil
}

// classifyFiles splits files into changed and unchanged based on stored hashes.
// Uses stat (mtime+size) as a fast pre-filter: files whose mtime and size match
// the stored values are assumed unchanged without reading/hashing. Only files
// with changed stat (or missing from the store) are hashed.
func (p *Pipeline) classifyFiles(files []discover.FileInfo) (changed, unchanged []discover.FileInfo) {
	storedHashes, err := p.Store.GetFileHashes(p.ProjectName)
	if err != nil || len(storedHashes) == 0 {
		return files, nil // no hashes → full index
	}

	// Stage 1: stat pre-filter — separate files into "stat-unchanged" and "needs-hash"
	var needsHash []discover.FileInfo
	for _, f := range files {
		stored, ok := storedHashes[f.RelPath]
		if !ok {
			needsHash = append(needsHash, f) // new file
			continue
		}
		fi, statErr := os.Stat(f.Path)
		if statErr != nil {
			needsHash = append(needsHash, f) // stat failed → hash it
			continue
		}
		if fi.ModTime().UnixNano() == stored.MtimeNs && fi.Size() == stored.Size && stored.MtimeNs != 0 {
			// Stat matches — trust the stored hash
			unchanged = append(unchanged, f)
		} else {
			needsHash = append(needsHash, f)
		}
	}

	if len(needsHash) == 0 {
		return changed, unchanged // nothing to hash
	}

	// Stage 2: hash only files that need it
	type hashResult struct {
		Hash string
		Err  error
	}

	results := make([]hashResult, len(needsHash))
	numWorkers := runtime.NumCPU()
	if numWorkers > len(needsHash) {
		numWorkers = len(needsHash)
	}

	g := new(errgroup.Group)
	g.SetLimit(numWorkers)
	for i, f := range needsHash {
		g.Go(func() error {
			hash, hashErr := fileHash(f.Path)
			results[i] = hashResult{Hash: hash, Err: hashErr}
			return nil
		})
	}
	_ = g.Wait()

	for i, f := range needsHash {
		r := results[i]
		if r.Err != nil {
			changed = append(changed, f)
			continue
		}
		if stored, ok := storedHashes[f.RelPath]; ok && stored.SHA256 == r.Hash {
			unchanged = append(unchanged, f)
		} else {
			changed = append(changed, f)
		}
	}
	return changed, unchanged
}

// findCallerOfTargetDependents returns unchanged files containing call
// sites whose target nodes live in changed files. Complements
// findDependentFiles (which walks the import graph one hop) by directly
// querying the existing CALLS/USES/HTTP_CALLS edge tables for callers
// of any node in a changed file. Catches transitive callers, type-based
// dispatch resolutions, and stranded handlers — the leak classes the
// import-graph heuristic misses.
//
// The result is the intersection of "files containing CALL-shape
// callers of changed-file targets" with "unchanged files" — adding a
// changed file to its own dependent set is harmless but the cap check
// in runIncrementalPasses works in terms of files-to-resolve, so we
// keep this set strictly disjoint from `changed`.
func (p *Pipeline) findCallerOfTargetDependents(changed, unchanged []discover.FileInfo) []discover.FileInfo {
	if len(changed) == 0 || len(unchanged) == 0 {
		return nil
	}
	changedPaths := make([]string, 0, len(changed))
	for _, f := range changed {
		changedPaths = append(changedPaths, f.RelPath)
	}
	callerFiles, err := p.Store.FindCallerFilesOfTargetsInFiles(
		p.ProjectName,
		changedPaths,
		[]string{"CALLS", "USES", "HTTP_CALLS"},
	)
	if err != nil {
		slog.Warn("incremental.caller_of_target.err", "err", err)
		return nil
	}
	if len(callerFiles) == 0 {
		return nil
	}
	callerSet := make(map[string]struct{}, len(callerFiles))
	for _, fp := range callerFiles {
		callerSet[fp] = struct{}{}
	}
	// Exclude changed files — they're re-resolved unconditionally.
	for _, f := range changed {
		delete(callerSet, f.RelPath)
	}
	var out []discover.FileInfo
	for _, f := range unchanged {
		if _, ok := callerSet[f.RelPath]; ok {
			out = append(out, f)
		}
	}
	return out
}

// incrementalCap resolves CODE_GRAPH_INCREMENTAL_CAP — the maximum
// number of files-to-resolve before runIncrementalPasses falls through
// to a full reindex. Default 10000. Set to 0 to disable the cap (keep
// expanding the dependent set indefinitely; not recommended for large
// repos).
func incrementalCap() int {
	raw := os.Getenv("CODE_GRAPH_INCREMENTAL_CAP")
	if raw == "" {
		return 10000
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 10000
	}
	return n
}

// findDependentFiles finds unchanged files that import any changed file's module.
func (p *Pipeline) findDependentFiles(changed, unchanged []discover.FileInfo) []discover.FileInfo {
	// Build set of module QNs for changed files
	changedModules := make(map[string]bool, len(changed))
	for _, f := range changed {
		mqn := fqn.ModuleQN(p.ProjectName, f.RelPath)
		changedModules[mqn] = true
		// Also add folder QN (for Go package-level imports)
		dir := filepath.Dir(f.RelPath)
		if dir != "." {
			changedModules[fqn.FolderQN(p.ProjectName, dir)] = true
		}
	}

	var dependents []discover.FileInfo
	for _, f := range unchanged {
		mqn := fqn.ModuleQN(p.ProjectName, f.RelPath)
		importMap := p.importMaps[mqn]
		// If no cached import map, check the store for IMPORTS edges
		if len(importMap) == 0 {
			importMap = p.loadImportMapFromDB(mqn)
		}
		for _, targetQN := range importMap {
			if changedModules[targetQN] {
				dependents = append(dependents, f)
				break
			}
		}
	}
	return dependents
}

// loadImportMapFromDB reconstructs an import map from stored IMPORTS edges.
func (p *Pipeline) loadImportMapFromDB(moduleQN string) map[string]string {
	moduleNode, err := p.Store.FindNodeByQN(p.ProjectName, moduleQN)
	if err != nil || moduleNode == nil {
		return nil
	}
	edges, err := p.Store.FindEdgesBySourceAndType(moduleNode.ID, "IMPORTS")
	if err != nil {
		return nil
	}
	result := make(map[string]string, len(edges))
	for _, e := range edges {
		target, tErr := p.Store.FindNodeByID(e.TargetID)
		if tErr != nil || target == nil {
			continue
		}
		alias := ""
		if a, ok := e.Properties["alias"].(string); ok {
			alias = a
		}
		if alias != "" {
			result[alias] = target.QualifiedName
		}
	}
	return result
}

// passCallsForFiles resolves calls only for the specified files (incremental).
//
// Unresolved-count handling mirrors the full-pass aggregator (passCalls
// stage 3, lines 1856-1862): we accumulate counts per caller-QN across
// every file in this incremental batch and write each caller's total
// EXACTLY ONCE at the end. Before this aggregation, the function wrote
// unresolved_call_count inline per-file via SetNodeIntProperty which
// uses json_set (overwrite, not add) — so a caller whose unresolved
// call sites spanned files A and B would end up with B's count alone,
// silently losing A's contribution. Rare in practice (most callers are
// file-local) but a real semantic divergence between full and
// incremental paths.
func (p *Pipeline) passCallsForFiles(files []discover.FileInfo) {
	slog.Info("pass3.calls.incremental", "files", len(files))
	aggregatedUnresolved := make(map[string]int)
	for _, f := range files {
		if p.ctx.Err() != nil {
			return
		}
		ext, ok := p.extractionCache[f.RelPath]
		if !ok {
			// File not in extraction cache — need to extract it
			source, err := os.ReadFile(f.Path)
			if err != nil {
				continue
			}
			source = stripBOM(source)
			cbmResult, err := cbm.ExtractFile(source, f.Language, p.ProjectName, f.RelPath)
			if err != nil {
				continue
			}
			ext = &cachedExtraction{Result: cbmResult, Language: f.Language}
			p.extractionCache[f.RelPath] = ext
		}
		edges, unresolved := p.resolveFileCallsCBM(f.RelPath, ext)
		// Release Definitions/Imports per-file after call resolution
		if ext.Result != nil {
			ext.Result.Definitions = nil
			ext.Result.Imports = nil
		}
		for _, re := range edges {
			callerNode, _ := p.Store.FindNodeByQN(p.ProjectName, re.CallerQN)
			targetNode, _ := p.Store.FindNodeByQN(p.ProjectName, re.TargetQN)
			if callerNode != nil && targetNode != nil {
				_, _ = p.Store.InsertEdge(&store.Edge{
					Project:    p.ProjectName,
					SourceID:   callerNode.ID,
					TargetID:   targetNode.ID,
					Type:       re.Type,
					Properties: re.Properties,
				})
			}
		}
		// Aggregate this file's unresolved counts; defer the write to
		// the end of the batch so a caller across multiple files gets
		// the sum, not just the last file's contribution.
		for callerQN, count := range unresolved {
			if count > 0 {
				aggregatedUnresolved[callerQN] += count
			}
		}
	}
	for callerQN, count := range aggregatedUnresolved {
		_, _ = p.Store.SetNodeIntProperty(p.ProjectName, callerQN, "unresolved_call_count", count)
	}
}

// removeDeletedFiles removes nodes/edges for files that no longer exist on disk.
func (p *Pipeline) removeDeletedFiles(currentFiles []discover.FileInfo) {
	currentSet := make(map[string]bool, len(currentFiles))
	for _, f := range currentFiles {
		currentSet[f.RelPath] = true
	}
	indexed, err := p.Store.ListFilesForProject(p.ProjectName)
	if err != nil {
		return
	}
	for _, filePath := range indexed {
		if !currentSet[filePath] {
			_ = p.Store.DeleteNodesByFile(p.ProjectName, filePath)
			_ = p.Store.DeleteFileHash(p.ProjectName, filePath)
			slog.Info("incremental.removed", "file", filePath)
		}
	}
}

// fieldGroup identifies which FileResult fields to release after a pass.
type fieldGroup int

const (
	fieldsPostCalls    fieldGroup = iota // Definitions, Calls, ResolvedCalls, TypeAssigns, Imports
	fieldsPostUsages                     // Usages
	fieldsPostSemantic                   // TypeRefs, Throws, ReadWrites, EnvAccesses
)

// releaseExtractionFields nils out consumed FileResult slices to reduce peak memory.
// Each FileResult field is used by specific passes; once a pass completes, its fields
// can be released. For a 100K-file repo, Definitions+Calls alone hold ~10 GB.
func (p *Pipeline) releaseExtractionFields(group fieldGroup) {
	for _, ext := range p.extractionCache {
		if ext.Result == nil {
			continue
		}
		switch group {
		case fieldsPostCalls:
			ext.Result.Definitions = nil
			ext.Result.Calls = nil
			ext.Result.ResolvedCalls = nil
			ext.Result.TypeAssigns = nil
			ext.Result.Imports = nil
		case fieldsPostUsages:
			ext.Result.Usages = nil
		case fieldsPostSemantic:
			ext.Result.TypeRefs = nil
			ext.Result.Throws = nil
			ext.Result.ReadWrites = nil
			ext.Result.EnvAccesses = nil
		}
	}
}

func (p *Pipeline) cleanupASTCache() {
	// Release extraction cache (Go GC handles the cbm.FileResult structs)
	p.extractionCache = nil
	// Prompt the Go runtime to return freed pages to the OS.
	// Especially useful under GOMEMLIMIT to keep RSS closer to actual usage.
	debug.FreeOSMemory()
}

// logHeapStats logs current Go heap metrics for memory diagnostics.
func logHeapStats(stage string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	slog.Info("mem.stats",
		"stage", stage,
		"heap_inuse_mb", m.HeapInuse/(1<<20),
		"heap_alloc_mb", m.HeapAlloc/(1<<20),
		"sys_mb", m.Sys/(1<<20),
	)
}

func (p *Pipeline) updateFileHashes(files []discover.FileInfo) {
	type hashResult struct {
		Hash    string
		MtimeNs int64
		Size    int64
		Err     error
	}

	results := make([]hashResult, len(files))
	numWorkers := runtime.NumCPU()
	if numWorkers > len(files) {
		numWorkers = len(files)
	}

	g := new(errgroup.Group)
	g.SetLimit(numWorkers)
	for i, f := range files {
		g.Go(func() error {
			hash, hashErr := fileHash(f.Path)
			r := hashResult{Hash: hash, Err: hashErr}
			if hashErr == nil {
				if fi, statErr := os.Stat(f.Path); statErr == nil {
					r.MtimeNs = fi.ModTime().UnixNano()
					r.Size = fi.Size()
				}
			}
			results[i] = r
			return nil
		})
	}
	_ = g.Wait()

	// Collect successful hashes for batch upsert
	batch := make([]store.FileHash, 0, len(files))
	for i, f := range files {
		if results[i].Err == nil {
			batch = append(batch, store.FileHash{
				Project: p.ProjectName,
				RelPath: f.RelPath,
				SHA256:  results[i].Hash,
				MtimeNs: results[i].MtimeNs,
				Size:    results[i].Size,
			})
		}
	}
	_ = p.Store.UpsertFileHashBatch(batch)
}

// mergeFiles returns the union of two file slices (deduped by RelPath).
func mergeFiles(a, b []discover.FileInfo) []discover.FileInfo {
	seen := make(map[string]bool, len(a))
	result := make([]discover.FileInfo, 0, len(a)+len(b))
	for _, f := range a {
		seen[f.RelPath] = true
		result = append(result, f)
	}
	for _, f := range b {
		if !seen[f.RelPath] {
			result = append(result, f)
		}
	}
	return result
}

// passStructure creates Project, Folder, Package, File nodes and containment edges.
// Collects all nodes/edges in memory first, then batch-writes to DB.
func (p *Pipeline) passStructure(files []discover.FileInfo) error {
	slog.Info("pass1.structure")

	dirSet, dirIsPackage := p.classifyDirectories(files)

	nodes := make([]*store.Node, 0, len(files)*2)
	edges := make([]pendingEdge, 0, len(files)*2)

	projectQN := p.ProjectName
	nodes = append(nodes, &store.Node{
		Project:       p.ProjectName,
		Label:         "Project",
		Name:          p.ProjectName,
		QualifiedName: projectQN,
	})

	dirNodes, dirEdges := p.buildDirNodesEdges(dirSet, dirIsPackage, projectQN)
	nodes = append(nodes, dirNodes...)
	edges = append(edges, dirEdges...)

	fileNodes, fileEdges := p.buildFileNodesEdges(files)
	nodes = append(nodes, fileNodes...)
	edges = append(edges, fileEdges...)

	return p.batchWriteStructure(nodes, edges)
}

// classifyDirectories collects all directories and determines which are packages.
func (p *Pipeline) classifyDirectories(files []discover.FileInfo) (allDirs, packageDirs map[string]bool) {
	packageIndicators := make(map[string]bool)
	for _, l := range lang.AllLanguages() {
		spec := lang.ForLanguage(l)
		if spec != nil {
			for _, pi := range spec.PackageIndicators {
				packageIndicators[pi] = true
			}
		}
	}

	allDirs = make(map[string]bool)
	for _, f := range files {
		dir := filepath.Dir(f.RelPath)
		for dir != "." && dir != "" && !allDirs[dir] {
			allDirs[dir] = true
			dir = filepath.Dir(dir)
		}
	}

	packageDirs = make(map[string]bool, len(allDirs))
	for dir := range allDirs {
		absDir := filepath.Join(p.RepoPath, dir)
		for indicator := range packageIndicators {
			if _, err := os.Stat(filepath.Join(absDir, indicator)); err == nil {
				packageDirs[dir] = true
				break
			}
		}
	}
	return
}

func (p *Pipeline) buildDirNodesEdges(dirSet, dirIsPackage map[string]bool, projectQN string) ([]*store.Node, []pendingEdge) {
	nodes := make([]*store.Node, 0, len(dirSet))
	edges := make([]pendingEdge, 0, len(dirSet))

	for dir := range dirSet {
		label := "Folder"
		edgeType := "CONTAINS_FOLDER"
		if dirIsPackage[dir] {
			label = "Package"
			edgeType = "CONTAINS_PACKAGE"
		}
		qn := fqn.FolderQN(p.ProjectName, dir)
		nodes = append(nodes, &store.Node{
			Project:       p.ProjectName,
			Label:         label,
			Name:          filepath.Base(dir),
			QualifiedName: qn,
			FilePath:      dir,
		})

		parent := filepath.Dir(dir)
		parentQN := projectQN
		if parent != "." && parent != "" {
			parentQN = fqn.FolderQN(p.ProjectName, parent)
		}
		edges = append(edges, pendingEdge{SourceQN: parentQN, TargetQN: qn, Type: edgeType})
	}
	return nodes, edges
}

func (p *Pipeline) buildFileNodesEdges(files []discover.FileInfo) ([]*store.Node, []pendingEdge) {
	nodes := make([]*store.Node, 0, len(files))
	edges := make([]pendingEdge, 0, len(files))

	for _, f := range files {
		fileQN := fqn.Compute(p.ProjectName, f.RelPath, "") + ".__file__"
		fileProps := map[string]any{
			"extension": filepath.Ext(f.RelPath),
			"is_test":   isTestFile(f.RelPath, f.Language),
		}
		if f.Language != "" {
			fileProps["language"] = string(f.Language)
		}
		nodes = append(nodes, &store.Node{
			Project:       p.ProjectName,
			Label:         "File",
			Name:          filepath.Base(f.RelPath),
			QualifiedName: fileQN,
			FilePath:      f.RelPath,
			Properties:    fileProps,
		})

		parentQN := p.dirQN(filepath.Dir(f.RelPath))
		edges = append(edges, pendingEdge{SourceQN: parentQN, TargetQN: fileQN, Type: "CONTAINS_FILE"})
	}
	return nodes, edges
}

func (p *Pipeline) batchWriteStructure(nodes []*store.Node, edges []pendingEdge) error {
	idMap, err := p.upsertNodeBatch(nodes)
	if err != nil {
		return fmt.Errorf("pass1 batch upsert: %w", err)
	}

	realEdges := make([]*store.Edge, 0, len(edges))
	for _, pe := range edges {
		srcID, srcOK := idMap[pe.SourceQN]
		tgtID, tgtOK := idMap[pe.TargetQN]
		if srcOK && tgtOK {
			realEdges = append(realEdges, &store.Edge{
				Project:    p.ProjectName,
				SourceID:   srcID,
				TargetID:   tgtID,
				Type:       pe.Type,
				Properties: pe.Properties,
			})
		}
	}

	if err := p.insertEdgeBatch(realEdges); err != nil {
		return fmt.Errorf("pass1 batch edges: %w", err)
	}
	return nil
}

func (p *Pipeline) dirQN(relDir string) string {
	if relDir == "." || relDir == "" {
		return p.ProjectName
	}
	return fqn.FolderQN(p.ProjectName, relDir)
}

// pendingEdge represents an edge to be created after batch node insertion,
// using qualified names that will be resolved to IDs.
type pendingEdge struct {
	SourceQN   string
	TargetQN   string
	Type       string
	Properties map[string]any
}

// parseResult holds the output of a pure file parse (no DB access).
type parseResult struct {
	File           discover.FileInfo
	Nodes          []*store.Node
	PendingEdges   []pendingEdge
	ImportMap      map[string]string
	ImportBindings map[string]string // Phase 3b: bare-name → import target path
	CBMResult      *cbm.FileResult   // CBM extraction result (nil when using legacy AST path)
	Err            error
}

// progressTicker logs a progress line every 5 seconds for long-running parallel phases.
// Returns a stop function that must be called when the phase completes.
func progressTicker(project, phase string, counter *atomic.Int64, total, basePct int) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n := int(counter.Load())
				pct := basePct + (n*10)/total
				slog.Info("index.progress", "project", project, "phase", phase, "pct", pct, "detail", fmt.Sprintf("%d/%d files", n, total))
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}

// passDefinitions extracts definitions from each file via CBM (C extraction library).
// Uses parallel extraction (Stage 1) followed by sequential batch DB writes (Stage 2).
func (p *Pipeline) passDefinitions(files []discover.FileInfo) {
	slog.Info("pass2.definitions")

	// Enrich JSON files with URL constants (for HTTP linking), then include
	// them in normal CBM extraction so they also get Variable/Class nodes.
	parseableFiles := make([]discover.FileInfo, 0, len(files))
	for _, f := range files {
		if f.Language == lang.JSON {
			if p.ctx.Err() != nil {
				return
			}
			if err := p.processJSONFile(f); err != nil {
				slog.Warn("pass2.json.err", "path", f.RelPath, "err", err)
			}
		}
		parseableFiles = append(parseableFiles, f)
	}

	if len(parseableFiles) == 0 {
		return
	}

	// Stage 1: Parallel CBM extraction (I/O + CPU, no DB, no shared state)
	// Adaptive pool auto-tunes concurrency via AIMD throughput feedback.
	t1 := time.Now()
	results := make([]*parseResult, len(parseableFiles))

	// Start readahead prefetcher to warm page cache ahead of workers
	pf := newPrefetcher(parseableFiles, 100)
	go pf.run(p.ctx)
	defer pf.stop()

	pool := newAdaptivePool(runtime.NumCPU())
	go pool.monitor(p.ctx)

	var parsed atomic.Int64
	totalFiles := len(parseableFiles)
	lastReportedPct := 10 // structure pass already reported 10%
	stopTicker := progressTicker(p.ProjectName, "definitions:parse", &parsed, totalFiles, 10)

	var wg sync.WaitGroup
	for i, f := range parseableFiles {
		pool.acquire()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer pool.releaseBytes(f.Size)
			if p.ctx.Err() != nil {
				return
			}
			results[i] = cbmParseFile(p.ProjectName, f)
			pf.advance(i + 1)
			done := int(parsed.Add(1))
			// Report progress every ~5% of files (10% -> 20% range)
			pct := 10 + (done*10)/totalFiles // maps [0, totalFiles] to [10, 20]
			if pct > lastReportedPct && pct <= 20 {
				lastReportedPct = pct
				p.reportProgress("definitions:parse", pct, fmt.Sprintf("%d/%d files parsed", done, totalFiles))
			}
		}()
	}
	wg.Wait()
	pool.stop()
	stopTicker()
	p.reportProgress("definitions:parse", 20, fmt.Sprintf("%d files parsed", totalFiles))
	slog.Info("pass2.stage1.extract", "files", len(parseableFiles), "elapsed", time.Since(t1))

	// Log C-side parse vs extraction breakdown
	profile := cbm.GetProfile()
	if profile.Files > 0 {
		slog.Info("pass2.stage1.profile",
			"files", profile.Files,
			"parse_total", time.Duration(profile.ParseNs),
			"extract_total", time.Duration(profile.ExtractNs),
			"parse_avg_us", profile.ParseNs/profile.Files/1000,
			"extract_avg_us", profile.ExtractNs/profile.Files/1000,
		)
	}

	// Stage 2: Sequential cache population + batch DB writes
	t2 := time.Now()
	var allNodes []*store.Node
	var allPendingEdges []pendingEdge

	for _, r := range results {
		if r == nil {
			continue
		}
		if r.Err != nil {
			slog.Warn("pass2.file.err", "path", r.File.RelPath, "err", r.Err)
			continue
		}
		// Populate extraction cache for use by later passes
		if r.CBMResult != nil {
			p.extractionCache[r.File.RelPath] = &cachedExtraction{
				Result:   r.CBMResult,
				Language: r.File.Language,
			}
		}
		// Store import map
		moduleQN := fqn.ModuleQN(p.ProjectName, r.File.RelPath)
		if len(r.ImportMap) > 0 {
			p.importMaps[moduleQN] = r.ImportMap
		}
		if len(r.ImportBindings) > 0 {
			p.importBindings[moduleQN] = r.ImportBindings
		}
		allNodes = append(allNodes, r.Nodes...)
		allPendingEdges = append(allPendingEdges, r.PendingEdges...)
	}

	slog.Info("pass2.stage2.collect", "nodes", len(allNodes), "edges", len(allPendingEdges), "elapsed", time.Since(t2))
	p.reportProgress("definitions:collect", 23, fmt.Sprintf("%d nodes, %d edges collected", len(allNodes), len(allPendingEdges)))

	// Batch insert all nodes
	t3 := time.Now()
	idMap, err := p.upsertNodeBatch(allNodes)
	if err != nil {
		slog.Warn("pass2.batch_upsert.err", "err", err)
		return
	}
	slog.Info("pass2.stage3.upsert_nodes", "nodes", len(allNodes), "elapsed", time.Since(t3))
	p.reportProgress("definitions:upsert", 26, fmt.Sprintf("%d nodes upserted", len(allNodes)))

	// Resolve pending edges to real edges using the ID map
	t4 := time.Now()
	edges := make([]*store.Edge, 0, len(allPendingEdges))
	for _, pe := range allPendingEdges {
		srcID, srcOK := idMap[pe.SourceQN]
		tgtID, tgtOK := idMap[pe.TargetQN]
		if srcOK && tgtOK {
			edges = append(edges, &store.Edge{
				Project:    p.ProjectName,
				SourceID:   srcID,
				TargetID:   tgtID,
				Type:       pe.Type,
				Properties: pe.Properties,
			})
		}
	}

	if err := p.insertEdgeBatch(edges); err != nil {
		slog.Warn("pass2.batch_edges.err", "err", err)
	}
	slog.Info("pass2.stage4.insert_edges", "edges", len(edges), "elapsed", time.Since(t4))
}

// buildRegistry populates the FunctionRegistry from all Function, Method,
// and Class nodes in the store.
func (p *Pipeline) buildRegistry() {
	// ACC-003: Module included so resolveViaTypeStaticDispatch can route
	// qualified-path module-dispatch calls (`diagnostics::router(...)`).
	// Modules go into a separate r.modules index inside Register; they
	// never appear in r.byName, so downstream callable-resolution paths
	// are unaffected.
	labels := []string{"Function", "Method", "Class", "Type", "Interface", "Enum", "Macro", "Variable", "Module"}
	for _, label := range labels {
		nodes, err := p.findNodesByLabel(p.ProjectName, label)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			p.registry.Register(n.Name, n.QualifiedName, n.Label)
		}
	}
	slog.Info("registry.built", "entries", p.registry.Size())
}

// buildReturnTypeMap builds a map from function QN to its return type QN.
// Uses the "return_types" property stored on Function/Method nodes during pass2.
func (p *Pipeline) buildReturnTypeMap() {
	p.returnTypes = make(ReturnTypeMap)
	for _, label := range []string{"Function", "Method"} {
		nodes, err := p.findNodesByLabel(p.ProjectName, label)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			retTypes, ok := n.Properties["return_types"]
			if !ok {
				continue
			}
			// return_types is stored as []any (JSON round-trip) containing type name strings
			typeList, ok := retTypes.([]any)
			if !ok || len(typeList) == 0 {
				continue
			}
			// Use the first return type — most functions return a single type
			firstType, ok := typeList[0].(string)
			if !ok || firstType == "" {
				continue
			}
			// Resolve the type name to a class QN
			classQN := resolveAsClass(firstType, p.registry, "", nil)
			if classQN != "" {
				p.returnTypes[n.QualifiedName] = classQN
			}
		}
	}
	if len(p.returnTypes) > 0 {
		slog.Info("return_types.built", "entries", len(p.returnTypes))
	}
}

// resolvedEdge represents an edge resolved during parallel call/usage resolution,
// stored as QN pairs to be converted to ID-based edges in the batch write stage.
type resolvedEdge struct {
	CallerQN   string
	TargetQN   string
	Type       string // "CALLS" or "USAGE"
	Properties map[string]any
}

// passCalls resolves call targets and creates CALLS edges.
// Uses parallel per-file resolution (Stage 1) followed by batch DB writes (Stage 2).
func (p *Pipeline) passCalls() {
	slog.Info("pass3.calls")

	// Collect files to process from extraction cache
	type fileEntry struct {
		relPath string
		ext     *cachedExtraction
	}
	var files []fileEntry
	for relPath, ext := range p.extractionCache {
		if lang.ForLanguage(ext.Language) != nil {
			files = append(files, fileEntry{relPath, ext})
		}
	}

	if len(files) == 0 {
		return
	}

	// Stage 1: Parallel per-file call resolution using CBM data
	results := make([][]resolvedEdge, len(files))
	unresolvedPerFile := make([]map[string]int, len(files))
	numWorkers := runtime.NumCPU()
	if numWorkers > len(files) {
		numWorkers = len(files)
	}

	var resolved atomic.Int64
	totalCallFiles := len(files)
	lastCallPct := 45
	stopCallTicker := progressTicker(p.ProjectName, "calls:resolve", &resolved, totalCallFiles, 45)

	g, gctx := errgroup.WithContext(p.ctx)
	g.SetLimit(numWorkers)
	for i, fe := range files {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			results[i], unresolvedPerFile[i] = p.resolveFileCallsCBM(fe.relPath, fe.ext)
			// Release heavy fields per-file immediately after call resolution.
			// Definitions + Imports are only needed for Go LSP cross-file inside
			// resolveFileCallsCBM. Releasing here reduces peak from O(all_files)
			// to O(concurrent_workers) for these fields.
			if fe.ext.Result != nil {
				fe.ext.Result.Definitions = nil
				fe.ext.Result.Imports = nil
			}
			done := int(resolved.Add(1))
			// Report progress every ~5% of files (45% -> 55% range)
			pct := 45 + (done*10)/totalCallFiles
			if pct > lastCallPct && pct <= 55 {
				lastCallPct = pct
				p.reportProgress("calls:resolve", pct, fmt.Sprintf("%d/%d files resolved", done, totalCallFiles))
			}
			return nil
		})
	}
	_ = g.Wait()
	stopCallTicker()
	p.reportProgress("calls:resolve", 55, fmt.Sprintf("%d files resolved", totalCallFiles))

	// Stage 2: Batch QN→ID resolution + batch edge insert
	p.reportProgress("calls:flush", 57, "flushing call edges to graph")
	p.flushResolvedEdges(results)

	// Stage 3: Aggregate unresolved counts per caller. A caller may appear
	// in multiple files (Rust impl spread across files, partial classes).
	// Stored on Pipeline; written to node properties by
	// passWriteUnresolvedCounts AFTER the buffer flush (otherwise the
	// flush at the end of runFullPasses overwrites our updates).
	aggregated := make(map[string]int)
	for _, m := range unresolvedPerFile {
		for caller, count := range m {
			aggregated[caller] += count
		}
	}
	p.unresolvedCallCounts = aggregated
	slog.Info("pass3.unresolved_counts.staged",
		"callers_with_unresolved", len(aggregated))
}

// passWriteUnresolvedCounts writes the unresolved_call_count property to
// each caller's Function/Method node. Runs after the graph buffer flush
// so the writes survive — earlier writes via passCalls were being
// clobbered by the bulk node-upsert phase that happens during flush.
func (p *Pipeline) passWriteUnresolvedCounts() {
	if len(p.unresolvedCallCounts) == 0 {
		return
	}
	written := 0
	missing := 0
	errs := 0
	for callerQN, count := range p.unresolvedCallCounts {
		if count <= 0 {
			continue
		}
		rows, err := p.Store.SetNodeIntProperty(p.ProjectName, callerQN, "unresolved_call_count", count)
		switch {
		case err != nil:
			errs++
		case rows > 0:
			written++
		default:
			missing++
		}
	}
	slog.Info("pass.unresolved_counts.write",
		"total", len(p.unresolvedCallCounts),
		"written", written, "node_missing", missing, "errors", errs)
}

// flushResolvedEdges converts QN-based resolved edges to ID-based edges and batch-inserts them.
func (p *Pipeline) flushResolvedEdges(results [][]resolvedEdge) {
	qnSet, totalEdges := collectEdgeQNs(results)
	if totalEdges == 0 {
		return
	}

	// Batch resolve all QNs to IDs
	qns := make([]string, 0, len(qnSet))
	for qn := range qnSet {
		qns = append(qns, qn)
	}
	qnToID, err := p.findNodeIDsByQNs(p.ProjectName, qns)
	if err != nil {
		slog.Warn("pass3.resolve_ids.err", "err", err)
		return
	}

	// Create stub nodes for LSP-resolved targets that don't exist in the graph.
	stubQNs := p.createLSPStubNodes(results, qnToID)

	// Fetch labels so we can filter CALLS edges whose target isn't a Function
	// or Method. Rust/TypeScript resolvers occasionally land on Variable nodes
	// generated from config files (diesel.toml, Cargo.toml) — those aren't
	// callable and inflate false-positive rate (2026-04-24 incident: 38% of
	// CALLS targeted Variable, 0% of TPs did). Stub nodes get labels Function
	// or Method by construction, so the filter lets them through.
	labels, err := p.findNodeLabelsByQNs(p.ProjectName, qns)
	if err != nil {
		slog.Warn("pass3.resolve_labels.err", "err", err)
		labels = map[string]string{}
	}

	// Build and insert edges
	edges := buildEdgesFromResults(results, qnToID, labels, p.ProjectName, totalEdges, stubQNs)
	if err := p.insertEdgeBatch(edges); err != nil {
		slog.Warn("pass3.batch_edges.err", "err", err)
	}
}

// collectEdgeQNs collects all unique qualified names and counts total edges from results.
func collectEdgeQNs(results [][]resolvedEdge) (qnSet map[string]struct{}, totalEdges int) {
	qnSet = make(map[string]struct{})
	for _, fileEdges := range results {
		for _, re := range fileEdges {
			qnSet[re.CallerQN] = struct{}{}
			qnSet[re.TargetQN] = struct{}{}
			totalEdges++
		}
	}
	return qnSet, totalEdges
}

// createLSPStubNodes creates stub nodes for LSP-resolved targets that don't exist in the graph.
// This happens for stdlib/external methods (e.g., context.Context.Done) that
// the LSP resolver correctly identifies but aren't indexed as nodes.
//
// Returns the set of stub-target QNs so buildEdgesFromResults can upgrade
// edges pointing at stubs to CALLS_EXTERNAL — this separates real-to-real
// call edges from real-to-external-stub edges in the graph.
func (p *Pipeline) createLSPStubNodes(results [][]resolvedEdge, qnToID map[string]int64) map[string]bool {
	var stubs []*store.Node
	stubQNs := make(map[string]bool)
	for _, fileEdges := range results {
		for _, re := range fileEdges {
			if _, ok := qnToID[re.TargetQN]; ok {
				continue
			}
			if stubQNs[re.TargetQN] {
				continue
			}
			strategy, _ := re.Properties["resolution_strategy"].(string)
			if !strings.HasPrefix(strategy, "lsp_") {
				continue
			}
			stubQNs[re.TargetQN] = true
			name := re.TargetQN
			if idx := strings.LastIndex(name, "."); idx >= 0 {
				name = name[idx+1:]
			}
			label := "Function"
			if strings.Count(re.TargetQN, ".") >= 2 {
				label = "Method"
			}
			stubs = append(stubs, &store.Node{
				Project:       p.ProjectName,
				Label:         label,
				Name:          name,
				QualifiedName: re.TargetQN,
				Properties:    map[string]any{"stub": true, "source": "lsp_resolution"},
			})
		}
	}
	if len(stubs) > 0 {
		stubIDs, err := p.upsertNodeBatch(stubs)
		if err != nil {
			slog.Warn("pass3.stub_nodes.err", "err", err)
		} else {
			for qn, id := range stubIDs {
				qnToID[qn] = id
			}
			slog.Info("pass3.stub_nodes", "count", len(stubs))
		}
	}
	return stubQNs
}

// buildEdgesFromResults converts QN-based resolved edges to store.Edge using the QN-to-ID map.
//
// Two CALLS-edge classification axes apply here:
//
//   1. stubQNs marks targets that were synthesized as LSP-resolved external
//      stubs (stdlib, vendored grammars, CGO targets). CALLS edges pointing
//      at stubs are upgraded to CALLS_EXTERNAL so downstream consumers can
//      opt in/out of external-stub noise. CALLS_PSEUDO (synthetic
//      module-level caller, set at resolve time) keeps its tag because the
//      pseudo-caller property is the dominant signal — but `external`
//      goes in the properties for power-user filtering.
//
//   2. labels marks the target node's label. CALLS to non-callable
//      targets (Variable, Class, File) are re-typed as INDIRECT_CALLS
//      rather than dropped — these are indirect-dispatch call sites
//      (closures, function-pointer variables, stored callables) that users
//      still want visible in the graph but marked as indirect so they
//      don't pollute CALLS precision. `labels` can be nil to disable the
//      filter (back-compat for callers that don't fetch labels). Stubs
//      are always Function or Method, so they're not affected.
//
// Order: stub-target check first (CALLS → CALLS_EXTERNAL), then non-callable
// check (remaining CALLS → INDIRECT_CALLS). Stubs are Function/Method-labeled
// so they correctly pass the callable check; pseudo-callers retain PSEUDO.
func buildEdgesFromResults(results [][]resolvedEdge, qnToID map[string]int64, labels map[string]string, project string, totalEdges int, stubQNs map[string]bool) []*store.Edge {
	edges := make([]*store.Edge, 0, totalEdges)
	droppedNonCallable := 0
	indirectCalls := 0
	for _, fileEdges := range results {
		for _, re := range fileEdges {
			srcID, srcOK := qnToID[re.CallerQN]
			tgtID, tgtOK := qnToID[re.TargetQN]
			if !srcOK || !tgtOK {
				continue
			}

			edgeType := re.Type
			// resolverRuleUpgrade tracks whether the modal-classification
			// override (Step 4, 2026-05-02 plateau-2) needs to overwrite
			// the resolver_rule property. Set when CALLS upgrades to
			// CALLS_EXTERNAL on a stub target. CALLS_PSEUDO already has
			// modal-pseudo set at resolve time.
			var resolverRuleUpgrade string

			// Stub-target classification (CALLS_EXTERNAL upgrade).
			if (edgeType == "CALLS" || edgeType == "CALLS_PSEUDO") && stubQNs[re.TargetQN] {
				if edgeType == "CALLS" {
					edgeType = "CALLS_EXTERNAL"
					resolverRuleUpgrade = ResolverRuleModalExternal
				}
				// CALLS_PSEUDO + stub target: keep PSEUDO as dominant tag,
				// flag external in properties below.
			}

			// Non-callable target classification (INDIRECT_CALLS retype).
			// Only applies to remaining CALLS edges — CALLS_EXTERNAL targets
			// are stubs (Function/Method by construction), CALLS_PSEUDO
			// keeps its tag.
			if edgeType == "CALLS" && labels != nil {
				tgtLabel, have := labels[re.TargetQN]
				// Class targets are direct constructor calls (e.g. Python
				// `AppContext(self)`, Rust struct construction surfaced
				// through register paths). PyCG records these as CALLS
				// to the class; demoting to INDIRECT_CALLS loses those
				// edges against the oracle. Variable/File/Module targets
				// remain indirect-dispatch and get the demotion.
				if have && tgtLabel != "Function" && tgtLabel != "Method" && tgtLabel != "Class" {
					edgeType = "INDIRECT_CALLS"
					indirectCalls++
				}
			}

			props := re.Properties
			if stubQNs[re.TargetQN] {
				if props == nil {
					props = map[string]any{}
				}
				props["external"] = true
			}
			if resolverRuleUpgrade != "" {
				if props == nil {
					props = map[string]any{}
				}
				props["resolver_rule"] = resolverRuleUpgrade
			}

			edges = append(edges, &store.Edge{
				Project:    project,
				SourceID:   srcID,
				TargetID:   tgtID,
				Type:       edgeType,
				Properties: props,
			})
		}
	}
	if droppedNonCallable > 0 {
		slog.Info("pass3.calls.filter_non_callable", "project", project, "dropped", droppedNonCallable)
	}
	if indirectCalls > 0 {
		slog.Info("pass3.calls.indirect_calls", "project", project, "count", indirectCalls)
	}
	return edges
}

// resolveCallWithTypes resolves a callee name using the registry, import maps,
// and type inference for method dispatch.
//
// Receiver-chain resolution: for a calleeName like `obj.field.method()`:
//  1. Look up `obj` in the per-function TypeMap (-> objType)
//  2. Walk intermediate field segments via p.fieldTypes:
//     fieldTypes[objType + "." + field] -> nextType, repeat
//  3. The final segment is the method name; look up `<finalType>.<method>`
//     in the registry.
//
// This catches the dominant Rust receiver-resolution failure pattern from
// the 2026-05-02 plateau-diagnose Step 6 sample (87% of assetman residual):
// `self.stage_repo.get_job_progress()`, `data.updater_client.cancel()`, and
// `service.call(req)` style chains where the receiver is a parameter or
// self-field rather than a `let x = Constructor()` local.
//
// callerQN is threaded through for the registry.Resolve consolidation
// (Phase 1, bench/research/registry-resolve-consolidation-plan.md). The
// chain-resolution logic doesn't consume it today; it flows into the
// CallContext passed to ResolveCtx so Phase 2+ can use it for
// receiver-type lookup against PerFuncTypeMap.
func (p *Pipeline) resolveCallWithTypes(
	calleeName, callerQN, moduleQN string,
	importMap map[string]string,
	typeMap TypeMap,
	language lang.Language,
) ResolutionResult {
	// ACC-006 (2026-05-03): strip the project's own crate-name prefix from
	// `::`-form callees so qualified-path calls like
	// `rust_futures_ready_negative::state::ready(5)` resolve through the
	// existing type_static_dispatch sibling-module path. Without this strip,
	// type_static_dispatch sees typeName="rust_futures_ready_negative" which
	// matches no internal class/module and returns empty (the project root
	// isn't registered as a Module). After strip, typeName="state" matches
	// r.modules and dispatch resolves. p.rustCrateMap is keyed by Rust
	// crate name (with `-` -> `_` normalized) and populated at pass2 by
	// scanning Cargo.toml.
	if strings.Contains(calleeName, "::") && len(p.rustCrateMap) > 0 {
		parts := strings.SplitN(calleeName, "::", 2)
		if len(parts) == 2 {
			if _, isOwnCrate := p.rustCrateMap[parts[0]]; isOwnCrate {
				calleeName = parts[1]
			}
		}
	}

	// ACC-008 (2026-05-03): normalize callee_name segment-by-segment.
	// Multi-line chain callees come through with embedded whitespace
	// (`obj\n    .method`); split-trim-rejoin gives clean dot-form input
	// for both the chain walker below AND the receiverType lookup at
	// line ~2017. Without this, both miss on multi-line chains.
	if strings.Contains(calleeName, ".") {
		segs := strings.Split(calleeName, ".")
		for i := range segs {
			segs[i] = strings.TrimSpace(segs[i])
		}
		calleeName = strings.Join(segs, ".")
	}

	// Multi-segment chain: a.b.c.method (where each `b`/`c` may be either
	// a struct field or a method call like `method_name(args)`). Walk via
	// fieldTypes (for fields) and returnTypes (for method calls).
	// Determines the FINAL receiver type so Tier 2 discrimination can
	// match against the chain target, not the root. Without this,
	// chain-dispatch calls like `data.updater_client.enqueue_asset_update`
	// drop in Tier 2 because root `data` has the wrong type for the
	// final method (data: AppContext, but method is on UpdaterClient).
	chainReceiverType := ""
	if strings.Contains(calleeName, ".") {
		parts := strings.Split(calleeName, ".")
		// Last segment is the method name; everything before is the receiver
		// expression.
		if len(parts) >= 2 {
			rootName := parts[0]
			fieldChain := parts[1 : len(parts)-1]
			methodName := parts[len(parts)-1]

			// Resolve the root identifier via the per-function TypeMap
			// (parameters, self, let-bindings).
			rootType, ok := typeMap[rootName]
			if ok && rootType != "" {
				currentType := rootType
				resolved := true
				// Walk the field/method chain. Field segments resolve via
				// fieldTypes; method-style segments (containing parens)
				// resolve via returnTypes after stripping the args.
				for _, segment := range fieldChain {
					// Method-style intermediate: `method_name(args...)`.
					// Strip the args to get the method name, look up
					// returnTypes[currentType.method].
					if idx := strings.IndexByte(segment, '('); idx >= 0 {
						methodOnly := segment[:idx]
						next, retOk := p.returnTypes[currentType+"."+methodOnly]
						if !retOk || next == "" {
							resolved = false
							break
						}
						currentType = next
						continue
					}
					// Field-style intermediate: lookup via fieldTypes.
					next, fieldOk := p.fieldTypes[currentType+"."+segment]
					if !fieldOk || next == "" {
						resolved = false
						break
					}
					currentType = next
				}
				if resolved {
					// Chain landed on a final receiver type. Try direct
					// candidate match first (high-confidence early return).
					candidate := currentType + "." + methodName
					// CG-2 (2026-05-06): only emit type_dispatch when
					// `currentType` is actually a registered class-like
					// type. Without this gate, the chain walker can
					// emit interface-dispatch edges for cases where
					// `currentType` resolved to e.g. a Module name that
					// happens to have a child with the same simple name
					// as the method — bypassing the
					// applyReceiverTypeFilter safety net used by
					// resolveViaNameLookup. The 2026-05-06 PSM baseline
					// shows interface-dispatch precision 0.81 (22 FPs
					// in 118 emissions). This gate is one of several
					// possible contributors; per-edge incident data
					// would be needed to attribute exact share.
					if p.registry.Exists(candidate) && p.registry.IsClassLike(currentType) {
						return ResolutionResult{
							QualifiedName:  candidate,
							Strategy:       "type_dispatch",
							Confidence:     0.90,
							CandidateCount: 1,
						}
					}
					// Direct match failed but the chain resolved to a
					// type. Pass that type to Tier 2 as the discrimination
					// receiver (overrides the root). Catches Trait-method
					// dispatches the direct candidate check misses.
					chainReceiverType = currentType
				}
			}

			// Tier-2 v0.1 (2026-05-24): external-crate chain root drop.
			// If the chain walker above didn't bottom out (rootType
			// unknown or chain walk failed) AND the chain root names
			// a static-call path into an external crate
			// (`<crate>::path::fn(...)`), mark the chain as external
			// and short-circuit: return an empty resolution with a
			// sentinel Strategy so the caller in pipeline_cbm.go
			// skips the fuzzy fallback. Without this drop the bare
			// callee (e.g. `get_result` from
			// `diesel::insert_into(...).get_result(conn)`) fuzzy-
			// matches into the only in-graph candidate
			// (AssetIntrospectImpl.get_result), the dominant FP
			// shape per PR #341.
			//
			// The check fires only when chainReceiverType is still
			// empty (the existing chain walker couldn't classify the
			// root). When externalCrates/workspaceMembers are nil
			// (non-Rust project, cargo unavailable, parse failure),
			// the check is a no-op and behavior is identical to
			// pre-v0.1.
			if chainReceiverType == "" && p.externalCrates != nil {
				if idx := strings.Index(rootName, "::"); idx >= 0 {
					rootCrate := rootName[:idx]
					if p.externalCrates[rootCrate] && !p.workspaceMembers[rootCrate] {
						slog.Debug("tier2.external_drop",
							"root_crate", rootCrate,
							"callee", calleeName,
							"caller", callerQN,
						)
						return ResolutionResult{
							Strategy: tier2ExternalDropStrategy,
						}
					}
				}
			}
		}
	}

	// Delegate to the registry's resolution strategy.
	// Phase 3a of the registry.Resolve consolidation: populate
	// ctx.ReceiverType from the per-function TypeMap so the registry's
	// Tier 2 discriminator can filter candidates by receiver-type
	// match. For a method-call shape `obj.method` (or
	// `obj.field.method`), the root identifier `obj` is the receiver;
	// its type comes from typeMap (parameter, self, or let-binding
	// captured by PR #149's PerFuncTypeMap).
	//
	// If chain resolution above succeeded, we returned early; reaching
	// here means the chain analysis couldn't land on a known method,
	// so the receiver type at the FINAL segment is unknown. The ROOT
	// receiver type is still useful as a discrimination signal: if the
	// receiver's known type is external (no internal methods match),
	// the registry's Tier 2 will drop the binding entirely instead of
	// falling through to a phantom-emitting bare-name suffix-match.
	// Receiver-type for Tier 2 discrimination. Prefer chainReceiverType
	// (computed by the chain walker above for multi-segment callees) over
	// the root receiver type. Without this preference, chain-dispatch
	// calls like `data.updater_client.method` drop in Tier 2 because
	// `data`'s type doesn't match the method's parent class.
	receiverType := chainReceiverType
	if receiverType == "" && strings.Contains(calleeName, ".") {
		rootName := strings.SplitN(calleeName, ".", 2)[0]
		if t, ok := typeMap[rootName]; ok && t != "" {
			receiverType = t
		}
	}
	// Phase 3b: include the per-module import-bindings map so the
	// registry's Tier 3 discriminator can drop bare-name candidates
	// for free-function calls whose name is bound by a `use` import
	// to an external (non-internal) target.
	importBindings := p.importBindings[moduleQN]
	return p.registry.ResolveCtx(CallContext{
		CalleeName:     calleeName,
		CallerQN:       callerQN,
		ModuleQN:       moduleQN,
		ImportMap:      importMap,
		ReceiverType:   receiverType,
		ImportBindings: importBindings,
		Language:       language,
	})
}

// buildFieldTypeMap walks every extracted file's TypeAssigns and identifies
// struct/enum field bindings (CBM emits these with `enclosing_func_qn` set
// to the struct's QN — distinguishable from local-variable bindings whose
// enclosing_func_qn is a Function/Method QN).
//
// Stores the result on p.fieldTypes for use by resolveCallWithTypes.
func (p *Pipeline) buildFieldTypeMap() {
	p.fieldTypes = make(FieldTypeMap)
	classLabels := map[string]bool{
		"Class": true, "Struct": true, "Type": true,
		"Interface": true, "Enum": true, "Trait": true,
	}
	for relPath, ext := range p.extractionCache {
		if ext == nil || ext.Result == nil {
			continue
		}
		moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
		importMap := p.importMaps[moduleQN]
		for _, ta := range ext.Result.TypeAssigns {
			if ta.VarName == "" || ta.TypeName == "" || ta.EnclosingFuncQN == "" {
				continue
			}
			// Distinguish field bindings from local-variable bindings by
			// checking the enclosing scope's label. Field bindings have
			// enclosing_func_qn pointing at a struct/enum; locals point at
			// a Function/Method.
			p.registry.mu.RLock()
			label, ok := p.registry.exact[ta.EnclosingFuncQN]
			p.registry.mu.RUnlock()
			if !ok || !classLabels[label] {
				continue
			}
			classQN := resolveAsClass(ta.TypeName, p.registry, moduleQN, importMap)
			if classQN == "" {
				continue
			}
			p.fieldTypes[ta.EnclosingFuncQN+"."+ta.VarName] = classQN
		}
	}
	if len(p.fieldTypes) > 0 {
		slog.Info("field_types.built", "entries", len(p.fieldTypes))
	}
}

// buildTraitImplMap walks every Rust file's CBM ImplTraits and records
// `trait_name -> [struct_name1, struct_name2, ...]`. Used by the
// resolver's trait-impl swap (preferImplOverTrait) to convert a resolved
// `Trait.method` target to `StructImpl.method` when exactly one impl
// pair exists with a same-named method.
func (p *Pipeline) buildTraitImplMap() {
	p.traitImpls = make(map[string][]string)
	for relPath, ext := range p.extractionCache {
		if ext == nil || ext.Result == nil || ext.Language != lang.Rust {
			continue
		}
		moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
		importMap := p.importMaps[moduleQN]
		for _, it := range ext.Result.ImplTraits {
			traitQN := resolveAsClass(it.TraitName, p.registry, moduleQN, importMap)
			if traitQN == "" {
				continue
			}
			structQN := resolveAsClass(it.StructName, p.registry, moduleQN, importMap)
			if structQN == "" {
				continue
			}
			// Avoid duplicate entries if the same impl pair appears in
			// multiple files (rare but possible with macro expansion).
			already := false
			for _, existing := range p.traitImpls[traitQN] {
				if existing == structQN {
					already = true
					break
				}
			}
			if !already {
				p.traitImpls[traitQN] = append(p.traitImpls[traitQN], structQN)
			}
			// Register the reverse direction on the FunctionRegistry so
			// applyReceiverTypeFilter can accept Trait-method candidates
			// when the receiver is a Struct that implements the Trait.
			p.registry.RegisterTraitImpl(structQN, traitQN)
		}
	}
	if len(p.traitImpls) > 0 {
		slog.Info("trait_impls.built", "traits", len(p.traitImpls))
	}
}

// preferImplOverTrait swaps a resolved `Trait.method` target to
// `StructImpl.method` when exactly one struct implements the trait AND
// that struct has the same method name in the registry. This matches
// oracle-rust-syn's convention of recording the impl method, not the
// trait method, for trait-method dispatch sites.
//
// Returns the (possibly swapped) target QN. Caller should use the
// return value as the edge target.
func (p *Pipeline) preferImplOverTrait(targetQN string) string {
	if targetQN == "" || len(p.traitImpls) == 0 {
		return targetQN
	}
	dotIdx := strings.LastIndex(targetQN, ".")
	if dotIdx <= 0 || dotIdx >= len(targetQN)-1 {
		return targetQN
	}
	parentQN := targetQN[:dotIdx]
	methodName := targetQN[dotIdx+1:]

	// Only swap when parent is a trait (Interface label). Don't swap when
	// parent is already a Struct/Class — caller already picked an impl.
	p.registry.mu.RLock()
	parentLabel := p.registry.exact[parentQN]
	p.registry.mu.RUnlock()
	if parentLabel != "Interface" {
		return targetQN
	}

	impls := p.traitImpls[parentQN]
	if len(impls) != 1 {
		// 0 impls: trait may be implemented externally; don't guess.
		// 2+ impls: ambiguous; oracle's pick depends on call-site type
		//          information we don't have.
		return targetQN
	}
	candidate := impls[0] + "." + methodName
	if p.registry.Exists(candidate) {
		return candidate
	}
	return targetQN
}

// frameworkDecoratorPrefixes are decorator prefixes that indicate a function
// is registered as an entry point by a framework (not dead code).
var frameworkDecoratorPrefixes = []string{
	// Python web frameworks (route handlers)
	"@app.get", "@app.post", "@app.put", "@app.delete", "@app.patch",
	"@app.route", "@app.websocket",
	"@router.get", "@router.post", "@router.put", "@router.delete", "@router.patch",
	"@router.route", "@router.websocket",
	"@blueprint.", "@api.", "@ns.",
	// Python middleware and exception handlers (framework-registered)
	"@app.middleware", "@app.exception_handler", "@app.on_event",
	// Testing frameworks
	"@pytest.fixture", "@pytest.mark",
	// CLI frameworks
	"@click.command", "@click.group",
	// Task/worker frameworks
	"@celery.task", "@shared_task", "@task",
	// Signal handlers
	"@receiver",
	// Rust Actix/Axum/Rocket route macros (#[get("/path")] → extracted as get("/path"))
	"get(", "post(", "put(", "delete(", "patch(", "head(", "options(",
	"route(", "connect(", "trace(",
}

// hasFrameworkDecorator returns true if any decorator matches a framework pattern.
func hasFrameworkDecorator(decorators []string) bool {
	for _, dec := range decorators {
		for _, prefix := range frameworkDecoratorPrefixes {
			if strings.HasPrefix(dec, prefix) {
				return true
			}
		}
	}
	return false
}

// resolvePythonRelativeImport handles Python relative-import syntax.
//
// CBM's extract_imports.c builds the ModulePath via
// `cbm_arena_sprintf("%s.%s", mod_path, name)` where mod_path is the
// text of tree-sitter's `relative_import` node and name is the imported
// symbol. This ALWAYS produces a "." separator between mod_path and
// name, which collides with the leading dots from the relative_import
// node text. So:
//
//   from . import sibling      → mod_path=".",      full="..sibling"
//   from .sub import helper    → mod_path=".sub",   full=".sub.helper"
//   from .. import top         → mod_path="..",     full="...top"
//   from ..top import x        → mod_path="..top",  full="..top.x"
//
// To recover semantic-leading-dots from raw-leading-dots, we use the
// fact that Python's imported `name` is always a single identifier
// (no internal dots): if the `rest` after stripping leading dots
// contains 0 dots, the source was an "all-dots mod_path" (dots only,
// no module after them) and the raw-dot-count is one MORE than the
// semantic dot count. If rest has internal dots, the source had
// `dots + module` and raw == semantic.
//
// Rules (CG-3):
//   - 1 semantic dot = current package (strip 1 segment from moduleQN)
//   - N semantic dots = strip N segments
//   - rest (the module portion if any, plus imported name) appended to parent
//   - if rest was just the imported name ("all-dots" case), localName is
//     appended to parent
//
// Returns the resolved target QN, or the original targetQN if no leading
// dot was present (no rewrite needed).
func resolvePythonRelativeImport(targetQN, moduleQN, localName string) string {
	rawDots := 0
	for rawDots < len(targetQN) && targetQN[rawDots] == '.' {
		rawDots++
	}
	if rawDots == 0 {
		return targetQN
	}
	rest := targetQN[rawDots:]
	// Recover semantic dot count: if rest has no internal dots, the
	// source was "from <dots> import <name>" (no module), raw = sem+1.
	// Otherwise raw = sem.
	semDots := rawDots
	allDotsMode := !strings.Contains(rest, ".")
	if allDotsMode {
		semDots = rawDots - 1
		if semDots < 1 {
			// Defensive: shouldn't happen — `from . import X` produces
			// raw=2, sem=1. Single raw dot would mean a flat absolute
			// path that started with a dot, which shouldn't occur.
			return targetQN
		}
	}
	parts := strings.Split(moduleQN, ".")
	if len(parts) < semDots {
		// More dots than the moduleQN can support — give up.
		return targetQN
	}
	parent := strings.Join(parts[:len(parts)-semDots], ".")
	if allDotsMode {
		// rest IS the imported name. Target is parent.<name>.
		return parent + "." + rest
	}
	// rest is "module.name" or "module.sub.name". Target QN keeps
	// everything; downstream suffix fallback strips trailing name to
	// find the module.
	return parent + "." + rest
}

// normalizePythonRelativeImports rewrites relative-import paths (`.util`,
// `..pkg`, etc.) in p.importMaps and p.importBindings to absolute QNs
// rooted at the importing module's parent package.
//
// Before this normalization (Phase B 2026-05-14), Python relative imports
// only got resolved when building IMPORTS edges in passImports. The
// resolver consulted the still-raw maps in passCalls, where leading-dot
// paths never matched any registered QN. applyImportBindingFilter would
// then misclassify the bare-name call as external and drop the call
// entirely — producing the requests-adversarial recall floor (5 of 10
// sampled FNs were `from .X import Y` cross-module calls per plan #523).
//
// The mutation is safe to call on absolute imports too:
// resolvePythonRelativeImport returns the input unchanged when no
// leading dot is present.
func (p *Pipeline) normalizePythonRelativeImports() {
	for moduleQN, importMap := range p.importMaps {
		for localName, targetQN := range importMap {
			resolved := resolvePythonRelativeImport(targetQN, moduleQN, localName)
			if resolved != targetQN {
				importMap[localName] = resolved
			}
		}
		bindings, ok := p.importBindings[moduleQN]
		if !ok {
			continue
		}
		for bareName, targetQN := range bindings {
			resolved := resolvePythonRelativeImport(targetQN, moduleQN, bareName)
			if resolved != targetQN {
				bindings[bareName] = resolved
			}
		}
	}
}

// passImports creates IMPORTS edges from the import maps built during pass 2.
func (p *Pipeline) passImports() {
	slog.Info("pass2b.imports")
	// Resolve Python relative imports (`.util` -> `<parent_pkg>.util`)
	// in p.importMaps and p.importBindings before any consumer reads
	// them. Without this, the resolver's applyImportBindingFilter sees
	// raw leading-dot paths and drops Python `from .X import Y` calls.
	p.normalizePythonRelativeImports()
	count := 0
	suffixHits := 0
	for moduleQN, importMap := range p.importMaps {
		moduleNode, _ := p.findNodeByQN(p.ProjectName, moduleQN)
		if moduleNode == nil {
			continue
		}
		for localName, targetQN := range importMap {
			// CG-3 normalization above mutated importMap in place;
			// this call is a no-op when targetQN has no leading dot.
			// Kept as defensive belt-and-suspenders for any code path
			// that bypasses normalizePythonRelativeImports.
			targetQN = resolvePythonRelativeImport(targetQN, moduleQN, localName)
			// Try to find the target as a Module node first
			targetNode, _ := p.findNodeByQN(p.ProjectName, targetQN)
			if targetNode == nil {
				// Try treating import path as a relative file path (e.g. "utils.mag", "lib/helpers.h")
				resolvedQN := fqn.ModuleQN(p.ProjectName, targetQN)
				if resolvedQN != targetQN {
					targetNode, _ = p.findNodeByQN(p.ProjectName, resolvedQN)
				}
			}
			if targetNode == nil {
				// Suffix fallback: nested packages. For a Python project laid
				// out as `src/flask/` (PEP 518 src-layout), `from flask.ctx
				// import X` extracts as targetQN=`flask.ctx.X` but the actual
				// node lives at `<project>.src.flask.ctx`. A prefix-only
				// resolver fails; suffix search finds it.
				//
				// Also handles Rust: the Rust extractor emits raw `use` paths
				// with `::` separators (e.g. `canstatd_types::CanError`).
				// Stored QNs use `.`, so we translate `::` -> `.` for the
				// suffix search. Mirrors the 2026-04-24 Python fix but for
				// the Rust import-path form.
				//
				// Constraints:
				//  - Module-only targets. Allowing Function/Class regressed
				//    mcp-servers IMPORTS precision by linking symbol imports
				//    to their definition (asymmetric with oracle's
				//    module-granularity emission).
				//  - Single match required — ambiguous suffixes shouldn't guess.
				//
				// Candidates, most specific first:
				//   `flask.ctx.X` -> `flask.ctx` (drop trailing segment)
				//   (Rust) `foo::bar::Baz` -> `foo.bar.Baz` -> `foo.bar`
				dotted := strings.ReplaceAll(targetQN, "::", ".")
				candidates := []string{dotted}
				if idx := strings.LastIndex(dotted, "."); idx > 0 {
					candidates = append(candidates, dotted[:idx])
				}
				// Rust crate-name substitution: if the first segment of the
				// dotted path matches a known crate in this workspace, also
				// try the form with that segment replaced by the crate's
				// actual directory path. `use canstatd_types::CanError` with
				// crate_map["canstatd_types"] = "types.src" yields candidate
				// `types.src.CanError`. Suffix-matched against the node
				// `<project>.types.src.lib.CanError` it finds a hit.
				if len(p.rustCrateMap) > 0 {
					// For `use crate_name` or `use crate_name::rest`, the
					// corresponding Module node is typically at
					// `<project>.<crate_path>.lib` (library crates) or
					// `<project>.<crate_path>.main` (binary crates) — since
					// the filename (lib.rs / main.rs) gets included in the QN.
					// We also try the crate_path as-is and with the remaining
					// :: path appended, to cover edge cases.
					var crateKey, restOfPath string
					if firstDot := strings.Index(dotted, "."); firstDot > 0 {
						crateKey = dotted[:firstDot]
						restOfPath = dotted[firstDot:] // includes leading dot
					} else {
						crateKey = dotted
						restOfPath = ""
					}
					if cratePath, ok := p.rustCrateMap[crateKey]; ok {
						// Prefer lib.rs / main.rs first — most imports target
						// the crate root which lives in one of these.
						candidates = append(candidates, cratePath+".lib"+restOfPath)
						candidates = append(candidates, cratePath+".main"+restOfPath)
						// Then the non-file variants.
						candidates = append(candidates, cratePath+restOfPath)
						if restOfPath != "" {
							// Strip trailing segment too.
							if idx := strings.LastIndex(cratePath+restOfPath, "."); idx > 0 {
								candidates = append(candidates, (cratePath + restOfPath)[:idx])
							}
						}
					}
				}
				// Label policy for suffix matches:
				//  - For Python/generic: Module-only (matches oracle form).
				//  - For Rust crate-resolved candidates (those containing a
				//    crate_map substitution): allow Module/Class/Struct/Enum/
				//    Function. `use foo::Bar` is commonly importing a type or
				//    function, not a module; the oracle doesn't measure Rust
				//    IMPORTS so there's no F1 cost to being more inclusive.
				for i, c := range candidates {
					hits, err := p.findNodesByQNSuffix(p.ProjectName, c)
					if err != nil || len(hits) == 0 {
						continue
					}
					isRustCrateResolved := i >= 2 // candidates[0..1] are Python-style; [2..] are crate-substituted
					// Filter by label. Rust crate-resolved allows symbols;
					// Python-style is Module-only to preserve module-granularity
					// matching against the ast oracle.
					var pick *store.Node
					for _, h := range hits {
						label := h.Label
						ok := false
						if isRustCrateResolved {
							ok = label == "Module" || label == "Class" || label == "Struct" ||
								label == "Enum" || label == "Function" || label == "Trait"
						} else {
							ok = label == "Module"
						}
						if !ok {
							continue
						}
						// Among eligible hits, prefer the SHORTEST QN — most
						// specific match to the suffix, least speculative.
						// `use foo::Bar` should resolve to the crate's root
						// `Bar`, not a deeply-nested re-export.
						if pick == nil || len(h.QualifiedName) < len(pick.QualifiedName) {
							pick = h
						}
					}
					if pick == nil {
						continue
					}
					targetNode = pick
					suffixHits++
					break
				}
			}
			if targetNode == nil {
				logImportDrop(moduleQN, localName, targetQN)
				continue
			}
			_ = p.insertEdge(&store.Edge{
				Project:  p.ProjectName,
				SourceID: moduleNode.ID,
				TargetID: targetNode.ID,
				Type:     "IMPORTS",
				Properties: map[string]any{
					"alias": localName,
				},
			})
			count++
		}
	}
	slog.Info("pass2b.imports.done", "edges", count, "suffix_fallback_hits", suffixHits)
}

// passHTTPLinks runs the HTTP linker to discover cross-service HTTP calls.
func (p *Pipeline) passHTTPLinks() error {
	// Clean up stale Route/InfraFile nodes and HTTP_CALLS/HANDLES/ASYNC_CALLS edges before re-running
	_ = p.Store.DeleteNodesByLabel(p.ProjectName, "Route")
	_ = p.Store.DeleteNodesByLabel(p.ProjectName, "InfraFile")
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "HTTP_CALLS")
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "HANDLES")
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "ASYNC_CALLS")

	// Index infrastructure files (Dockerfiles, compose, cloudbuild, .env)
	p.passInfraFiles()

	// Scan config files for env var URLs and create synthetic Module nodes
	envBindings := ScanProjectEnvURLs(p.RepoPath)
	if len(envBindings) > 0 {
		p.injectEnvBindings(envBindings)
	}

	linker := httplink.New(p.Store, p.ProjectName)

	// Feed InfraFile environment URLs into the HTTP linker
	infraSites := p.extractInfraCallSites()
	if len(infraSites) > 0 {
		linker.AddCallSites(infraSites)
		slog.Info("pass4.infra_callsites", "count", len(infraSites))
	}

	links, err := linker.Run()
	if err != nil {
		return err
	}
	slog.Info("pass4.httplinks", "links", len(links))
	return nil
}

// extractInfraCallSites extracts URL values from InfraFile environment properties
// and converts them to HTTPCallSite entries for the HTTP linker.
func (p *Pipeline) extractInfraCallSites() []httplink.HTTPCallSite {
	infraNodes, err := p.Store.FindNodesByLabel(p.ProjectName, "InfraFile")
	if err != nil {
		return nil
	}

	var sites []httplink.HTTPCallSite
	for _, node := range infraNodes {
		// InfraFile nodes use different property keys depending on source:
		// compose files: "environment", Dockerfiles/shell/.env: "env_vars",
		// cloudbuild: "deploy_env_vars"
		for _, envKey := range []string{"environment", "env_vars", "deploy_env_vars"} {
			sites = append(sites, extractEnvURLSites(node, envKey)...)
		}
	}
	return sites
}

// extractEnvURLSites extracts HTTP call sites from a single env property of an InfraFile node.
func extractEnvURLSites(node *store.Node, propKey string) []httplink.HTTPCallSite {
	env, ok := node.Properties[propKey]
	if !ok {
		return nil
	}

	// env_vars are stored as map[string]string (from Go), but after JSON round-trip
	// through SQLite they come back as map[string]any.
	var sites []httplink.HTTPCallSite
	switch envMap := env.(type) {
	case map[string]any:
		for _, val := range envMap {
			valStr, ok := val.(string)
			if !ok {
				continue
			}
			sites = append(sites, urlSitesFromValue(node, valStr)...)
		}
	case map[string]string:
		for _, valStr := range envMap {
			sites = append(sites, urlSitesFromValue(node, valStr)...)
		}
	}
	return sites
}

// urlSitesFromValue extracts URL paths from a string value and creates HTTPCallSite entries.
func urlSitesFromValue(node *store.Node, val string) []httplink.HTTPCallSite {
	if !strings.Contains(val, "http://") && !strings.Contains(val, "https://") && !strings.HasPrefix(val, "/") {
		return nil
	}

	paths := httplink.ExtractURLPaths(val)
	sites := make([]httplink.HTTPCallSite, 0, len(paths))
	for _, path := range paths {
		sites = append(sites, httplink.HTTPCallSite{
			Path:                path,
			SourceName:          node.Name,
			SourceQualifiedName: node.QualifiedName,
			SourceLabel:         "InfraFile",
		})
	}
	return sites
}

// injectEnvBindings creates or updates Module nodes for config files that contain
// environment variable URL bindings. These synthetic constants feed into the
// HTTP linker's call site discovery.
func (p *Pipeline) injectEnvBindings(bindings []EnvBinding) {
	byFile := make(map[string][]EnvBinding)
	for _, b := range bindings {
		byFile[b.FilePath] = append(byFile[b.FilePath], b)
	}

	count := 0
	for filePath, fileBindings := range byFile {
		moduleQN := fqn.ModuleQN(p.ProjectName, filePath)
		constants := buildConstantsList(fileBindings)

		if p.mergeWithExistingModule(moduleQN, constants) {
			count += len(fileBindings)
			continue
		}

		_, _ = p.Store.UpsertNode(&store.Node{
			Project:       p.ProjectName,
			Label:         "Module",
			Name:          filepath.Base(filePath),
			QualifiedName: moduleQN,
			FilePath:      filePath,
			Properties:    map[string]any{"constants": constants},
		})
		count += len(fileBindings)
	}

	if count > 0 {
		slog.Info("envscan.injected", "bindings", count, "files", len(byFile))
	}
}

// buildConstantsList converts env bindings to "KEY = VALUE" constant strings, capped at 50.
func buildConstantsList(bindings []EnvBinding) []string {
	constants := make([]string, 0, len(bindings))
	for _, b := range bindings {
		constants = append(constants, b.Key+" = "+b.Value)
	}
	if len(constants) > 50 {
		constants = constants[:50]
	}
	return constants
}

// mergeWithExistingModule merges new constants into an existing Module node's constant list.
// Returns true if the module existed and was updated.
func (p *Pipeline) mergeWithExistingModule(moduleQN string, constants []string) bool {
	existing, _ := p.Store.FindNodeByQN(p.ProjectName, moduleQN)
	if existing == nil {
		return false
	}
	existConsts, ok := existing.Properties["constants"].([]any)
	if !ok {
		return false
	}
	seen := make(map[string]bool, len(existConsts))
	for _, c := range existConsts {
		if s, ok := c.(string); ok {
			seen[s] = true
		}
	}
	for _, c := range constants {
		if !seen[c] {
			existConsts = append(existConsts, c)
		}
	}
	if existing.Properties == nil {
		existing.Properties = map[string]any{}
	}
	existing.Properties["constants"] = existConsts
	_, _ = p.Store.UpsertNode(existing)
	return true
}

// jsonURLKeyPattern matches JSON keys that likely contain URL/endpoint values.
var jsonURLKeyPattern = regexp.MustCompile(`(?i)(url|endpoint|base_url|host|api_url|service_url|target_url|callback_url|webhook|href|uri|address|server|origin|proxy|redirect|forward|destination)`)

// processJSONFile extracts URL-related string values from JSON config files.
// Uses a key-pattern allowlist to avoid flooding constants with noise.
func (p *Pipeline) processJSONFile(f discover.FileInfo) error {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}

	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("json parse: %w", err)
	}

	var constants []string
	extractJSONURLValues(parsed, "", &constants, 0)

	if len(constants) == 0 {
		return nil
	}

	// Cap at 20 constants per JSON file
	if len(constants) > 20 {
		constants = constants[:20]
	}

	moduleQN := fqn.ModuleQN(p.ProjectName, f.RelPath)
	err = p.upsertNode(&store.Node{
		Project:       p.ProjectName,
		Label:         "Module",
		Name:          filepath.Base(f.RelPath),
		QualifiedName: moduleQN,
		FilePath:      f.RelPath,
		Properties:    map[string]any{"constants": constants},
	})
	return err
}

// extractJSONURLValues recursively extracts key=value pairs from JSON where
// the key matches the URL key pattern or the value looks like a URL/path.
func extractJSONURLValues(v any, key string, out *[]string, depth int) {
	if depth > 20 {
		return
	}

	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			extractJSONURLValues(child, k, out, depth+1)
		}
	case []any:
		for _, child := range val {
			extractJSONURLValues(child, key, out, depth+1)
		}
	case string:
		if key == "" || val == "" {
			return
		}
		// Include if key matches URL pattern
		if jsonURLKeyPattern.MatchString(key) {
			*out = append(*out, key+" = "+val)
			return
		}
		// Include if value looks like a URL or API path
		if looksLikeURL(val) {
			*out = append(*out, key+" = "+val)
		}
	}
}

// looksLikeURL returns true if s appears to be a URL or API path.
func looksLikeURL(s string) bool {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return true
	}
	// Path starting with /api/ or containing at least 2 segments
	if strings.HasPrefix(s, "/") && strings.Count(s, "/") >= 2 {
		// Skip version-like paths: /1.0.0, /v2, /en
		seg := strings.TrimPrefix(s, "/")
		return len(seg) > 3
	}
	return false
}

// safeRowToLine converts a tree-sitter row (uint) to a 1-based line number (int).
// Returns math.MaxInt if the value would overflow.
// stripBOM removes a UTF-8 BOM (0xEF 0xBB 0xBF) from the start of source.
// Common in C# and Windows-generated files; tree-sitter may choke on BOM bytes.
func stripBOM(source []byte) []byte {
	if len(source) >= 3 && source[0] == 0xEF && source[1] == 0xBB && source[2] == 0xBF {
		return source[3:]
	}
	return source
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := xxh3.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
