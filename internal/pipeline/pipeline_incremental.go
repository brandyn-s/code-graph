// Incremental indexing: change classification, dependent expansion, hash bookkeeping.
//
// Split from pipeline.go without behaviour changes.
package pipeline

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"

	"github.com/brandyn-s/code-graph/internal/cbm"
	"github.com/brandyn-s/code-graph/internal/config"
	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/fqn"
	"github.com/brandyn-s/code-graph/internal/store"
	"golang.org/x/sync/errgroup"
)

// classifyFiles splits files into changed, unchanged, and deleted based on
// stored hashes. Deleted paths are returned as RelPath-only FileInfo values so
// dependency discovery can inspect their still-live graph nodes before the
// incremental writer removes them.
// Uses stat (mtime+size) as a fast pre-filter: files whose mtime and size match
// the stored values are assumed unchanged without reading/hashing. Only files
// with changed stat (or missing from the store) are hashed.
func (p *Pipeline) classifyFiles(files []discover.FileInfo) (changed, unchanged, deleted []discover.FileInfo) {
	storedHashes, err := p.Store.GetFileHashes(p.ProjectName)
	if err != nil || len(storedHashes) == 0 {
		return files, nil, nil // no hashes → full index
	}
	currentPaths := make(map[string]struct{}, len(files))
	for _, f := range files {
		currentPaths[f.RelPath] = struct{}{}
	}
	for relPath := range storedHashes {
		if _, ok := currentPaths[relPath]; !ok {
			deleted = append(deleted, discover.FileInfo{RelPath: relPath})
		}
	}
	sort.Slice(deleted, func(i, j int) bool { return deleted[i].RelPath < deleted[j].RelPath })

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
		return changed, unchanged, deleted // nothing to hash
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
	return changed, unchanged, deleted
}

// findCallerOfTargetDependents returns unchanged files containing call
// sites whose target nodes live in changed files. Complements
// findDependentFiles (which walks the import graph one hop) by directly
// querying existing call/usage edge tables for callers
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
		[]string{"CALLS", "USAGE", "HTTP_CALLS", "ASYNC_CALLS", "INDIRECT_CALLS"},
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
	raw := config.Get(config.IncrementalCap)
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
	// Use the same QN-to-ID classification and property-preserving writer as
	// the full path. Direct incremental inserts previously bypassed callable
	// target classification and made the two index modes semantically diverge.
	results := make([][]resolvedEdge, 0, len(files))
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
		results = append(results, edges)
		// Release Definitions/Imports per-file after call resolution
		if ext.Result != nil {
			ext.Result.Definitions = nil
			ext.Result.Imports = nil
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
	p.flushResolvedEdges(results)
	for callerQN, count := range aggregatedUnresolved {
		_, _ = p.Store.SetNodeIntProperty(p.ProjectName, callerQN, "unresolved_call_count", count)
	}
}

// hydrateResolutionFiles ensures every changed/dependent file has current CBM
// extraction plus import maps/bindings before incremental calls and imports are
// resolved. It is idempotent for changed files already parsed by passDefinitions.
func (p *Pipeline) hydrateResolutionFiles(files []discover.FileInfo) {
	for _, f := range files {
		if _, ok := p.extractionCache[f.RelPath]; ok {
			continue
		}
		parsed := cbmParseFile(p.ProjectName, f)
		if parsed == nil || parsed.Err != nil || parsed.CBMResult == nil {
			continue
		}
		p.extractionCache[f.RelPath] = &cachedExtraction{
			Result: parsed.CBMResult, Language: f.Language,
		}
		moduleQN := fqn.ModuleQN(p.ProjectName, f.RelPath)
		if len(parsed.ImportMap) > 0 {
			p.importMaps[moduleQN] = parsed.ImportMap
		}
		if len(parsed.ImportBindings) > 0 {
			p.importBindings[moduleQN] = parsed.ImportBindings
		}
	}
	// Reapply the same normalizations the full pass performs before imports and
	// calls consume these maps.
	p.normalizePythonRelativeImports()
	p.normalizeJSTSRelativeImports()
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
