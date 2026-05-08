package pipeline

import "strings"

// TypeMap maps variable names to their resolved class QN.
type TypeMap map[string]string

// PerFuncTypeMap maps a function/method QN to the local-variable
// TypeMap visible inside its body (including parameters, `self`, and
// `let` bindings). Keys are caller QNs; values are per-scope name->type
// lookups. Empty key "" is the module-scope TypeMap (used for free-fn
// callers and CALLS_PSEUDO sites that have no enclosing function).
type PerFuncTypeMap map[string]TypeMap

// FieldTypeMap maps "<structQN>.<fieldName>" -> field-type-class QN.
// Populated by walking struct/enum/union field declarations across
// every extracted file in the project. Used by the resolver to walk
// chains like `obj.field.method()` — once obj's type is known via
// the per-function TypeMap, FieldTypeMap supplies field's type so the
// resolver can look up `.method` on the field's type.
type FieldTypeMap map[string]string

// ReturnTypeMap maps function QN to the return type name.
type ReturnTypeMap map[string]string

// ResolveAsClassReason names the specific failure mode when
// resolveAsClassWithReason returns an empty QN. Used by Phase D
// instrumentation (2026-05-08) to split the previously-aggregate
// `traitQN-empty` skip reason in implements.go into its three
// downstream causes — without changing any caller behavior.
type ResolveAsClassReason string

const (
	// ResolveOK — the name resolved to a class-like QN via the existing
	// 9 resolver strategies. No failure, no fallback fired.
	ResolveOK ResolveAsClassReason = ""
	// ResolveOKViaFallbackFromEmpty — Phase A (2026-05-08, plan
	// 2026-05-08-d-implement-actix-extension). The existing 9 strategies
	// returned no QN, but the byName + class-like-label filter found
	// exactly one match. Tracks how many of PR #262's 736 resolve-empty
	// cases this fallback closes.
	ResolveOKViaFallbackFromEmpty ResolveAsClassReason = "ok:fallback-from-empty"
	// ResolveOKViaFallbackFromMismatch — Phase A. Existing strategies
	// returned a QN with a non-class-like label (e.g., Variable for a
	// TS story export named "Default"); the byName fallback found a
	// class-like-labeled candidate instead. Tracks how many of PR #262's
	// 153 label-mismatch cases this fallback closes.
	ResolveOKViaFallbackFromMismatch ResolveAsClassReason = "ok:fallback-from-mismatch"
	// ResolveEmpty — registry.Resolve returned no QN AND the byName
	// fallback found zero or multiple class-like candidates (so we
	// can't disambiguate without making something up).
	ResolveEmpty ResolveAsClassReason = "resolve-empty"
	// ResolveLabelMissing — registry.Resolve returned a QN but
	// registry.exact has no label entry for that QN. Should be rare;
	// indicates a registry-population gap.
	ResolveLabelMissing ResolveAsClassReason = "label-missing"
	// ResolveLabelMismatch — registry.Resolve returned a QN with a
	// non-class-like label, AND the byName fallback found zero or
	// multiple class-like candidates.
	ResolveLabelMismatch ResolveAsClassReason = "label-mismatch"
)

// isClassLikeLabel checks whether a registry label denotes a type-like
// node that's a valid trait/struct/class IMPLEMENTS target. Centralized
// so the existing 2-step flow and the Phase A fallback both agree.
func isClassLikeLabel(label string) bool {
	switch label {
	case "Class", "Type", "Interface", "Enum", "Struct", "Trait":
		return true
	}
	return false
}

// resolveAsClassWithReason mirrors resolveAsClass but additionally
// returns a structured reason when the QN is empty. Phase D-Instrument
// (2026-05-08) callers use this to split traitQN-empty into the three
// failure sub-buckets above.
//
// resolveAsClass remains as the legacy wrapper (forwards here, drops
// the reason) so existing callers don't change.
func resolveAsClassWithReason(name string, registry *FunctionRegistry, moduleQN string, importMap map[string]string) (string, ResolveAsClassReason) {
	result := registry.Resolve(name, moduleQN, importMap)

	// Step 1: existing 2-step flow — Resolve, then label-check.
	if result.QualifiedName != "" {
		registry.mu.RLock()
		label, exists := registry.exact[result.QualifiedName]
		registry.mu.RUnlock()
		if exists && isClassLikeLabel(label) {
			return result.QualifiedName, ResolveOK
		}
	}

	// Determine the reason the existing flow failed — needed both for
	// fallback bucket attribution and for the no-fallback-hit return.
	var primaryReason ResolveAsClassReason
	switch {
	case result.QualifiedName == "":
		primaryReason = ResolveEmpty
	default:
		registry.mu.RLock()
		_, exists := registry.exact[result.QualifiedName]
		registry.mu.RUnlock()
		if !exists {
			primaryReason = ResolveLabelMissing
		} else {
			primaryReason = ResolveLabelMismatch
		}
	}

	// Step 2 (Phase A, 2026-05-08): label-aware project-wide fallback.
	// Strip module prefix from name (e.g., "redacted_core::Foo" → "Foo",
	// "obj.Method" → "Method"). Then scan registry.byName for entries
	// with a class-like label. If exactly one match, return it.
	//
	// Why: PR #262's PSM measurement showed 736 resolve-empty + 153
	// label-mismatch traitQN failures. Sample failing impls: `Default`
	// resolves to a TS Story Export (Variable label), `Clone` resolves
	// to a Go Method, `Debug` resolves to a markdown Section. The
	// existing 9 strategies pick the wrong-label hit via name-uniqueness
	// because they're not filtering by class-like-label. This fallback
	// fires only for resolveAsClassWithReason callers (IMPLEMENTS),
	// not for the generic Resolve path used by CALLS — so receiver-type
	// binding and call resolution are unchanged.
	bareName := name
	if idx := strings.LastIndex(bareName, "::"); idx >= 0 {
		bareName = bareName[idx+2:]
	}
	if idx := strings.LastIndex(bareName, "."); idx >= 0 {
		bareName = bareName[idx+1:]
	}
	if bareName == "" {
		return "", primaryReason
	}

	registry.mu.RLock()
	candidates := registry.byName[bareName]
	classLikeMatches := make([]string, 0, 4)
	for _, qn := range candidates {
		if label, ok := registry.exact[qn]; ok && isClassLikeLabel(label) {
			classLikeMatches = append(classLikeMatches, qn)
		}
	}
	registry.mu.RUnlock()

	if len(classLikeMatches) == 0 {
		// No class-like candidates by name. PSM measurement: this is
		// the dominant case for external stdlib traits (`From`, `TryFrom`,
		// `Display`, etc.) — they're not registered as Interface nodes
		// because std isn't in the indexed graph. No fallback possible
		// without external-crate awareness (deferred to a future plan).
		return "", primaryReason
	}

	var picked string
	if len(classLikeMatches) == 1 {
		picked = classLikeMatches[0]
	} else {
		// Phase A v2 (2026-05-08): crate-locality tiebreaker for ambiguous
		// matches. PSM has many trait-shaped names with multiple Interface
		// definitions (e.g., separate `Command` traits in assetman / cmd /
		// healthcheck). Pick the candidate whose qualified name shares the
		// longest common prefix with moduleQN — that's the same-crate
		// candidate by construction. Same heuristic PR #257 (D1) used for
		// HTTP handler resolution.
		bestLen := -1
		for _, qn := range classLikeMatches {
			n := commonStringPrefixLen(moduleQN, qn)
			if n > bestLen {
				bestLen = n
				picked = qn
			}
		}
		// If tied at bestLen=0 (no shared prefix at all — shouldn't happen
		// since every node shares the project prefix), fall through with
		// `picked` = first iterated candidate. Map iteration order in Go
		// is randomized; this is the only nondeterministic case and only
		// affects truly cross-project name collisions.
	}

	// Tag with the original failure mode so the implementsRust.summary
	// can attribute closures to specific sub-buckets.
	switch primaryReason {
	case ResolveEmpty:
		return picked, ResolveOKViaFallbackFromEmpty
	case ResolveLabelMissing, ResolveLabelMismatch:
		return picked, ResolveOKViaFallbackFromMismatch
	}
	return picked, ResolveOK
}

// commonStringPrefixLen returns the number of leading bytes a and b share.
// Used by resolveAsClassWithReason to pick the same-crate trait when
// multiple class-like candidates share the bare name. Local copy avoids
// importing `commonPrefixLen` from another package; the helper is short
// enough that the duplication is preferable to introducing a cross-package
// helper-only import.
func commonStringPrefixLen(a, b string) int {
	n := 0
	max := len(a)
	if len(b) < max {
		max = len(b)
	}
	for n < max && a[n] == b[n] {
		n++
	}
	return n
}

// resolveAsClass checks if a name refers to a class-like node in the
// registry (Class / Type / Interface / Enum / Struct / Trait).
//
// A2 (2026-05-07): Struct and Trait added to the accepted label set.
// The Rust extractor labels traits as "Trait" and structs as "Struct"
// (NOT "Class"); without these labels in the accepted set,
// `implementsRust` (in implements.go) and the IMPLEMENTS pass in
// pipeline.go silently dropped every Rust impl-block — both
// `resolveAsClass(traitName)` AND `resolveAsClass(structName)`
// returned empty, so the IMPLEMENTS edge was never emitted. PSM
// 2026-05-07 baseline: 365 IMPLEMENTS edges across 2,065 Rust files
// (CradlepointApiClientSync IMPLEMENTS CradlepointClientSync was
// missing) — explained entirely by this label-mismatch.
//
// Risk: anywhere `resolveAsClass` was used as a way to detect
// "is this name a CLASS specifically (not a Trait/Struct)" — but
// every existing call site uses `resolveAsClass` to find the QN of
// a type for IMPLEMENTS / EXTENDS / receiver-type binding. None of
// these care whether the type is technically a Class vs Struct vs
// Trait — they all behave identically.
func resolveAsClass(name string, registry *FunctionRegistry, moduleQN string, importMap map[string]string) string {
	qn, _ := resolveAsClassWithReason(name, registry, moduleQN, importMap)
	return qn
}
