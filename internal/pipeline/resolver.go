package pipeline

import (
	"math"
	"strings"
	"sync"

	"github.com/DeusData/codebase-memory-mcp/internal/lang"
)

// ResolutionResult carries the resolved QN plus quality metadata.
// Initial confidence values are estimates — recalibrate after measuring
// precision per strategy on real repos.
type ResolutionResult struct {
	QualifiedName  string
	Strategy       string  // "import_map", "import_map_suffix", "same_module", "unique_name", "suffix_match", "fuzzy", "type_dispatch"
	Confidence     float64 // 0.0–1.0
	CandidateCount int     // how many candidates were considered

	// DiscriminationApplied — populated when CandidateCount > 1 and a
	// tiebreaker fired. Empty string means the candidate set was unique
	// or no discrimination was needed. Populated by Phase 3+ of the
	// registry.Resolve consolidation
	// (bench/research/registry-resolve-consolidation-plan.md). Today
	// always empty; reading this field is a forward-compat hook.
	DiscriminationApplied string
}

// CallContext bundles every signal a resolver strategy may consult when
// choosing among bare-name candidates. Phase 1 of the registry.Resolve
// consolidation: introduce the surface, forward to legacy implementation,
// no behavior change.
//
// Phase 1 fields (consumed today):
//   - CalleeName, CallerQN, ModuleQN, ImportMap match the legacy
//     Resolve(calleeName, moduleQN, importMap) signature plus an explicit
//     CallerQN slot. CallerQN is unused today and is reserved for Phase 3+
//     when receiver-type discrimination needs the caller's enclosing
//     function QN to look up type bindings.
//
// Phase 2+ fields (reserved, always empty today):
//   - ReceiverType — set by callers when a receiver-type can be inferred
//     from the call site (e.g. PR #149's PerFuncTypeMap). Used by Phase 3a.
//   - ImportBindings — bare-name → qualified-target from `use` statements
//     in scope at the caller. Used by Phase 3b.
//   - Aliases — `use X as alias` mappings. Always empty until ACC-004
//     (bench/accuracy/FOLLOWUPS.md) ships the import-table tracking that
//     populates aliases. Reserved here so the struct shape doesn't churn.
//
// The shape is finalized at Phase 1 so Phase 2 only changes how the
// strategies CONSUME these fields, not the struct itself.
type CallContext struct {
	// Phase 1 — present today.
	CalleeName string
	CallerQN   string
	ModuleQN   string
	ImportMap  map[string]string

	// Phase 2+ — reserved, populated as later phases ship.
	ReceiverType   string
	ImportBindings map[string]string
	Aliases        map[string]string

	// Language identifies the source language of the call site. Populated
	// by CALLS-edge resolution paths (resolveCallEdge -> resolveCallWithTypes
	// -> ResolveCtx) so language-specific drop policies can apply. Empty
	// string means "language unknown" — the drop policies below treat
	// unknown as "do not drop" (preserve legacy behavior on Resolve()
	// callers that don't set this field). Added 2026-05-06 to support
	// CG-1 (Python drop-on-no-match for cross-package-suffix bucket).
	Language lang.Language
}

// shouldDropCrossPackageSuffix returns true when an import_map_suffix /
// suffix_match strategy result should be dropped instead of emitted, based
// on the call site's language. Drop policy is currently Python-only:
// 2026-05-06 baselines show 0.07-0.23 precision on Python adversarial
// fixtures (essentially noise) for the cross-package-suffix bucket, while
// Go and Rust score 0.85-0.95 on the same bucket. See
// `bench/accuracy/baselines/2026-05-06-adversarial-rerun-finding.md`.
func shouldDropCrossPackageSuffix(language lang.Language) bool {
	return language == lang.Python
}

// FunctionRegistry indexes all Function, Method, and Class nodes by qualified
// name and simple name for fast call resolution.
type FunctionRegistry struct {
	mu sync.RWMutex
	// exact maps qualifiedName -> label (Function/Method/Class)
	exact map[string]string
	// byName maps simpleName -> []qualifiedName for reverse lookup
	byName map[string][]string
	// modules: simpleName -> []qualifiedName for Module nodes only.
	// Maintained separately so module-dispatch resolution doesn't pollute
	// byName (which downstream strategies treat as a callable index).
	// ACC-003 (2026-05-02): added so resolveViaTypeStaticDispatch can
	// route `diagnostics::router(...)` calls to the right module.
	modules map[string][]string
	// traitsByStruct: struct_qn -> [trait_qns]. Maps a Rust Struct to the
	// Traits it implements. Used by applyReceiverTypeFilter to accept
	// Trait-method candidates when the receiver is a Struct that
	// implements the Trait — without this, multi-line chain method calls
	// where the actual method is defined on a Trait (not the Struct
	// directly) get dropped by Tier 2.
	traitsByStruct map[string][]string
}

// NewFunctionRegistry creates an empty registry.
func NewFunctionRegistry() *FunctionRegistry {
	return &FunctionRegistry{
		exact:          make(map[string]string),
		byName:         make(map[string][]string),
		modules:        make(map[string][]string),
		traitsByStruct: make(map[string][]string),
	}
}

// RegisterTraitImpl records that structQN implements traitQN.
// Populated by Pipeline.buildTraitImplMap from CBM ImplTraits data.
func (r *FunctionRegistry) RegisterTraitImpl(structQN, traitQN string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.traitsByStruct[structQN] {
		if existing == traitQN {
			return
		}
	}
	r.traitsByStruct[structQN] = append(r.traitsByStruct[structQN], traitQN)
}

// Register adds a node to the registry.
func (r *FunctionRegistry) Register(name, qualifiedName, nodeLabel string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.exact[qualifiedName] = nodeLabel

	// Module nodes go into a separate index so they don't pollute byName
	// (downstream strategies treat byName as callable-only). Module
	// dispatch uses r.modules directly.
	if nodeLabel == "Module" {
		simple := simpleName(qualifiedName)
		for _, existing := range r.modules[simple] {
			if existing == qualifiedName {
				return
			}
		}
		r.modules[simple] = append(r.modules[simple], qualifiedName)
		return
	}

	// Index by simple name (last segment after the final dot)
	simple := simpleName(qualifiedName)
	// Avoid duplicates in the slice
	for _, existing := range r.byName[simple] {
		if existing == qualifiedName {
			return
		}
	}
	r.byName[simple] = append(r.byName[simple], qualifiedName)
}

// ResolveCtx is the CallContext-shaped entry point. As of Phase 2 of the
// registry.Resolve consolidation
// (bench/research/registry-resolve-consolidation-plan.md), this is the
// PRIMARY resolver path: every strategy receives the full CallContext.
// The legacy Resolve(calleeName, moduleQN, importMap) signature now
// builds a CallContext and forwards here, so existing callers see no
// behavior change.
//
// Phase 2 still does NOT consume ctx.ReceiverType, ctx.ImportBindings, or
// ctx.Aliases — those land in Phase 3 (discrimination ladder). The
// strategies receive them so Phase 3 can add discrimination at each
// strategy's CandidateCount > 1 branch without further signature churn.
//
// Forwarding-equivalence is pinned by TestResolveCtx_ForwardsToLegacy.
func (r *FunctionRegistry) ResolveCtx(ctx CallContext) ResolutionResult {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Rust scoped-form `Foo::new` / `Type::method` — try type-static
	// dispatch FIRST. If it matches a registered Class+method exactly,
	// emit. Otherwise fall through to existing strategies (some `::`
	// callees resolve via fuzzy / import-map paths that we shouldn't
	// short-circuit).
	if strings.Contains(ctx.CalleeName, "::") {
		if result := r.resolveViaTypeStaticDispatch(ctx); result.QualifiedName != "" {
			return result
		}
	}

	if result := r.resolveViaImportMap(ctx); result.QualifiedName != "" {
		return result
	}

	if result := r.resolveViaSameModule(ctx); result.QualifiedName != "" {
		return result
	}

	return r.resolveViaNameLookup(ctx)
}

// Resolve is the legacy entry point. Builds a CallContext and forwards to
// ResolveCtx. Existing callers (decorates.go, pipeline.go, pipeline_cbm.go
// references/exceptions/variables/types paths, tests) continue to work
// unchanged. Phase 4 may migrate or remove this wrapper.
func (r *FunctionRegistry) Resolve(calleeName, moduleQN string, importMap map[string]string) ResolutionResult {
	return r.ResolveCtx(CallContext{
		CalleeName: calleeName,
		ModuleQN:   moduleQN,
		ImportMap:  importMap,
	})
}

// splitCalleeName extracts the leading prefix and remainder of a callee
// name like "pkg.Func" -> ("pkg", "Func") or "obj.field.method" ->
// ("obj", "field.method"). Bare names yield (calleeName, "").
//
// Centralized so each strategy doesn't re-implement the split.
func splitCalleeName(calleeName string) (prefix, suffix string) {
	parts := strings.SplitN(calleeName, ".", 2)
	prefix = parts[0]
	if len(parts) > 1 {
		suffix = parts[1]
	}
	return prefix, suffix
}

// resolveViaImportMap tries to resolve a callee using the import map (Strategy 1).
func (r *FunctionRegistry) resolveViaImportMap(ctx CallContext) ResolutionResult {
	if ctx.ImportMap == nil {
		return ResolutionResult{}
	}
	prefix, suffix := splitCalleeName(ctx.CalleeName)
	resolved, ok := ctx.ImportMap[prefix]
	if !ok {
		return ResolutionResult{}
	}
	var candidate string
	if suffix != "" {
		candidate = resolved + "." + suffix
	} else {
		candidate = resolved
	}
	if _, exists := r.exact[candidate]; exists {
		return ResolutionResult{QualifiedName: candidate, Strategy: "import_map", Confidence: 0.95, CandidateCount: 1}
	}
	if suffix != "" {
		for qn := range r.exact {
			if strings.HasPrefix(qn, resolved+".") && strings.HasSuffix(qn, "."+suffix) {
				// CG-1 drop-on-no-match for Python: this strategy
				// scores 0.07 precision on flask-adversarial. Drop
				// here lets downstream same_module / nameLookup try;
				// if neither resolves, the call goes unresolved (the
				// correct outcome — we don't have evidence this is
				// the right target).
				if shouldDropCrossPackageSuffix(ctx.Language) {
					return ResolutionResult{}
				}
				return ResolutionResult{QualifiedName: qn, Strategy: "import_map_suffix", Confidence: 0.85, CandidateCount: 1}
			}
		}
	}
	return ResolutionResult{}
}

// resolveViaSameModule tries to resolve a callee within the same module (Strategy 2).
func (r *FunctionRegistry) resolveViaSameModule(ctx CallContext) ResolutionResult {
	_, suffix := splitCalleeName(ctx.CalleeName)
	sameModule := ctx.ModuleQN + "." + ctx.CalleeName
	if _, exists := r.exact[sameModule]; exists {
		return ResolutionResult{QualifiedName: sameModule, Strategy: "same_module", Confidence: 0.90, CandidateCount: 1}
	}
	if suffix != "" {
		sameModuleQualified := ctx.ModuleQN + "." + suffix
		if label, exists := r.exact[sameModuleQualified]; exists {
			// Type-aware tiebreak (2026-05-02): a method-call shape
			// (calleeName has a dot, e.g. `args.run`) shouldn't auto-
			// resolve to a same-module FREE FUNCTION just because the
			// simple name matches. Methods on a struct are the more
			// likely target. Defer to nameLookup which can also see
			// Method candidates project-wide.
			//
			// FN #3 from psm plateau-diagnose
			// (2026-05-02): `args.run()` inside MigrationArgs.run was
			// resolving to the free fn `cmd.db.run` instead of the
			// method `Commands.run` because the same_module suffix
			// shortcut hit at confidence 0.90 before suffix_match
			// could consider methods.
			if strings.Contains(ctx.CalleeName, ".") && label == "Function" {
				return ResolutionResult{}
			}
			return ResolutionResult{QualifiedName: sameModuleQualified, Strategy: "same_module", Confidence: 0.90, CandidateCount: 1}
		}
	}
	return ResolutionResult{}
}

// resolveViaTypeStaticDispatch handles Rust scoped-form static method calls
// (`Foo::new`, `Foo::Bar::method`). Splits on the first `::` to get the
// type name, treats the remainder (with internal `::` -> `.`) as the method
// path, then requires the type name to match a registered Class-family
// label and the resulting `<class_qn>.<method>` to exist in the registry.
//
// Drop-on-no-match is intentional: PR #165 regressed by normalizing `::`
// to `.` at the extractor and letting external paths fall through to
// project-wide suffix-match. Vec::new and tracing::info phantom-bound
// to internal `.new` / `.info` defs at scale (-12.6pp F1 on merry-rust).
// This strategy gates emission on Class-membership and refuses to fall
// through, eliminating that class.
//
// Strategy: "type_static_dispatch"; rule: "exact-qn-match" semantically
// (single-target, structural).
func (r *FunctionRegistry) resolveViaTypeStaticDispatch(ctx CallContext) ResolutionResult {
	if !strings.Contains(ctx.CalleeName, "::") {
		return ResolutionResult{}
	}
	parts := strings.SplitN(ctx.CalleeName, "::", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ResolutionResult{}
	}
	typeName := parts[0]
	// `Foo::Bar::new` -> typeName="Foo", remainder="Bar::new" -> "Bar.new".
	// We only resolve <class_qn>.<remainder> where typeName is the class.
	// For deeper nesting (Foo::Bar::baz::qux) the rightmost class is the
	// dispatch target, but resolving that requires walking the registered
	// hierarchy — defer.
	remainder := strings.ReplaceAll(parts[1], "::", ".")

	// Caller's parent-module prefix — used for module-dispatch reachability.
	// E.g., caller ModuleQN = "<project>.src.v2.telem.health.mod" ->
	// parent = "<project>.src.v2.telem.health". Sibling/cousin modules
	// share this prefix and are reachable without explicit `use`.
	callerParent := ""
	if ctx.ModuleQN != "" {
		if idx := strings.LastIndex(ctx.ModuleQN, "."); idx > 0 {
			callerParent = ctx.ModuleQN[:idx]
		}
	}

	classLabels := map[string]bool{
		"Class": true, "Struct": true, "Type": true,
		"Enum": true, "Interface": true, "Trait": true,
	}
	var emissions []string

	// Class-family lookup (existing, ACC-001).
	for _, classQN := range r.byName[typeName] {
		label := r.exact[classQN]
		if !classLabels[label] {
			continue
		}
		candidate := classQN + "." + remainder
		if _, exists := r.exact[candidate]; !exists {
			continue
		}
		// Class reachability: same-module OR explicit import. Sibling
		// modules don't qualify here — common type names (Result, Error)
		// would phantom-match unrelated internal classes. PR #165 lesson.
		sameModule := ctx.ModuleQN != "" && strings.HasPrefix(classQN, ctx.ModuleQN+".")
		_, imported := ctx.ImportBindings[typeName]
		if sameModule || imported {
			emissions = append(emissions, candidate)
		}
	}

	// ACC-003 (2026-05-02): Module dispatch lookup. Routes
	// `diagnostics::router(...)` to <module_qn>.router. Lookups go through
	// r.modules — a separate Module-only index — so callable-resolution
	// paths in resolveViaNameLookup aren't polluted with structural Module
	// candidates. Module reachability is WIDER than Class: sibling/cousin
	// modules in the same package are reachable without a `use`. Common
	// type-name phantom risk doesn't apply because Module names tend to
	// be unique within a package, and the ambiguous-drop (2+ matches)
	// catches collisions across packages.
	for _, modQN := range r.modules[typeName] {
		candidate := modQN + "." + remainder
		if _, exists := r.exact[candidate]; !exists {
			continue
		}
		sameModule := ctx.ModuleQN != "" && strings.HasPrefix(modQN, ctx.ModuleQN+".")
		_, imported := ctx.ImportBindings[typeName]
		siblingModule := callerParent != "" && strings.HasPrefix(modQN, callerParent+".")
		if sameModule || imported || siblingModule {
			emissions = append(emissions, candidate)
		}
	}

	// ACC-004 (2026-05-03): Resolution-side aliasing. If typeName isn't a
	// registered Class-family or Module name BUT ctx.ImportBindings[typeName]
	// points to an internal Rust path, chase the alias to the real class.
	// Example: `use crate::repo::AssetRepo as MyAlias;` ->
	// ImportBindings["MyAlias"] = "crate::repo::AssetRepo". The target's
	// last segment ("AssetRepo") is looked up in r.byName; the full target
	// path is matched as a suffix of the candidate's QN to disambiguate
	// when the simple name collides across packages.
	//
	// Reachability is implicit: the existence of the alias binding IS
	// evidence the caller imported the type. No same-module / sibling
	// gate needed.
	if len(r.byName[typeName]) == 0 && len(r.modules[typeName]) == 0 {
		if importTarget, ok := ctx.ImportBindings[typeName]; ok {
			targetQN := strings.TrimPrefix(strings.ReplaceAll(importTarget, "::", "."), "crate.")
			lastSeg := targetQN
			if idx := strings.LastIndex(targetQN, "."); idx >= 0 {
				lastSeg = targetQN[idx+1:]
			}
			dotTarget := "." + targetQN
			for _, qn := range r.byName[lastSeg] {
				label := r.exact[qn]
				if !classLabels[label] {
					continue
				}
				if qn == targetQN || strings.HasSuffix(qn, dotTarget) {
					candidate := qn + "." + remainder
					if _, exists := r.exact[candidate]; exists {
						emissions = append(emissions, candidate)
					}
					break
				}
			}
		}
	}

	if len(emissions) == 0 && len(r.byName[typeName]) == 0 && len(r.modules[typeName]) == 0 {
		return ResolutionResult{}
	}
	if len(emissions) == 1 {
		return ResolutionResult{
			QualifiedName:  emissions[0],
			Strategy:       "type_static_dispatch",
			Confidence:     0.85,
			CandidateCount: 1,
		}
	}
	// 0 matches (external static call) or >1 (ambiguous across same-named
	// types) -> drop. Caller (ResolveCtx) does NOT fall through.
	return ResolutionResult{}
}

// resolveViaNameLookup tries project-wide name lookup and suffix matching (Strategies 3+4).
func (r *FunctionRegistry) resolveViaNameLookup(ctx CallContext) ResolutionResult {
	_, suffix := splitCalleeName(ctx.CalleeName)
	lookupName := ctx.CalleeName
	if suffix != "" {
		lookupName = suffix
	}
	simple := simpleName(lookupName)
	candidates := r.byName[simple]

	// Phase 3a discrimination: when the call site has a known receiver
	// type, prefer candidates whose parent class matches it. If the
	// receiver type is non-empty AND no candidate matches, the call is
	// almost certainly external — drop the binding rather than fall
	// through to bare-name suffix-match (the path that produced every
	// phantom in the negative-fixture corpus).
	//
	// Only fires for method-call shape (calleeName contains "."). Free-
	// function calls (`bare(args)`) keep legacy behavior; Phase 3b's
	// import-binding tier handles those.
	if discriminated, applied, dropAll := r.applyReceiverTypeFilter(ctx, candidates); applied != "" {
		if dropAll {
			return ResolutionResult{}
		}
		candidates = discriminated
	}

	// Phase 3b discrimination: free-function calls with a `use` import
	// binding for the bare name. If the import target is external,
	// drop internal candidates instead of falling through to bare-name
	// suffix-match. If the import target IS internal, prefer the
	// matching candidate.
	if discriminated, applied, dropAll := r.applyImportBindingFilter(ctx, candidates); applied != "" {
		if dropAll {
			return ResolutionResult{}
		}
		candidates = discriminated
	}

	// Strategy 3: unique name — single candidate project-wide
	if len(candidates) == 1 {
		conf := 0.75
		if ctx.ImportMap != nil && !isImportReachable(candidates[0], ctx.ImportMap) {
			conf *= 0.5
		}
		return ResolutionResult{QualifiedName: candidates[0], Strategy: "unique_name", Confidence: conf, CandidateCount: 1}
	}

	// Strategy 4: suffix match with import distance scoring
	if suffix != "" {
		if res := r.resolveSuffixMatch(ctx, candidates); res.QualifiedName != "" {
			return res
		}
	}

	return r.pickBestCandidate(ctx, candidates)
}

// applyReceiverTypeFilter is the Phase 3a Tier 2 discriminator
// (bench/research/registry-resolve-consolidation-plan.md).
//
// Returns (filtered, applied, dropAll):
//   - applied=""  → discrimination did not fire (receiver type unknown,
//                   or call shape is not method-call). Caller continues
//                   with the unfiltered candidate set.
//   - applied="receiver-type-match" → at least one candidate's parent
//                   class equals ctx.ReceiverType. Caller proceeds with
//                   the filtered set.
//   - applied="receiver-type-no-internal-match", dropAll=true →
//                   ctx.ReceiverType is set but no internal candidate
//                   matches. The call is almost certainly external
//                   (the receiver type isn't a registered class, or
//                   none of its methods share this bare name). Caller
//                   should return empty to drop the binding entirely.
//
// Method candidates have parent class = the QN segment immediately
// before the method name. Function/Class/etc. candidates have no
// parent-class semantics; they pass through unfiltered.
func (r *FunctionRegistry) applyReceiverTypeFilter(ctx CallContext, candidates []string) (filtered []string, applied string, dropAll bool) {
	if ctx.ReceiverType == "" || len(candidates) == 0 {
		return candidates, "", false
	}
	// Only fire on method-call shape. Free-function calls go through
	// Phase 3b's import-binding tier.
	if !strings.Contains(ctx.CalleeName, ".") {
		return candidates, "", false
	}
	// Trait/Impl support: when the receiver is a Struct, accept Methods
	// whose parent class is a Trait that the Struct implements. Without
	// this, multi-line chain calls where the actual method definition
	// lives on a Trait (e.g., `impl Display for Foo { fn fmt() {...} }`)
	// get dropped by Tier 2. The traitsByStruct index is populated by
	// Pipeline.buildTraitImplMap from CBM ImplTraits data.
	implementedTraits := r.traitsByStruct[ctx.ReceiverType]
	traitSet := make(map[string]bool, len(implementedTraits))
	for _, t := range implementedTraits {
		traitSet[t] = true
	}

	var matching []string
	var sawMethodCandidate bool
	for _, qn := range candidates {
		label := r.exact[qn]
		if label != "Method" {
			// Non-method candidate (e.g. free function) — receiver-type
			// discrimination is undefined for these. Pass them through
			// so the existing logic still considers them.
			matching = append(matching, qn)
			continue
		}
		sawMethodCandidate = true
		// Parent class QN = everything before the last "." segment.
		idx := strings.LastIndex(qn, ".")
		if idx < 0 {
			continue
		}
		parent := qn[:idx]
		if parent == ctx.ReceiverType || traitSet[parent] {
			matching = append(matching, qn)
		}
	}
	// If we saw at least one Method candidate but nothing matched the
	// receiver type, the call is external by inference. Drop the
	// binding (return empty).
	if sawMethodCandidate && len(matching) == 0 {
		return nil, "receiver-type-no-internal-match", true
	}
	// If filtering shrank the set, that's a discrimination win. If it
	// didn't (only non-method candidates present, or no Methods at all),
	// pass through quietly so legacy behavior decides.
	if sawMethodCandidate && len(matching) < len(candidates) {
		return matching, "receiver-type-match", false
	}
	return candidates, "", false
}

// applyImportBindingFilter is the Phase 3b Tier 3 discriminator
// (bench/research/registry-resolve-consolidation-plan.md).
//
// Targets free-function bare-name calls where the calleeName is brought
// into scope via a `use`/`import` statement. Three outcomes:
//
//   - applied="import-binding-match", dropAll=false: at least one
//     internal candidate's QN ends with the import binding's target.
//     The filter narrows to the matching candidate(s); legacy logic
//     picks among them.
//   - applied="import-binding-external", dropAll=true: a binding exists
//     for this bare name but no internal candidate matches AND no
//     registered QN ends with the target. The call is external; caller
//     should return empty to drop the binding.
//   - applied="" otherwise: no binding for this bare name, call shape
//     is method-call, or the target IS internal (registered somewhere)
//     but didn't surface in current candidates — pass through and let
//     legacy logic decide.
//
// Comparison uses suffix match because import targets are written in
// the user's source-relative form (`utils.fetch_data` for Python's
// `from utils import fetch_data`, `futures_util::future::ready` for
// Rust's `use futures_util::future::ready;`) while registry QNs carry
// the project-name prefix (`<project>.utils.fetch_data`). Rust's `::`
// separator is normalized to QN-style `.` first.
func (r *FunctionRegistry) applyImportBindingFilter(ctx CallContext, candidates []string) (filtered []string, applied string, dropAll bool) {
	if len(ctx.ImportBindings) == 0 || len(candidates) == 0 {
		return candidates, "", false
	}
	// Tier 3 only fires for free-function calls. Method calls go
	// through Tier 2.
	if strings.Contains(ctx.CalleeName, ".") {
		return candidates, "", false
	}
	target, ok := ctx.ImportBindings[ctx.CalleeName]
	if !ok {
		return candidates, "", false
	}
	// Normalize Rust `::` separators to QN-style `.` for comparison.
	targetQN := strings.ReplaceAll(target, "::", ".")
	// Rust `crate::` is the implicit crate root, not a registered QN
	// segment. Registry QNs carry the project-name prefix
	// (`<project>.apid.src.v1.util.error_response`) without `crate`.
	// Strip the leading `crate.` so suffix-match catches internal
	// imports of the form `use crate::v1::util::error_response;`.
	// Without this, every internal Rust use that travels through `crate::`
	// is misclassified as external and its bindings dropped — caused
	// the apid -14.3pp F1 regression on psm's
	// 2026-05-02 measurement after Phase 3b shipped.
	targetQN = strings.TrimPrefix(targetQN, "crate.")
	dotTarget := "." + targetQN

	// Look for internal candidate(s) whose QN ends with the import
	// target. Exact equality OR suffix match catches both
	// "<project>.utils.fetch_data" and the (rare) bare "utils.fetch_data"
	// case where no project prefix is added.
	var matching []string
	for _, qn := range candidates {
		if qn == targetQN || strings.HasSuffix(qn, dotTarget) {
			matching = append(matching, qn)
		}
	}
	if len(matching) > 0 {
		return matching, "import-binding-match", false
	}
	// Binding exists; not in current candidate set. Check if it
	// resolves to ANY registered QN — if yes, the target IS internal
	// (current candidates didn't include it for some other reason),
	// pass through rather than risk silently dropping.
	for qn := range r.exact {
		if qn == targetQN || strings.HasSuffix(qn, dotTarget) {
			return candidates, "", false
		}
	}
	// External: drop internal candidates entirely.
	return nil, "import-binding-external", true
}

// resolveSuffixMatch handles Strategy 4 — suffix-based matching among multiple candidates.
func (r *FunctionRegistry) resolveSuffixMatch(ctx CallContext, candidates []string) ResolutionResult {
	// CG-1 drop-on-no-match for Python: the suffix_match strategy is
	// catastrophically imprecise on Python adversarial fixtures (0.07
	// flask, 0.23 requests). Drop entire branch for Python; downstream
	// callers (resolveViaNameLookup) treat empty result as "unresolved"
	// rather than emitting a phantom edge. Go and Rust keep current
	// behavior (precision 0.85+ on those languages).
	if shouldDropCrossPackageSuffix(ctx.Language) {
		return ResolutionResult{}
	}
	_, suffix := splitCalleeName(ctx.CalleeName)
	var matches []string
	for _, qn := range candidates {
		if strings.HasSuffix(qn, "."+ctx.CalleeName) {
			conf := candidateCountPenalty(importAdjustedConfidence(0.55, qn, ctx.ImportMap), len(candidates))
			return ResolutionResult{QualifiedName: qn, Strategy: "suffix_match", Confidence: conf, CandidateCount: len(candidates)}
		}
		if strings.HasSuffix(qn, "."+suffix) {
			matches = append(matches, qn)
		}
	}
	if ctx.ImportMap != nil {
		matches = filterImportReachable(matches, ctx.ImportMap)
	}
	if len(matches) == 1 {
		return ResolutionResult{QualifiedName: matches[0], Strategy: "suffix_match", Confidence: candidateCountPenalty(0.55, len(candidates)), CandidateCount: len(candidates)}
	}
	if len(matches) > 1 {
		best := r.bestByImportDistancePreferMethod(matches, ctx.ModuleQN, ctx.CalleeName)
		return ResolutionResult{QualifiedName: best, Strategy: "suffix_match", Confidence: candidateCountPenalty(0.55, len(matches)), CandidateCount: len(matches)}
	}
	return ResolutionResult{}
}

// pickBestCandidate selects the best match from multiple candidates with
// import filtering. Method receiver of the registry so we can apply the
// type-aware tiebreak (prefer Method over Function for method-call shape).
func (r *FunctionRegistry) pickBestCandidate(ctx CallContext, candidates []string) ResolutionResult {
	if len(candidates) <= 1 {
		return ResolutionResult{}
	}
	// CG-1 drop-on-no-match for Python: pickBestCandidate emits at the
	// suffix_match strategy when forced to choose among ambiguous
	// project-wide name matches. Same precision concern as
	// resolveSuffixMatch above — drop entire branch for Python.
	if shouldDropCrossPackageSuffix(ctx.Language) {
		return ResolutionResult{}
	}
	filtered := candidates
	if ctx.ImportMap != nil {
		filtered = filterImportReachable(candidates, ctx.ImportMap)
	}
	if len(filtered) == 0 {
		best := r.bestByImportDistancePreferMethod(candidates, ctx.ModuleQN, ctx.CalleeName)
		return ResolutionResult{QualifiedName: best, Strategy: "suffix_match", Confidence: candidateCountPenalty(0.55*0.5, len(candidates)), CandidateCount: len(candidates)}
	}
	if len(filtered) == 1 {
		return ResolutionResult{QualifiedName: filtered[0], Strategy: "suffix_match", Confidence: candidateCountPenalty(0.55, len(candidates)), CandidateCount: len(candidates)}
	}
	best := r.bestByImportDistancePreferMethod(filtered, ctx.ModuleQN, ctx.CalleeName)
	return ResolutionResult{QualifiedName: best, Strategy: "suffix_match", Confidence: candidateCountPenalty(0.55, len(filtered)), CandidateCount: len(filtered)}
}

// importAdjustedConfidence halves confidence when a candidate is not import-reachable.
func importAdjustedConfidence(base float64, candidateQN string, importMap map[string]string) float64 {
	if importMap != nil && !isImportReachable(candidateQN, importMap) {
		return base * 0.5
	}
	return base
}

// candidateCountPenalty scales confidence inversely with candidate count.
// Common method names (String, Get, Add, Close) have many candidates project-wide,
// making random selection unreliable. Penalty: base * min(1.0, 3.0/count).
// 1-3 candidates: full confidence. 6: halved. 30: 1/10th.
func candidateCountPenalty(base float64, count int) float64 {
	if count <= 3 {
		return base
	}
	return base * math.Min(1.0, 3.0/float64(count))
}

// FuzzyResolveCtx is the CallContext-shaped fuzzy resolver. As of Phase 2
// this is the primary fuzzy path; FuzzyResolve forwards here. See ResolveCtx
// for the consolidation rationale.
//
// Phase 2 still does NOT consume ctx.ReceiverType or ctx.ImportBindings —
// those land in Phase 3. The fuzzy path is the most likely to benefit from
// receiver-type discrimination since CandidateCount > 1 is common here.
func (r *FunctionRegistry) FuzzyResolveCtx(ctx CallContext) (ResolutionResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Extract the simple name (last segment after dots)
	lookupName := simpleName(ctx.CalleeName)
	candidates := r.byName[lookupName]

	if len(candidates) == 0 {
		return ResolutionResult{}, false
	}

	// Phase 3a Tier 2: receiver-type discrimination. Same semantics as
	// resolveViaNameLookup. Fuzzy is the most permissive resolver path
	// and benefits most from receiver-type filtering.
	if discriminated, applied, dropAll := r.applyReceiverTypeFilter(ctx, candidates); applied != "" {
		if dropAll {
			return ResolutionResult{}, false
		}
		candidates = discriminated
	}

	// Phase 3b Tier 3: import-binding discrimination. Drop internal
	// candidates when a `use` import binds the bare name to an external
	// target. Most permissive resolver path → most likely to phantom-
	// emit on external calls if not gated.
	if discriminated, applied, dropAll := r.applyImportBindingFilter(ctx, candidates); applied != "" {
		if dropAll {
			return ResolutionResult{}, false
		}
		candidates = discriminated
	}

	// If there's exactly one candidate, use it
	if len(candidates) == 1 {
		conf := 0.40
		if ctx.ImportMap != nil && !isImportReachable(candidates[0], ctx.ImportMap) {
			conf *= 0.5
		}
		return ResolutionResult{
			QualifiedName: candidates[0], Strategy: "fuzzy",
			Confidence: conf, CandidateCount: 1,
		}, true
	}

	// Multiple candidates: filter by import reachability, then pick best by distance
	filtered := candidates
	if ctx.ImportMap != nil {
		filtered = filterImportReachable(candidates, ctx.ImportMap)
	}
	if len(filtered) == 0 {
		// No import-reachable candidates — use original with penalty
		best := bestByImportDistance(candidates, ctx.ModuleQN)
		if best == "" {
			return ResolutionResult{}, false
		}
		return ResolutionResult{
			QualifiedName: best, Strategy: "fuzzy",
			Confidence: candidateCountPenalty(0.30*0.5, len(candidates)), CandidateCount: len(candidates),
		}, true
	}
	if len(filtered) == 1 {
		return ResolutionResult{
			QualifiedName: filtered[0], Strategy: "fuzzy",
			Confidence: candidateCountPenalty(0.40, len(candidates)), CandidateCount: len(candidates),
		}, true
	}
	best := bestByImportDistance(filtered, ctx.ModuleQN)
	if best == "" {
		return ResolutionResult{}, false
	}
	return ResolutionResult{
		QualifiedName: best, Strategy: "fuzzy",
		Confidence: candidateCountPenalty(0.30, len(filtered)), CandidateCount: len(filtered),
	}, true
}

// FuzzyResolve is the legacy fuzzy entry point. Builds a CallContext and
// forwards to FuzzyResolveCtx. Existing callers (pipeline_cbm.go:462) work
// unchanged.
//
// Unlike Resolve(), this does not require prefix/import agreement — it purely
// matches on the function name.
func (r *FunctionRegistry) FuzzyResolve(calleeName, moduleQN string, importMap map[string]string) (ResolutionResult, bool) {
	return r.FuzzyResolveCtx(CallContext{
		CalleeName: calleeName,
		ModuleQN:   moduleQN,
		ImportMap:  importMap,
	})
}

// LabelOf returns the node label for a qualified name, or "" if not registered.
func (r *FunctionRegistry) LabelOf(qualifiedName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.exact[qualifiedName]
}

// IsClassLike returns true if `qualifiedName` is registered as a
// class-like type (Class, Struct, Enum, Trait, Interface). Used by the
// chain walker (CG-2) to gate type_dispatch emission: even if
// `currentType.method` exists exactly, we should not emit type_dispatch
// when `currentType` itself isn't a registered class. This catches
// edge cases where the chain walker would otherwise bypass the
// `applyReceiverTypeFilter` safety net (e.g. when `currentType` is a
// Module name or other non-class entity that happens to share a
// qualified-name prefix with a method).
func (r *FunctionRegistry) IsClassLike(qualifiedName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	switch r.exact[qualifiedName] {
	case "Class", "Struct", "Enum", "Trait", "Interface":
		return true
	}
	return false
}

// Exists returns true if a qualified name is registered.
// Uses RLock for concurrent read safety.
func (r *FunctionRegistry) Exists(qualifiedName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.exact[qualifiedName]
	return ok
}

// FindByName returns all qualified names with the given simple name.
func (r *FunctionRegistry) FindByName(name string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, len(r.byName[name]))
	copy(result, r.byName[name])
	return result
}

// FindEndingWith returns all qualified names ending with ".suffix".
func (r *FunctionRegistry) FindEndingWith(suffix string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	target := "." + suffix
	var result []string
	for qn := range r.exact {
		if strings.HasSuffix(qn, target) {
			result = append(result, qn)
		}
	}
	return result
}

// Size returns the number of entries in the registry.
func (r *FunctionRegistry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.exact)
}

// simpleName extracts the last dot-separated segment.
func simpleName(qn string) string {
	if idx := strings.LastIndex(qn, "."); idx >= 0 {
		return qn[idx+1:]
	}
	return qn
}

// bestByImportDistance picks the candidate whose QN shares the longest common
// prefix with the caller's module QN. This approximates "closest in the
// project structure".
func bestByImportDistance(candidates []string, callerModuleQN string) string {
	best := ""
	bestLen := -1

	for _, c := range candidates {
		prefixLen := commonPrefixLen(c, callerModuleQN)
		if prefixLen > bestLen {
			bestLen = prefixLen
			best = c
		}
	}
	return best
}

// bestByImportDistancePreferMethod is the type-aware variant: when calleeName
// looks like a method call (`obj.method`), and there are import-distance ties,
// prefer Method-labeled candidates over Function-labeled ones. Falls back to
// bestByImportDistance for path calls and bare-name calls.
//
// 2026-05-02 plateau-diagnose Step 6 finding: FN #3 had `args.run()` resolved
// to free fn `cmd.db.run` because three method candidates and the free fn all
// shared the same import-distance score; bestByImportDistance picked the
// first-encountered (slice iteration order). The type-aware tiebreak fixes
// the FN #3 class without affecting non-method-call resolution.
func (r *FunctionRegistry) bestByImportDistancePreferMethod(candidates []string, callerModuleQN, calleeName string) string {
	if !strings.Contains(calleeName, ".") {
		// Bare-name calls: original behavior, no method preference.
		return bestByImportDistance(candidates, callerModuleQN)
	}
	best := ""
	bestLen := -1
	bestIsMethod := false
	for _, c := range candidates {
		prefixLen := commonPrefixLen(c, callerModuleQN)
		isMethod := r.exact[c] == "Method"
		switch {
		case prefixLen > bestLen:
			bestLen = prefixLen
			best = c
			bestIsMethod = isMethod
		case prefixLen == bestLen && isMethod && !bestIsMethod:
			best = c
			bestIsMethod = true
		}
	}
	return best
}

// commonPrefixLen returns the length of the common dot-segment prefix.
func commonPrefixLen(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	count := 0
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] != bParts[i] {
			break
		}
		count++
	}
	return count
}

// modulePrefix extracts the module portion of a QN (everything before the last dot segment).
func modulePrefix(qn string) string {
	if idx := strings.LastIndex(qn, "."); idx >= 0 {
		return qn[:idx]
	}
	return qn
}

// isImportReachable checks if the candidate's module prefix appears anywhere
// in the caller's import map values.
func isImportReachable(candidateQN string, importMap map[string]string) bool {
	candidateModule := modulePrefix(candidateQN)
	for _, importedQN := range importMap {
		if strings.HasPrefix(candidateModule, importedQN) || strings.HasPrefix(importedQN, candidateModule) {
			return true
		}
	}
	return false
}

// filterImportReachable returns only candidates reachable via the import map.
// Returns the original slice if importMap is nil or filtering eliminates everything.
func filterImportReachable(candidates []string, importMap map[string]string) []string {
	if importMap == nil {
		return candidates
	}
	var filtered []string
	for _, c := range candidates {
		if isImportReachable(c, importMap) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// confidenceBand returns a human-readable band label for a confidence score.
func confidenceBand(score float64) string {
	switch {
	case score >= 0.7:
		return "high"
	case score >= 0.45:
		return "medium"
	default:
		return "speculative"
	}
}
