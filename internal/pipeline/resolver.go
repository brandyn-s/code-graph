package pipeline

import (
	"math"
	"strings"
	"sync"
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
}

// FunctionRegistry indexes all Function, Method, and Class nodes by qualified
// name and simple name for fast call resolution.
type FunctionRegistry struct {
	mu sync.RWMutex
	// exact maps qualifiedName -> label (Function/Method/Class)
	exact map[string]string
	// byName maps simpleName -> []qualifiedName for reverse lookup
	byName map[string][]string
}

// NewFunctionRegistry creates an empty registry.
func NewFunctionRegistry() *FunctionRegistry {
	return &FunctionRegistry{
		exact:  make(map[string]string),
		byName: make(map[string][]string),
	}
}

// Register adds a node to the registry.
func (r *FunctionRegistry) Register(name, qualifiedName, nodeLabel string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.exact[qualifiedName] = nodeLabel

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

// ResolveCtx is the CallContext-shaped entry point introduced in Phase 1
// of the registry.Resolve consolidation
// (bench/research/registry-resolve-consolidation-plan.md). It forwards
// to the legacy Resolve signature today; Phase 2 will rewire the
// strategies to consume CallContext directly, and Phase 3 will add
// discrimination using ctx.ReceiverType / ctx.ImportBindings.
//
// Forwarding-equivalence is pinned by TestResolveCtx_ForwardsToLegacy
// in resolver_test.go. Callers can migrate to ResolveCtx incrementally
// without behavior change.
func (r *FunctionRegistry) ResolveCtx(ctx CallContext) ResolutionResult {
	return r.Resolve(ctx.CalleeName, ctx.ModuleQN, ctx.ImportMap)
}

// Resolve attempts to find the qualified name of a callee using a prioritized
// resolution strategy:
//  1. Import map lookup
//  2. Same-module match
//  3. Project-wide single match by simple name
//  4. Suffix match with import distance scoring
func (r *FunctionRegistry) Resolve(calleeName, moduleQN string, importMap map[string]string) ResolutionResult {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Split calleeName for qualified calls like "pkg.Func" or "obj.method"
	parts := strings.SplitN(calleeName, ".", 2)
	prefix := parts[0]
	var suffix string
	if len(parts) > 1 {
		suffix = parts[1]
	}

	if result := r.resolveViaImportMap(prefix, suffix, importMap); result.QualifiedName != "" {
		return result
	}

	if result := r.resolveViaSameModule(calleeName, suffix, moduleQN); result.QualifiedName != "" {
		return result
	}

	return r.resolveViaNameLookup(calleeName, suffix, moduleQN, importMap)
}

// resolveViaImportMap tries to resolve a callee using the import map (Strategy 1).
func (r *FunctionRegistry) resolveViaImportMap(prefix, suffix string, importMap map[string]string) ResolutionResult {
	if importMap == nil {
		return ResolutionResult{}
	}
	resolved, ok := importMap[prefix]
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
				return ResolutionResult{QualifiedName: qn, Strategy: "import_map_suffix", Confidence: 0.85, CandidateCount: 1}
			}
		}
	}
	return ResolutionResult{}
}

// resolveViaSameModule tries to resolve a callee within the same module (Strategy 2).
func (r *FunctionRegistry) resolveViaSameModule(calleeName, suffix, moduleQN string) ResolutionResult {
	sameModule := moduleQN + "." + calleeName
	if _, exists := r.exact[sameModule]; exists {
		return ResolutionResult{QualifiedName: sameModule, Strategy: "same_module", Confidence: 0.90, CandidateCount: 1}
	}
	if suffix != "" {
		sameModuleQualified := moduleQN + "." + suffix
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
			if strings.Contains(calleeName, ".") && label == "Function" {
				return ResolutionResult{}
			}
			return ResolutionResult{QualifiedName: sameModuleQualified, Strategy: "same_module", Confidence: 0.90, CandidateCount: 1}
		}
	}
	return ResolutionResult{}
}

// resolveViaNameLookup tries project-wide name lookup and suffix matching (Strategies 3+4).
func (r *FunctionRegistry) resolveViaNameLookup(calleeName, suffix, moduleQN string, importMap map[string]string) ResolutionResult {
	lookupName := calleeName
	if suffix != "" {
		lookupName = suffix
	}
	simple := simpleName(lookupName)
	candidates := r.byName[simple]

	// Strategy 3: unique name — single candidate project-wide
	if len(candidates) == 1 {
		conf := 0.75
		if importMap != nil && !isImportReachable(candidates[0], importMap) {
			conf *= 0.5
		}
		return ResolutionResult{QualifiedName: candidates[0], Strategy: "unique_name", Confidence: conf, CandidateCount: 1}
	}

	// Strategy 4: suffix match with import distance scoring
	if suffix != "" {
		if res := r.resolveSuffixMatch(calleeName, suffix, moduleQN, importMap, candidates); res.QualifiedName != "" {
			return res
		}
	}

	return r.pickBestCandidate(candidates, moduleQN, calleeName, importMap)
}

// resolveSuffixMatch handles Strategy 4 — suffix-based matching among multiple candidates.
func (r *FunctionRegistry) resolveSuffixMatch(calleeName, suffix, moduleQN string, importMap map[string]string, candidates []string) ResolutionResult {
	var matches []string
	for _, qn := range candidates {
		if strings.HasSuffix(qn, "."+calleeName) {
			conf := candidateCountPenalty(importAdjustedConfidence(0.55, qn, importMap), len(candidates))
			return ResolutionResult{QualifiedName: qn, Strategy: "suffix_match", Confidence: conf, CandidateCount: len(candidates)}
		}
		if strings.HasSuffix(qn, "."+suffix) {
			matches = append(matches, qn)
		}
	}
	if importMap != nil {
		matches = filterImportReachable(matches, importMap)
	}
	if len(matches) == 1 {
		return ResolutionResult{QualifiedName: matches[0], Strategy: "suffix_match", Confidence: candidateCountPenalty(0.55, len(candidates)), CandidateCount: len(candidates)}
	}
	if len(matches) > 1 {
		best := r.bestByImportDistancePreferMethod(matches, moduleQN, calleeName)
		return ResolutionResult{QualifiedName: best, Strategy: "suffix_match", Confidence: candidateCountPenalty(0.55, len(matches)), CandidateCount: len(matches)}
	}
	return ResolutionResult{}
}

// pickBestCandidate selects the best match from multiple candidates with
// import filtering. Method receiver of the registry so we can apply the
// type-aware tiebreak (prefer Method over Function for method-call shape).
func (r *FunctionRegistry) pickBestCandidate(candidates []string, moduleQN, calleeName string, importMap map[string]string) ResolutionResult {
	if len(candidates) <= 1 {
		return ResolutionResult{}
	}
	filtered := candidates
	if importMap != nil {
		filtered = filterImportReachable(candidates, importMap)
	}
	if len(filtered) == 0 {
		best := r.bestByImportDistancePreferMethod(candidates, moduleQN, calleeName)
		return ResolutionResult{QualifiedName: best, Strategy: "suffix_match", Confidence: candidateCountPenalty(0.55*0.5, len(candidates)), CandidateCount: len(candidates)}
	}
	if len(filtered) == 1 {
		return ResolutionResult{QualifiedName: filtered[0], Strategy: "suffix_match", Confidence: candidateCountPenalty(0.55, len(candidates)), CandidateCount: len(candidates)}
	}
	best := r.bestByImportDistancePreferMethod(filtered, moduleQN, calleeName)
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

// FuzzyResolve attempts a loose match when Resolve() returns "".
// It searches for any registered function whose simple name matches the callee's
// last name segment. Returns the best match (by import distance) with confidence,
// or an empty result and false if no match is found.
//
// Unlike Resolve(), this does not require prefix/import agreement — it purely
// matches on the function name.
func (r *FunctionRegistry) FuzzyResolve(calleeName, moduleQN string, importMap map[string]string) (ResolutionResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Extract the simple name (last segment after dots)
	lookupName := simpleName(calleeName)
	candidates := r.byName[lookupName]

	if len(candidates) == 0 {
		return ResolutionResult{}, false
	}

	// If there's exactly one candidate, use it
	if len(candidates) == 1 {
		conf := 0.40
		if importMap != nil && !isImportReachable(candidates[0], importMap) {
			conf *= 0.5
		}
		return ResolutionResult{
			QualifiedName: candidates[0], Strategy: "fuzzy",
			Confidence: conf, CandidateCount: 1,
		}, true
	}

	// Multiple candidates: filter by import reachability, then pick best by distance
	filtered := candidates
	if importMap != nil {
		filtered = filterImportReachable(candidates, importMap)
	}
	if len(filtered) == 0 {
		// No import-reachable candidates — use original with penalty
		best := bestByImportDistance(candidates, moduleQN)
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
	best := bestByImportDistance(filtered, moduleQN)
	if best == "" {
		return ResolutionResult{}, false
	}
	return ResolutionResult{
		QualifiedName: best, Strategy: "fuzzy",
		Confidence: candidateCountPenalty(0.30, len(filtered)), CandidateCount: len(filtered),
	}, true
}

// LabelOf returns the node label for a qualified name, or "" if not registered.
func (r *FunctionRegistry) LabelOf(qualifiedName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.exact[qualifiedName]
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
