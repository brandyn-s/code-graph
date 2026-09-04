// Pass orchestration: full, incremental, and post-flush pass sequencing.
//
// Split from pipeline.go without behaviour changes.
package pipeline

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/brandyn-s/code-graph/internal/config"
	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/store"
)

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
	} else {
		discoverOpts.MaxFileSize = fullModeMaxFileSize()
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

	nc, ncErr := p.Store.CountNodes(p.ProjectName)
	ec, ecErr := p.Store.CountEdges(p.ProjectName)
	if ncErr != nil || ecErr != nil {
		// A failed count must not masquerade as a real zero. Silently
		// discarding these errors is what hid the 2026-06-11 evictor
		// incident (store *sql.DB closed mid-index → "sql: database is
		// closed" → responses reported nodes=0/edges=0 with no signal).
		slog.Warn("pipeline.count.err", "project", p.ProjectName,
			"node_err", ncErr, "edge_err", ecErr)
	}
	p.LastNodeCount = nc
	p.LastEdgeCount = ec
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
	changed, unchanged, deleted := p.classifyFiles(files)
	p.LastIndexDelta = IndexDelta{
		FilesDiscovered: len(files),
		FilesChanged:    len(changed),
		FilesDeleted:    len(deleted),
		FilesUnchanged:  len(unchanged),
	}

	// If all files are changed (first index or no hashes), do full pass
	isFullIndex := len(unchanged) == 0
	if isFullIndex {
		p.LastIndexDelta.Mode = "full"
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
			p.LastIndexDelta.Mode = "full"
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

	slog.Info("incremental.classify", "changed", len(changed), "deleted", len(deleted), "unchanged", len(unchanged), "total", len(files))

	// Fast path: nothing changed → skip all heavy passes
	if len(changed) == 0 && len(deleted) == 0 {
		p.LastIndexDelta.Mode = "noop"
		slog.Info("incremental.noop", "reason", "no_changes")
		return false, nil
	}
	p.LastIndexDelta.Mode = "incremental"

	if err := p.runIncrementalPasses(files, changed, unchanged, deleted); err != nil {
		return true, err
	}
	_ = p.Store.IncrementIncrementalsSinceFull(p.ProjectName)
	return true, nil
}

// fullReindexEvery resolves the periodic-full-reindex threshold from
// CODE_GRAPH_FULL_REINDEX_EVERY. Default 50; 0 (or unparseable) disables
// the sentinel and lets the incremental path run indefinitely.
func fullReindexEvery() int {
	raw := config.Get(config.FullReindexEvery)
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

	// SCIP ingest (opt-in, CBM_SCIP_INDEX_PATH) replaces heuristic CALLS
	// edges with compiler-grade ones BEFORE tests/communities so the
	// downstream passes cluster and link over the corrected edge set.
	p.reportProgress("scip_ingest", 74, "ingesting SCIP index (if configured)")
	t = time.Now()
	p.passSCIPIngest()
	slog.Info("pass.timing", "pass", "scip_ingest", "elapsed", time.Since(t))

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

// resolvableEdgeTypes lists every AST-derived edge type that passCalls* and
// passUsages* re-create, so incremental runs can drop them for the files
// about to be re-resolved.
var resolvableEdgeTypes = [...]string{
	"CALLS", "USAGE", "USES_TYPE", "THROWS", "RAISES", "READS", "WRITES", "CONFIGURES",
}

// deleteResolvableEdges removes the AST-derived outgoing edges of files that
// are about to be re-resolved.
func (p *Pipeline) deleteResolvableEdges(files []discover.FileInfo) {
	for _, f := range files {
		for _, edgeType := range resolvableEdgeTypes {
			_ = p.Store.DeleteEdgesBySourceFile(p.ProjectName, f.RelPath, edgeType)
		}
	}
}

// runIncrementalPasses re-indexes only changed files + their dependents.
func (p *Pipeline) runIncrementalPasses(
	allFiles []discover.FileInfo,
	changed, unchanged, deleted []discover.FileInfo,
) error {
	// Discover dependents while the previous graph is still intact. Deleting
	// changed-file nodes first also cascades the incoming CALLS/USAGE edges that
	// identify unchanged callers, making the invalidation set permanently too
	// small for this run.
	// Deleted paths remain valid invalidation targets until removeDeletedFiles
	// cascades their nodes and incoming edges. Including them here is what lets
	// a pure deletion or file rename re-resolve unchanged importers/callers.
	invalidationTargets := mergeFiles(changed, deleted)
	dependents := p.findDependentFiles(invalidationTargets, unchanged)
	callerDependents := p.findCallerOfTargetDependents(invalidationTargets, unchanged)
	filesToResolve := mergeFiles(changed, dependents)
	filesToResolve = mergeFiles(filesToResolve, callerDependents)
	slog.Info("incremental.resolve",
		"changed", len(changed),
		"deleted", len(deleted),
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

	// Remove stale nodes/edges for deleted files
	p.removeDeletedFiles(allFiles)

	// Delete nodes for changed files (will be re-created in pass 2)
	for _, f := range changed {
		_ = p.Store.DeleteNodesByFile(p.ProjectName, f.RelPath)
	}

	// Pass 1 runs after stale changed-file nodes are removed so it restores the
	// changed File node and containment edges. Structure upserts preserve
	// semantic Module nodes when index.ts/__init__.py intentionally share their
	// directory qualified name.
	if err := p.passStructure(allFiles); err != nil {
		return fmt.Errorf("pass1 structure: %w", err)
	}
	if err := p.checkCancel(); err != nil {
		return err
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
	// Dependents are extracted lazily below for call resolution. Their stored
	// IMPORTS edges can be cascade-deleted when a changed target module is
	// rebuilt, so their maps must be hydrated before passImports restores them.
	p.hydrateResolutionFiles(filesToResolve)
	p.passImports()
	if err := p.checkCancel(); err != nil {
		return err
	}

	// Delete edges for files being re-resolved (all AST-derived edge types)
	p.deleteResolvableEdges(filesToResolve)

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
	// Changed reader nodes were deleted above, so their READS_ENV edges were
	// removed by FK cascade. Preserve unchanged readers and upsert only the
	// changed keys; then remove any environment nodes left with no readers.
	p.passEnvVarNodes()
	_ = p.Store.DeleteOrphanNodesByLabel(p.ProjectName, "EnvVar")
	p.passImplements()
	p.passGitHistory()
	p.passOPALinker()
	p.passZenoh()
	p.passNixServices()

	// Re-embed nodes whose embeddings were destroyed by this run's
	// DeleteNodesByFile (node_embeddings cascades on node deletion) and
	// backfill any historical gaps. Missing-only, so cost tracks the change
	// set — see passEmbeddingsMissing for the decay incident this prevents.
	t := time.Now()
	p.passEmbeddingsMissing()
	slog.Info("pass.timing", "pass", "embeddings_missing", "elapsed", time.Since(t))

	p.updateFileHashes(allFiles)

	// Observability
	p.logEdgeCounts()

	// Record the enrichment version so index_status can detect stale enrichment.
	if err := p.Store.SetEnrichmentVersion(p.ProjectName, EnrichmentVersion); err != nil {
		slog.Warn("pipeline.enrichment_version.err", "err", err)
	}

	return nil
}
