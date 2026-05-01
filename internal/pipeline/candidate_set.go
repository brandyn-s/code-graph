// Package pipeline — candidate_set.go exposes the resolver's pre-tie-break
// candidate-set cardinality on every emitted CALLS-family edge so harness
// metrics can stratify precision by call-site ambiguity.
//
// 2026-05-02 plateau-2 plan, Step 5 — the largest of the five
// instrumentation changes. Mirrors caller_kind.go (Step 3) and
// resolver_rule.go (Step 4); this is the third edge property added by
// the plateau-2 sequence.
//
// Why this exists
// ---------------
// Step 2's LLM-Judge taxonomy (knowledge-base PR #360) found that 60% of
// judged FPs are `same_named_method_disambiguation` — the resolver picked
// the wrong receiver type when the method name resolved to >=2 candidates.
// Step 4's `resolver_rule` exposes WHICH rule fired (cross-package-heuristic,
// receiver-qualified, etc.) but cannot say HOW AMBIGUOUS each individual
// call site was — a rule can be "right" 95% of the time when the call
// site has one candidate and "right" 40% of the time when it has six.
//
// This step exposes the pre-tie-break candidate set: at every call site,
// when the resolver picked one of N candidates, record N. Multiple emitted
// edges can share a call site (rare; resolver is currently single-target),
// and a call site can produce zero edges if resolution failed.
//
// Storage
// -------
// Stored as a number in the edge's `candidate_set_size` property. Indexed
// via the `candidate_set_size_gen` generated column on the edges table
// (see internal/store/store.go), mirroring the existing
// `confidence_tier_gen`, `caller_node_kind_gen`, and `resolver_rule_gen`
// columns. Cypher exposes it as `r.candidate_set_size`.
//
// Per-call-site vs per-edge
// -------------------------
// Conceptually candidate-set-size is a CALL SITE property, not an edge
// property. Today the resolver is single-target — one chosen edge per
// call site — so each emitted edge inherits its site's cardinality
// without ambiguity. If the resolver ever emits multiple edges per site
// (interface-dispatch with explicit alternates) every emitted edge would
// carry the SAME cardinality, which is correct semantically: every
// alternate edge "knew about" the same N candidates.
//
// LSP-resolved calls
// ------------------
// LSP returns ONE definite resolved target with a confidence score; the
// LSP path does not expose the alternates it considered. LSP-resolved
// edges therefore carry candidate_set_size=1 BY DEFINITION — not because
// no other candidates existed, but because we cannot enumerate them
// without invasive C-side refactoring. Documented as a known limitation.
// The Janusian signal lives in the Go-side registry strategies (which
// explicitly enumerate via FunctionRegistry.byName) and in the
// type-dispatch paths.
//
// Confidence: VERIFIED for all registry-strategy emit sites — every
// ResolutionResult already carries CandidateCount today. INFERRED for
// the LSP-resolved path's =1 default (LSP is single-target by design).
package pipeline

const (
	// CandidateSetPropertyName is the JSON key in edge properties that
	// stores the resolver's pre-tie-break candidate-set cardinality.
	// Pinned string — harness baselines and the generated column
	// definition reference it by name.
	CandidateSetPropertyName = "candidate_set_size"

	// CandidateSetSizeUnknown sentinel — used when a code path emits an
	// edge without going through a resolution step that exposes its
	// candidate count. Stored as -1 (rather than 0 or NULL) to keep the
	// "unknown" signal greppable and distinguishable from "1 candidate"
	// in queries. NULL would alias with pre-migration rows; 0 would
	// alias with "resolver found nothing but emitted anyway" which is a
	// real bug class we want surfaced. -1 is the explicit sentinel.
	CandidateSetSizeUnknown = -1

	// CandidateSetSizeLSPDefault — LSP-resolved calls carry size=1 by
	// definition. The C-side LSP returns one resolved target per call
	// site; we cannot enumerate alternates without invasive C-side
	// refactoring. Documented as a limitation in the package comment.
	CandidateSetSizeLSPDefault = 1
)

// candidateSetSizeFromResolution returns the cardinality the resolver
// considered before picking the chosen target. Wraps the existing
// ResolutionResult.CandidateCount field (already populated by every
// FunctionRegistry strategy in resolver.go) to keep the call-site
// surface consistent across strategies.
//
// A non-positive CandidateCount on a successful resolution indicates a
// resolver bug — every successful path in resolver.go sets at least 1.
// We clamp to >= 1 here to keep the property semantically meaningful
// (a resolved edge always saw at least one candidate, namely the one
// picked).
//
// Confidence: VERIFIED — resolver.go's resolveViaImportMap,
// resolveViaSameModule, resolveViaNameLookup, resolveSuffixMatch,
// pickBestCandidate, and FuzzyResolve all set CandidateCount before
// returning a non-empty QualifiedName.
func candidateSetSizeFromResolution(r ResolutionResult) int {
	if r.CandidateCount < 1 {
		return 1
	}
	return r.CandidateCount
}
