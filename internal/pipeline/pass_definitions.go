// Definitions and calls passes: symbol registry, call resolution, edge flushing.
//
// Split from pipeline.go without behaviour changes.
package pipeline

import (
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brandyn-s/code-graph/internal/cbm"
	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/fqn"
	"github.com/brandyn-s/code-graph/internal/lang"
	"github.com/brandyn-s/code-graph/internal/store"
	"golang.org/x/sync/errgroup"
)

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
	var progressMu sync.Mutex
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
			progressMu.Lock()
			if pct > lastReportedPct && pct <= 20 {
				lastReportedPct = pct
				p.reportProgress("definitions:parse", pct, fmt.Sprintf("%d/%d files parsed", done, totalFiles))
			}
			progressMu.Unlock()
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
	var progressMu sync.Mutex
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
			progressMu.Lock()
			if pct > lastCallPct && pct <= 55 {
				lastCallPct = pct
				p.reportProgress("calls:resolve", pct, fmt.Sprintf("%d/%d files resolved", done, totalCallFiles))
			}
			progressMu.Unlock()
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
//  1. stubQNs marks targets that were synthesized as LSP-resolved external
//     stubs (stdlib, vendored grammars, CGO targets). CALLS edges pointing
//     at stubs are upgraded to CALLS_EXTERNAL so downstream consumers can
//     opt in/out of external-stub noise. CALLS_PSEUDO (synthetic
//     module-level caller, set at resolve time) keeps its tag because the
//     pseudo-caller property is the dominant signal — but `external`
//     goes in the properties for power-user filtering.
//
//  2. labels marks the target node's label. CALLS to non-callable
//     targets (Variable, Class, File) are re-typed as INDIRECT_CALLS
//     rather than dropped — these are indirect-dispatch call sites
//     (closures, function-pointer variables, stored callables) that users
//     still want visible in the graph but marked as indirect so they
//     don't pollute CALLS precision. `labels` can be nil to disable the
//     filter (back-compat for callers that don't fetch labels). Stubs
//     are always Function or Method, so they're not affected.
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
