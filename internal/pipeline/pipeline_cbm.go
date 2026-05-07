package pipeline

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/fqn"
	"github.com/DeusData/codebase-memory-mcp/internal/lang"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// cachedExtraction holds the CBM extraction result for a file.
// Replaces cachedAST for all post-definition passes.
type cachedExtraction struct {
	Result   *cbm.FileResult
	Language lang.Language
}

// cbmParseFile reads a file, calls cbm.ExtractFile(), and converts the
// result to the same parseResult format used by the batch write infrastructure.
// This replaces parseFileAST() — all AST walking happens in C.
func cbmParseFile(projectName string, f discover.FileInfo) *parseResult {
	source, cleanup, err := mmapFile(f.Path)
	if cleanup != nil {
		defer cleanup()
	}
	return cbmParseFileFromSource(projectName, f, source, err)
}

// cbmParseFileFromSource is like cbmParseFile but takes pre-read source data.
// Used by the producer-consumer pipeline where I/O and CPU are separated.
func cbmParseFileFromSource(projectName string, f discover.FileInfo, source []byte, readErr error) *parseResult {
	result := &parseResult{File: f}

	if readErr != nil {
		result.Err = readErr
		return result
	}

	// Strip UTF-8 BOM if present (common in C#/Windows-generated files)
	source = stripBOM(source)

	cbmResult, err := cbm.ExtractFile(source, f.Language, projectName, f.RelPath)
	if err != nil {
		slog.Warn("cbm.extract.err", "path", f.RelPath, "lang", f.Language, "err", err)
		result.Err = err
		return result
	}

	result.CBMResult = cbmResult

	moduleQN := fqn.ModuleQN(projectName, f.RelPath)

	// Module node
	moduleNode := &store.Node{
		Project:       projectName,
		Label:         "Module",
		Name:          filepath.Base(f.RelPath),
		QualifiedName: moduleQN,
		FilePath:      f.RelPath,
		Properties:    make(map[string]any),
	}
	result.Nodes = append(result.Nodes, moduleNode)

	// Convert CBM definitions to store.Node objects
	for i := range cbmResult.Definitions {
		node, edge := cbmDefToNode(&cbmResult.Definitions[i], projectName, moduleQN)
		result.Nodes = append(result.Nodes, node)
		result.PendingEdges = append(result.PendingEdges, edge)
	}

	// Enrich module node with properties from CBM result
	enrichModuleNodeCBM(moduleNode, cbmResult, result)

	// Build import map from CBM imports.
	//
	// Two maps come out of this loop:
	//   - importMap (existing): LocalName → ModulePath. For Rust this is
	//     full-path → full-path; for Python typically bare-alias → module.
	//     Used by resolveViaImportMap for prefix-resolution.
	//   - importBindings (Phase 3b): bareName → ModulePath. The bare name
	//     is the last `::` or `.` segment of LocalName. Used by Tier 3
	//     receiver-type discrimination for free-function calls — when
	//     `entry()` calls `ready(42)` after `use futures_util::future::ready;`,
	//     importBindings["ready"] = "futures_util::future::ready" and the
	//     resolver knows to drop internal `*::ready` candidates.
	if len(cbmResult.Imports) > 0 {
		importMap := make(map[string]string, len(cbmResult.Imports))
		importBindings := make(map[string]string, len(cbmResult.Imports))
		for _, imp := range cbmResult.Imports {
			if imp.LocalName == "" || imp.ModulePath == "" {
				continue
			}
			importMap[imp.LocalName] = imp.ModulePath
			bare := bareNameOfImport(imp.LocalName)
			if bare != "" {
				importBindings[bare] = imp.ModulePath
			}
		}
		result.ImportMap = importMap
		result.ImportBindings = importBindings
	}

	moduleNode.Properties["imports_count"] = cbmResult.ImportCount
	moduleNode.Properties["is_test"] = cbmResult.IsTestFile

	// exports: collect exported symbol names
	var exports []string
	for _, n := range result.Nodes {
		if n.QualifiedName == moduleQN {
			continue
		}
		if exp, ok := n.Properties["is_exported"].(bool); ok && exp {
			exports = append(exports, n.Name)
		}
	}
	if len(exports) > 0 {
		moduleNode.Properties["exports"] = exports
	}

	if symbols := buildSymbolSummary(result.Nodes, moduleQN); len(symbols) > 0 {
		moduleNode.Properties["symbols"] = symbols
	}

	return result
}

// cbmDefToNode converts a CBM Definition to a store.Node and its DEFINES/DEFINES_METHOD edge.
func cbmDefToNode(def *cbm.Definition, projectName, moduleQN string) (*store.Node, pendingEdge) {
	props := map[string]any{}

	if def.Signature != "" {
		props["signature"] = def.Signature
	}
	if def.ReturnType != "" {
		props["return_type"] = def.ReturnType
	}
	if def.Receiver != "" {
		props["receiver"] = def.Receiver
	}
	if def.Docstring != "" {
		props["docstring"] = def.Docstring
	}
	if len(def.Decorators) > 0 {
		props["decorators"] = def.Decorators
		if hasFrameworkDecorator(def.Decorators) {
			props["is_entry_point"] = true
		}
	}
	if len(def.BaseClasses) > 0 {
		props["base_classes"] = def.BaseClasses
	}
	if len(def.ParamNames) > 0 {
		props["param_names"] = def.ParamNames
	}
	if len(def.ParamTypes) > 0 {
		props["param_types"] = def.ParamTypes
	}
	if len(def.ReturnTypes) > 0 {
		props["return_types"] = def.ReturnTypes
	}
	if def.Complexity > 0 {
		props["complexity"] = def.Complexity
	}
	if def.Lines > 0 {
		props["lines"] = def.Lines
	}

	props["is_exported"] = def.IsExported

	if def.IsAbstract {
		props["is_abstract"] = true
	}
	if def.IsTest {
		props["is_test"] = true
	}
	if def.IsEntryPoint {
		props["is_entry_point"] = true
	}

	node := &store.Node{
		Project:       projectName,
		Label:         def.Label,
		Name:          def.Name,
		QualifiedName: def.QualifiedName,
		FilePath:      def.FilePath,
		StartLine:     def.StartLine,
		EndLine:       def.EndLine,
		Properties:    props,
	}

	// Determine edge type and source QN
	edgeType := "DEFINES"
	sourceQN := moduleQN
	if def.Label == "Method" || def.Label == "Field" {
		edgeType = "DEFINES_METHOD"
		if def.Label == "Field" {
			edgeType = "DEFINES_FIELD"
		}
		if def.ParentClass != "" {
			sourceQN = def.ParentClass
		}
	}

	edge := pendingEdge{
		SourceQN: sourceQN,
		TargetQN: def.QualifiedName,
		Type:     edgeType,
	}

	return node, edge
}

// enrichModuleNodeCBM populates module node properties from CBM extraction results.
func enrichModuleNodeCBM(moduleNode *store.Node, cbmResult *cbm.FileResult, _ *parseResult) {
	// Additional module-level properties can be added here if CBM exposes them
	// (e.g., macros, constants, global_vars from CBMFileResult)
}

// inferTypesCBM builds per-function TypeMaps from CBM TypeAssign data plus
// registry resolution. Replaces the 14 language-specific infer*Types().
//
// CBM emits one TypeAssign per binding with EnclosingFuncQN identifying the
// scope. Function-scoped bindings (EnclosingFuncQN is a Function/Method) go
// into the per-function TypeMap so callers in different functions don't
// collide on shared variable names like `data` or `service`. Class-scoped
// bindings (EnclosingFuncQN is a Struct/Enum) are skipped here — they're
// handled by the global p.fieldTypes map built in buildFieldTypeMap().
//
// Bindings with empty or unknown EnclosingFuncQN fall through to the
// module-scope map (key ""), used as a fallback for free-fn calls.
func inferTypesCBM(
	typeAssigns []cbm.TypeAssign,
	registry *FunctionRegistry,
	moduleQN string,
	importMap map[string]string,
) PerFuncTypeMap {
	out := make(PerFuncTypeMap)
	classLabels := map[string]bool{
		"Class": true, "Struct": true, "Type": true,
		"Interface": true, "Enum": true, "Trait": true,
	}

	for _, ta := range typeAssigns {
		if ta.VarName == "" || ta.TypeName == "" {
			continue
		}
		// Skip class-scoped (field) bindings — they're handled globally.
		registry.mu.RLock()
		label, hasLabel := registry.exact[ta.EnclosingFuncQN]
		registry.mu.RUnlock()
		if hasLabel && classLabels[label] {
			continue
		}

		// Rust constructor-call shape: the C extractor captures
		// `let x = Foo::new()` as TypeName="Foo::new" (the full
		// path expression). Strip the trailing `::method` segment for
		// well-known constructor methods so resolveAsClass can find
		// `Foo` in the registry. Without this, every let-bound
		// constructor binding was silently dropped — critical for
		// Phase 3a's receiver-type discrimination
		// (bench/research/registry-resolve-consolidation-plan.md).
		// Empirically discovered when Tier 2 didn't fire on the
		// rust-actix-data-negative fixture: TypeAssigns showed
		// `metrics` bound to `MetricsCollector::new`, not
		// `MetricsCollector`.
		typeName := stripRustConstructorSuffix(ta.TypeName)
		classQN := resolveAsClass(typeName, registry, moduleQN, importMap)

		key := ta.EnclosingFuncQN
		if out[key] == nil {
			out[key] = make(TypeMap)
		}
		if classQN != "" {
			out[key][ta.VarName] = classQN
			continue
		}

		// Phase 3c (external-type-receiver passthrough,
		// bench/research/registry-resolve-consolidation-plan.md):
		// when the type isn't a registered internal class, record the
		// raw type name instead of dropping the binding. This lets the
		// resolver's Tier 2 (applyReceiverTypeFilter) recognize the
		// receiver as external — no internal Method's parent will equal
		// the raw external name, so dropAll fires and the call is NOT
		// bound to a same-named internal Method via cross-package-
		// heuristic.
		//
		// Targets the rust-restate-chain-negative fixture:
		// `entry(ctx: Context)` where Context is external (restate_sdk).
		// Without this, `ctx.run(...)` and chain segments
		// `ctx.invocation().target().send()` bound to internal
		// Workflow.run / Invocation.target / Invocation.send via bare-
		// name suffix-match (3 phantoms).
		//
		// Risk: types whose label isn't in resolveAsClass's allowed set
		// (Trait, Macro, Variable) get recorded raw too. For Trait
		// receivers, Tier 2 will drop bindings that today suffix-match
		// to some random internal method — usually a phantom-removal
		// win, but if the trait's method is genuinely implemented by
		// the suffix-matched type, recall drops. Hard rollback at >5pp
		// real-fixture syn-oracle F1 drop applies.
		if typeName != "" {
			out[key][ta.VarName] = typeName
		}
	}

	return out
}

// rustConstructorMethods enumerates the trailing `::<method>` segments
// that the Rust extractor may capture in a let-binding's TypeName when
// the binding is `let x = Type::<method>(...)`. For these, the binding's
// type is `Type`, not `Type::<method>`. Conservative list — only the
// canonical "returns Self" idioms. UFCS method calls
// (`let x = Type::method(self, ...)`) where method is NOT a constructor
// would silently misbind here, but those are rare in practice and the
// alternative (no inference at all) costs more than the rare misbind.
var rustConstructorMethods = map[string]bool{
	"new":           true,
	"default":       true,
	"from":          true,
	"with_capacity": true,
	"from_str":      true,
	"build":         true,
	"empty":         true,
	"zero":          true,
}

func stripRustConstructorSuffix(typeName string) string {
	idx := strings.LastIndex(typeName, "::")
	if idx < 0 {
		return typeName
	}
	suffix := typeName[idx+2:]
	if rustConstructorMethods[suffix] {
		return typeName[:idx]
	}
	return typeName
}

// bareNameOfImport returns the bare (unqualified) name an import brings
// into scope. For Rust `use futures_util::future::ready;`, LocalName is
// the full `futures_util::future::ready` path and the bare name is
// `ready`. For Python `import foo` or `from x import bar`, LocalName is
// already the bare alias name. Splits on `::` and `.` and returns the
// last non-empty segment.
//
// Used by Phase 3b's Tier 3 discriminator
// (bench/research/registry-resolve-consolidation-plan.md): the registry
// maps the bare name to the full import target so it can drop internal
// candidates when the import points outside the project.
func bareNameOfImport(localName string) string {
	if localName == "" {
		return ""
	}
	// Rust path separator first.
	if idx := strings.LastIndex(localName, "::"); idx >= 0 {
		tail := localName[idx+2:]
		if tail != "" {
			localName = tail
		}
	}
	// Python / Go separator second (catches mixed forms too).
	if idx := strings.LastIndex(localName, "."); idx >= 0 {
		tail := localName[idx+1:]
		if tail != "" {
			localName = tail
		}
	}
	return localName
}

// resolveFileCallsCBM resolves all call targets using pre-extracted CBM data.
// Replaces resolveFileCalls() — no AST walking needed.
//
// Returns (edges, unresolvedByCaller). The latter maps each caller QN seen
// in CBM call sites to the count of calls that the resolver did NOT
// successfully emit. Used by passWriteUnresolvedCounts to set the
// unresolved_call_count property on Function/Method nodes for diagnostic
// queries (e.g., "find callers with missing call sites" — the plateau-
// diagnose Step 6 finding that 3 of 5 sampled FN callers had ZERO outbound
// edges due to silent resolver drops).
func (p *Pipeline) resolveFileCallsCBM(relPath string, ext *cachedExtraction) ([]resolvedEdge, map[string]int) {
	moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
	importMap := p.importMaps[moduleQN]

	// Cross-file LSP resolution for Go files
	p.runGoLSPCrossFileResolution(ext, moduleQN, relPath, importMap)

	// Build per-function type map from CBM type assignments.
	perFuncTypeMap := inferTypesCBM(ext.Result.TypeAssigns, p.registry, moduleQN, importMap)

	// LSP-resolved calls take priority (high confidence, type-aware).
	edges, lspCallerMethods := collectLSPResolvedEdges(ext.Result.ResolvedCalls, p.registry)

	// Track per-caller unresolved counts. CBM emits one Call entry per
	// detected call site. For each, we either emit a resolved edge (success)
	// or return (zero, false) (failure). The diagnostic counter records the
	// failure count per caller so consumers can detect "this fn body has
	// call sites but emits zero edges" via a node property check.
	unresolvedByCaller := make(map[string]int)

	// Resolve remaining calls via registry + fuzzy matching.
	// Each call's TypeMap is selected by its enclosing function so that
	// parameters / locals / `self` from one function don't shadow another's.
	for _, call := range ext.Result.Calls {
		callerQN := call.EnclosingFuncQN
		if callerQN == "" {
			callerQN = moduleQN
		}
		typeMap := perFuncTypeMap[call.EnclosingFuncQN]
		if typeMap == nil {
			typeMap = perFuncTypeMap[""]
		}
		if typeMap == nil {
			typeMap = TypeMap{}
		}
		if edge, ok := p.resolveCallEdge(call, moduleQN, importMap, typeMap, lspCallerMethods); ok {
			edges = append(edges, edge)
		} else {
			unresolvedByCaller[callerQN]++
		}
	}

	return edges, unresolvedByCaller
}

// runGoLSPCrossFileResolution re-runs LSP with cross-file definitions from imported packages.
func (p *Pipeline) runGoLSPCrossFileResolution(ext *cachedExtraction, moduleQN, relPath string, importMap map[string]string) {
	if ext.Language != lang.Go || p.goLSPIdx == nil {
		return
	}
	crossDefs := p.goLSPIdx.collectCrossFileDefs(importMap)
	if len(crossDefs) == 0 {
		return
	}
	source := readFileSource(p.RepoPath, relPath)
	if len(source) == 0 {
		return
	}
	fileDefs := cbm.DefsToLSPDefs(ext.Result.Definitions, moduleQN)
	resolved := cbm.RunGoLSPCrossFile(source, moduleQN, fileDefs, crossDefs, ext.Result.Imports)
	if len(resolved) > 0 {
		ext.Result.ResolvedCalls = resolved
	}
}

// collectLSPResolvedEdges converts LSP-resolved calls to edges and builds a caller+method dedup set.
//
// `registry` is consulted to classify the caller's AST scope kind for the
// `caller_node_kind` property (added 2026-05-02 plateau-2 plan, Step 3).
// LSP-resolved edges always carry edgeType="CALLS"; the kind branches on
// the caller's registry label. registry may be nil in tests; callers
// then receive CallerKindUnknown which is the safe fallback.
//
// resolver_rule (added Step 4, 2026-05-02 plateau-2 plan) is derived from
// the LSP strategy string via resolverRuleFromLSPStrategy. Modal upgrades
// (CALLS → CALLS_EXTERNAL on stub targets) happen later in
// buildEdgesFromResults and override the original rule there.
//
// candidate_set_size (added Step 5, 2026-05-02 plateau-2 plan) is the
// pre-tie-break candidate cardinality. LSP returns one definite target
// per call site without enumerating alternates, so LSP-resolved edges
// carry size=1 by definition (CandidateSetSizeLSPDefault). The Janusian
// signal lives in the registry/type-dispatch paths in resolveCallEdge.
// See candidate_set.go for the rationale.
func collectLSPResolvedEdges(resolvedCalls []cbm.ResolvedCall, registry *FunctionRegistry) (edges []resolvedEdge, lspCallerMethods map[string]bool) {
	lspResolved := make(map[string]bool)
	lspCallerMethods = make(map[string]bool)

	for _, rc := range resolvedCalls {
		if rc.CallerQN == "" || rc.CalleeQN == "" || rc.Confidence == 0 {
			continue
		}
		key := rc.CallerQN + "\x00" + rc.CalleeQN
		if lspResolved[key] {
			continue
		}
		lspResolved[key] = true
		edges = append(edges, resolvedEdge{
			CallerQN: rc.CallerQN,
			TargetQN: rc.CalleeQN,
			Type:     "CALLS",
			Properties: map[string]any{
				"confidence":               float64(rc.Confidence),
				"confidence_band":          confidenceBand(float64(rc.Confidence)),
				"resolution_strategy":      rc.Strategy,
				"caller_node_kind":         callerKindFromContext(rc.CallerQN, "CALLS", registry),
				"resolver_rule":            resolverRuleFromLSPStrategy(rc.Strategy),
				CandidateSetPropertyName:   CandidateSetSizeLSPDefault,
			},
		})

		shortName := rc.CalleeQN
		if idx := strings.LastIndex(shortName, "."); idx >= 0 {
			shortName = shortName[idx+1:]
		}
		lspCallerMethods[rc.CallerQN+"\x00"+shortName] = true
	}

	return edges, lspCallerMethods
}

// resolveCallEdge resolves a single call to an edge using the registry and type system.
func (p *Pipeline) resolveCallEdge(
	call cbm.Call, moduleQN string, importMap map[string]string,
	typeMap TypeMap, lspCallerMethods map[string]bool,
) (resolvedEdge, bool) {
	calleeName := call.CalleeName
	callerQN := call.EnclosingFuncQN
	if calleeName == "" {
		return resolvedEdge{}, false
	}
	// When the call site has no enclosing function (e.g. top-level package
	// init, vendored grammar entry, Python module body), default the caller
	// to the module-level node and tag the resulting edge as CALLS_PSEUDO.
	// CALLS_PSEUDO surfaces module-level invocations to power users while
	// keeping precision-relevant queries (default :CALLS) free of synthetic
	// callers. See bench/accuracy: removing CALLS_PSEUDO from accuracy
	// queries lifts raw-exact precision +20pp on the Go fixture.
	edgeType := "CALLS"
	if callerQN == "" {
		callerQN = moduleQN
		edgeType = "CALLS_PSEUDO"
	}

	// Skip if LSP already resolved this caller+method
	fuzzyShort := calleeName
	if idx := strings.LastIndex(fuzzyShort, "."); idx >= 0 {
		fuzzyShort = fuzzyShort[idx+1:]
	}
	if lspCallerMethods[callerQN+"\x00"+fuzzyShort] {
		return resolvedEdge{}, false
	}

	// caller_node_kind classifies the AST scope that emitted this CALLS
	// edge. Computed once per call site and attached to every edge this
	// function returns. CALLS_PSEUDO + module-defaulted callerQN maps to
	// CallerKindFileBlock; everything else routes through the registry
	// label lookup. See caller_kind.go for the decision rules.
	callerKind := callerKindFromContext(callerQN, edgeType, p.registry)

	// resolver_rule classifies WHICH resolver pathway emitted this edge.
	// CALLS_PSEUDO is the dominant modal classification — it overrides
	// any underlying registry/type-dispatch rule. Each non-pseudo emit
	// branch picks its own rule from the resolver-rule taxonomy in
	// resolver_rule.go. Step 4 of the 2026-05-02 plateau-2 plan.
	pseudoRule := edgeType == "CALLS_PSEUDO"

	// Python self.method() resolution
	if strings.HasPrefix(calleeName, "self.") {
		classQN := extractClassFromMethodQN(callerQN)
		if classQN != "" {
			candidate := classQN + "." + calleeName[5:]
			if p.registry.Exists(candidate) {
				rule := ResolverRuleSelfMethod
				if pseudoRule {
					rule = ResolverRuleModalPseudo
				}
				// self.method() is always a single-target resolution:
				// the callee is the unique class+method pair. Size=1.
				return resolvedEdge{
					CallerQN: callerQN,
					TargetQN: p.preferImplOverTrait(candidate),
					Type:     edgeType,
					Properties: map[string]any{
						"caller_node_kind":       callerKind,
						"resolver_rule":          rule,
						CandidateSetPropertyName: 1,
					},
				}, true
			}
		}
	}

	// Type-based method dispatch for qualified calls like obj.method()
	result := p.resolveCallWithTypes(calleeName, callerQN, moduleQN, importMap, typeMap)
	if result.QualifiedName == "" {
		// Phase 3a/b: route the fuzzy fallback through FuzzyResolveCtx
		// so Tier 2 (receiver-type) and Tier 3 (import-binding)
		// discrimination apply here too. Without this, the fuzzy path
		// silently bypasses both discriminators and re-emits phantoms
		// the upstream paths correctly dropped.
		fuzzyCtx := CallContext{
			CalleeName:     calleeName,
			CallerQN:       callerQN,
			ModuleQN:       moduleQN,
			ImportMap:      importMap,
			ImportBindings: p.importBindings[moduleQN],
		}
		if strings.Contains(calleeName, ".") {
			rootName := strings.SplitN(calleeName, ".", 2)[0]
			if t, ok := typeMap[rootName]; ok && t != "" {
				fuzzyCtx.ReceiverType = t
			}
		}
		if fuzzyResult, ok := p.registry.FuzzyResolveCtx(fuzzyCtx); ok && fuzzyResult.Confidence >= 0.10 {
			rule := ResolverRuleFuzzyResolve
			if pseudoRule {
				rule = ResolverRuleModalPseudo
			}
			return resolvedEdge{
				CallerQN: callerQN,
				TargetQN: p.preferImplOverTrait(fuzzyResult.QualifiedName),
				Type:     edgeType,
				Properties: map[string]any{
					"confidence":             fuzzyResult.Confidence,
					"confidence_band":        confidenceBand(fuzzyResult.Confidence),
					"resolution_strategy":    fuzzyResult.Strategy,
					"caller_node_kind":       callerKind,
					"resolver_rule":          rule,
					CandidateSetPropertyName: candidateSetSizeFromResolution(fuzzyResult),
				},
			}, true
		}
		return resolvedEdge{}, false
	}

	if result.Confidence < 0.10 {
		return resolvedEdge{}, false
	}
	rule := resolverRuleFromRegistryStrategy(result.Strategy)
	if pseudoRule {
		rule = ResolverRuleModalPseudo
	}

	// Rec 1 (2026-05-06): drop-on-no-match for the two LOW-PRECISION
	// cross-package sub-buckets (suffix-match fall-through and project-
	// wide unique-name lookup). Gated behind RESOLVER_DROP_LOOSE_CROSS_PACKAGE
	// so production behavior is unchanged when the env var is unset.
	//
	// The 2026-05-06 sub-bucket-split measurement (PR #234) found these
	// two buckets have catastrophic precision on Python adversarial
	// fixtures (0.00-0.35) but merely-poor on Go (0.48 on gin's suffix,
	// 0.88-0.95 on Go unique-name). Dropping unconditionally would
	// crater Go recall by 57-68%. The env-var gate lets the eval
	// harness apply the drop selectively (Python fixtures only) while
	// production behavior remains the recall-favorable status quo.
	//
	// Pseudo-caller edges are excluded from the drop — they're already
	// classified as ResolverRuleModalPseudo above, which is not in the
	// loose-cross-package set.
	if isLooseCrossPackageRule(rule) && os.Getenv("RESOLVER_DROP_LOOSE_CROSS_PACKAGE") != "" {
		return resolvedEdge{}, false
	}

	// Y.3 (2026-05-02 plateau-2 plan): Janusian-ambiguity penalty.
	// Step 6 baseline showed ambiguous-site (candidate_set_size>=2)
	// precision was 0.20 vs unambiguous 0.82 — a 62pp gap. Refuse to
	// emit ambiguous cross-package-heuristic edges: the resolver's
	// import-distance tie-break on 2+ candidates is unreliable
	// (60% of judged FPs were "same_named_method_disambiguation"
	// per the Step 2 LLM-Judge taxonomy). Pseudo-caller cases are
	// excluded — pkg_block_caller_FP_rate=0 in baseline.
	candSize := candidateSetSizeFromResolution(result)
	// Janusian penalty (#135) used to DROP cross-package-heuristic emissions
	// with candidate_set_size >= 2 entirely. The 2026-05-02 plateau-diagnose
	// session showed that drop trades 60% legitimate TPs for 40% FPs (per
	// the Step 2 LLM-Judge taxonomy in #135) — collectively responsible for
	// most of the recall regression from R=0.798 (stale) to R=0.685 (fresh)
	// on the psm Rust fixture.
	//
	// New behavior (2026-05-02 PR follow-up): emit the edge but tag it as
	// `confidence_band="speculative-janusian"` so downstream consumers can
	// filter ambiguous Janusian emissions out of high-precision use cases
	// while retaining them for recall-sensitive ones (impact analysis,
	// blast-radius queries, "find any plausible caller").
	//
	// The harness now reports both `scope_aligned` (all bands, includes
	// these speculative emissions) and `scope_aligned_high_confidence`
	// (excludes speculative-janusian) so the precision tier is preserved.
	// Janusian penalty fires on the cross-package family (any of the three
	// sub-buckets that the lumped legacy bucket was split into 2026-05-06).
	// Use the helper rather than enumerating sub-buckets here so future
	// additions to the family flow through automatically.
	janusianAmbiguous := isCrossPackageRule(rule) && candSize >= 2
	confBand := confidenceBand(result.Confidence)
	if janusianAmbiguous {
		confBand = "speculative-janusian"
	}
	props := map[string]any{
		"confidence":             result.Confidence,
		"confidence_band":        confBand,
		"resolution_strategy":    result.Strategy,
		"caller_node_kind":       callerKind,
		"resolver_rule":          rule,
		CandidateSetPropertyName: candSize,
	}
	if janusianAmbiguous {
		props["janusian_ambiguous"] = true
	}
	return resolvedEdge{
		CallerQN:   callerQN,
		TargetQN:   p.preferImplOverTrait(result.QualifiedName),
		Type:       edgeType,
		Properties: props,
	}, true
}

// resolveFileUsagesCBM resolves usage references using pre-extracted CBM data.
// Replaces resolveFileUsages() — no AST walking needed.
func (p *Pipeline) resolveFileUsagesCBM(relPath string, ext *cachedExtraction) []resolvedEdge {
	moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
	importMap := p.importMaps[moduleQN]

	var edges []resolvedEdge
	seen := make(map[[2]string]bool)

	for _, usage := range ext.Result.Usages {
		refName := usage.RefName
		callerQN := usage.EnclosingFuncQN
		if refName == "" {
			continue
		}
		if callerQN == "" {
			callerQN = moduleQN
		}

		result := p.registry.Resolve(refName, moduleQN, importMap)
		if result.QualifiedName == "" {
			continue
		}

		key := [2]string{callerQN, result.QualifiedName}
		if seen[key] {
			continue
		}
		seen[key] = true

		edges = append(edges, resolvedEdge{
			CallerQN: callerQN,
			TargetQN: result.QualifiedName,
			Type:     "USAGE",
		})
	}

	return edges
}

// resolveFileThrowsCBM resolves throw/raise targets using pre-extracted CBM data.
// Replaces resolveFileThrows() — no AST walking needed.
func (p *Pipeline) resolveFileThrowsCBM(relPath string, ext *cachedExtraction) []resolvedEdge {
	moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
	importMap := p.importMaps[moduleQN]

	var edges []resolvedEdge
	seen := make(map[[2]string]bool)

	for _, thr := range ext.Result.Throws {
		excName := thr.ExceptionName
		funcQN := thr.EnclosingFuncQN
		if excName == "" || funcQN == "" {
			continue
		}

		key := [2]string{funcQN, excName}
		if seen[key] {
			continue
		}
		seen[key] = true

		// Determine edge type: THROWS for checked exceptions, RAISES for runtime/unchecked
		edgeType := "RAISES"
		if isCheckedException(excName) {
			edgeType = "THROWS"
		}

		// Try to resolve exception class
		result := p.registry.Resolve(excName, moduleQN, importMap)
		targetQN := excName
		if result.QualifiedName != "" {
			targetQN = result.QualifiedName
		}

		edges = append(edges, resolvedEdge{
			CallerQN: funcQN,
			TargetQN: targetQN,
			Type:     edgeType,
		})
	}

	return edges
}

// resolveFileReadsWritesCBM resolves reads/writes using pre-extracted CBM data.
// Replaces resolveFileReadsWrites() — no AST walking needed.
func (p *Pipeline) resolveFileReadsWritesCBM(relPath string, ext *cachedExtraction) []resolvedEdge {
	moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
	importMap := p.importMaps[moduleQN]

	var edges []resolvedEdge
	seen := make(map[[3]string]bool)

	for _, rw := range ext.Result.ReadWrites {
		varName := rw.VarName
		funcQN := rw.EnclosingFuncQN
		if varName == "" || funcQN == "" {
			continue
		}

		edgeType := "READS"
		if rw.IsWrite {
			edgeType = "WRITES"
		}

		key := [3]string{funcQN, varName, edgeType}
		if seen[key] {
			continue
		}
		seen[key] = true

		// Try to resolve variable to a known node
		result := p.registry.Resolve(varName, moduleQN, importMap)
		if result.QualifiedName == "" {
			continue
		}

		edges = append(edges, resolvedEdge{
			CallerQN: funcQN,
			TargetQN: result.QualifiedName,
			Type:     edgeType,
		})
	}

	return edges
}

// resolveFileTypeRefsCBM resolves type references using pre-extracted CBM data.
// Replaces resolveFileTypeRefs() — no AST walking needed.
func (p *Pipeline) resolveFileTypeRefsCBM(relPath string, ext *cachedExtraction) []resolvedEdge {
	moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
	importMap := p.importMaps[moduleQN]

	var edges []resolvedEdge
	seen := make(map[[2]string]bool)

	for _, tr := range ext.Result.TypeRefs {
		typeName := tr.TypeName
		funcQN := tr.EnclosingFuncQN
		if typeName == "" || funcQN == "" {
			continue
		}

		key := [2]string{funcQN, typeName}
		if seen[key] {
			continue
		}
		seen[key] = true

		// Resolve type name to a node QN
		result := p.registry.Resolve(typeName, moduleQN, importMap)
		if result.QualifiedName == "" {
			continue
		}

		edges = append(edges, resolvedEdge{
			CallerQN: funcQN,
			TargetQN: result.QualifiedName,
			Type:     "USES_TYPE",
		})
	}

	return edges
}

// resolveFileConfiguresCBM resolves env access calls using pre-extracted CBM data.
// Replaces resolveFileConfigures() — no AST walking needed.
func (p *Pipeline) resolveFileConfiguresCBM(relPath string, ext *cachedExtraction, envIndex map[string]string) []resolvedEdge {
	moduleQN := fqn.ModuleQN(p.ProjectName, relPath)

	var edges []resolvedEdge
	seen := make(map[[2]string]bool)

	for _, ea := range ext.Result.EnvAccesses {
		envKey := ea.EnvKey
		funcQN := ea.EnclosingFuncQN
		if envKey == "" || funcQN == "" {
			continue
		}

		targetModuleQN, ok := envIndex[envKey]
		if !ok {
			continue
		}

		key := [2]string{funcQN, targetModuleQN}
		if seen[key] {
			continue
		}
		seen[key] = true

		_ = moduleQN
		edges = append(edges, resolvedEdge{
			CallerQN: funcQN,
			TargetQN: targetModuleQN,
			Type:     "CONFIGURES",
			Properties: map[string]any{
				"env_key": envKey,
			},
		})
	}

	return edges
}

// extractClassFromMethodQN extracts the class QN from a method QN.
// E.g., "project.path.ClassName.methodName" -> "project.path.ClassName"
func extractClassFromMethodQN(methodQN string) string {
	idx := strings.LastIndex(methodQN, ".")
	if idx <= 0 {
		return ""
	}
	return methodQN[:idx]
}

// isCheckedException returns true if the exception name looks like a checked exception
// (Java convention: checked exceptions don't extend RuntimeException).
func isCheckedException(excName string) bool {
	// Heuristic: exceptions ending in "Exception" without "Runtime" prefix are checked
	if strings.HasSuffix(excName, "Exception") && !strings.HasPrefix(excName, "Runtime") {
		return true
	}
	return false
}
