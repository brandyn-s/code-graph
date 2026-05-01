// Package pipeline — resolver_rule.go classifies which resolver rule emitted
// a given CALLS-family edge. Mirrors caller_kind.go (Step 3 of the
// 2026-05-02 plateau-2 plan); this is Step 4.
//
// Why this exists
// ---------------
// Step 2's LLM-Judge taxonomy (knowledge-base PR #360) found that the
// dominant FP class on the Go fixture is `same_named_method_disambiguation`
// (60% of judged sample) and the dominant FN class is
// `cross_package_heuristic_overreach` (83% of FNs — all 5 of the
// `Server.handleIndexRepository → tools.*` misses). Today these FPs and
// FNs are aggregate noise: the headline F1 lumps every CALLS edge
// together, the per-project breakdown shows per-subset variance, and the
// Step-3 caller-kind cut shows per-caller-shape variance — but the
// resolver_rule cut is the dimension that exposes WHICH RULE chose the
// wrong target.
//
// Every CALLS-family emit site picks ONE rule it represents. Rule labels
// are mutually exclusive at emit time — if a site is genuinely ambiguous,
// pick the dominant one and document why (resolver entanglement is its
// own signal worth surfacing rather than papering over).
//
// Modal upgrades (CALLS → CALLS_EXTERNAL when the target is an LSP stub;
// CALLS_PSEUDO for synthetic module-level callers) override the original
// resolver-rule label since the modal classification is the dominant
// signal a power user filters on. The override is applied in
// `pipeline.go::buildEdgesFromResults` once the stub-target check has
// run; the resolver picks the original rule, the modal-upgrade path
// overrides to `modal-external`/`modal-pseudo` if applicable.
//
// Confidence: VERIFIED for the static rules below from reading the
// resolver source (resolver.go, pipeline_cbm.go, pipeline.go). LSP
// strategy → rule mapping VERIFIED from internal/cbm/lsp_test.go which
// pins the strategy strings used by the C-side LSP path.
package pipeline

// ResolverRule enumerates the resolver pathway that emitted a CALLS-family
// edge. Stored as a string in the edge's `resolver_rule` property — Cypher
// exposes it as an edge attribute, json_extract('$.resolver_rule') keeps
// the SQL path simple, and a generated column on the edges table
// (`resolver_rule_gen`) gives indexed access for harness queries.
//
// Buckets are stable identifiers — never rename them; harness baselines
// reference them by string.
const (
	// ResolverRuleExactQN — direct fully-qualified-name match. Today only
	// the LSP-resolved path (collectLSPResolvedEdges) when the LSP
	// strategy is neither receiver- nor interface-dispatch labels here.
	// Strategy strings: anything from cbm.ResolvedCall.Strategy that
	// doesn't start with "lsp_type_", "lsp_embed_", or "lsp_interface_".
	ResolverRuleExactQN = "exact-qn-match"

	// ResolverRuleReceiverQualified — method call resolved via receiver
	// type. LSP strategies "lsp_type_dispatch" and "lsp_embed_dispatch"
	// route here (the C-side LSP resolves the obj's type then looks up
	// method on the type, embedded type included).
	ResolverRuleReceiverQualified = "receiver-qualified"

	// ResolverRuleInterfaceDispatch — Go interface-satisfaction or
	// type-dispatch path. LSP strategies "lsp_interface_dispatch" and
	// "lsp_interface_resolve" route here. Also: the Go-side
	// `resolveCallWithTypes` "type_dispatch" strategy (TypeMap lookup
	// produces classQN+method that exists in the registry).
	ResolverRuleInterfaceDispatch = "interface-dispatch"

	// ResolverRuleSelfMethod — method call on the same instance, e.g.
	// Python's `self.x()` resolved against the enclosing class QN. Fires
	// in resolveCallEdge when calleeName starts with "self.".
	ResolverRuleSelfMethod = "self-method"

	// ResolverRuleSamePackageShadow — same-package symbol resolution.
	// Registry "same_module" strategy: callee was found at moduleQN +
	// "." + name (the caller's own module).
	ResolverRuleSamePackageShadow = "same-package-shadow"

	// ResolverRuleCrossPackageHeuristic — imported-package call where
	// the resolver matches on heuristics rather than exact-resolved
	// types. Covers registry strategies "import_map", "import_map_suffix",
	// "unique_name", and "suffix_match" — all three rely on the prefix
	// (or suffix) being import-reachable plus a name match in the
	// project-wide registry. This is the rule that produces the
	// dominant Step 2 FN class (`cross_package_heuristic_overreach`).
	ResolverRuleCrossPackageHeuristic = "cross-package-heuristic"

	// ResolverRulePackageBlockFallback — emission where the caller is a
	// package- or file-level block rather than a real function scope.
	// Today this case is fully subsumed by ResolverRuleModalPseudo
	// (which fires when edgeType == CALLS_PSEUDO). Reserved for future
	// extractor enhancements (e.g. var-init or type-decl emission paths
	// that don't go through CALLS_PSEUDO).
	ResolverRulePackageBlockFallback = "package-block-fallback"

	// ResolverRuleFuzzyResolve — last-resort name match via
	// FunctionRegistry.FuzzyResolve (registry "fuzzy" strategy). Fires
	// when neither the type-dispatch path nor the structured registry
	// strategies produced a candidate — the resolver falls back to
	// matching purely on simple name and picks the best candidate by
	// import distance. Lowest pre-emit confidence; a high share of FPs
	// here is a known failure mode.
	ResolverRuleFuzzyResolve = "fuzzy-resolve"

	// ResolverRuleModalExternal — CALLS_EXTERNAL emission (LSP-resolved
	// external symbol; target is a synthesized stub). Set in
	// buildEdgesFromResults when the stub-target check upgrades a CALLS
	// edge to CALLS_EXTERNAL. Overrides the original rule because the
	// "external" modal classification is the dominant signal.
	ResolverRuleModalExternal = "modal-external"

	// ResolverRuleModalPseudo — CALLS_PSEUDO emission (synthetic
	// module-default caller). Set when the resolver substitutes
	// moduleQN for an empty EnclosingFuncQN. Overrides whatever the
	// underlying registry/LSP rule would have chosen because the
	// pseudo-caller property is the dominant signal.
	ResolverRuleModalPseudo = "modal-pseudo"

	// ResolverRuleUnresolvedEmitted — emitted despite no confident
	// resolution path firing. Reserved for diagnostics; today the
	// emission paths all bail out before reaching this state (no
	// ambiguous emits). Non-zero counts in production indicate a
	// resolver bug.
	ResolverRuleUnresolvedEmitted = "unresolved-emitted"

	// ResolverRuleUnknown — fallback when no classification rule fires.
	// Should be 0% on healthy inputs; non-zero counts indicate either a
	// new emit path we haven't covered or an unexpected strategy string
	// from CBM/LSP. Acts as the safe default for future extractor
	// changes.
	ResolverRuleUnknown = "unknown"
)

// resolverRuleFromLSPStrategy maps a cbm.ResolvedCall.Strategy string to
// the matching resolver_rule. Pinned to the strategy values defined
// in internal/cbm/lsp.c and exercised by internal/cbm/lsp_test.go:
//
//   - "lsp_type_dispatch"      → receiver-qualified
//   - "lsp_embed_dispatch"     → receiver-qualified
//   - "lsp_interface_dispatch" → interface-dispatch
//   - "lsp_interface_resolve"  → interface-dispatch
//   - any other (incl. empty)  → exact-qn-match (LSP path's default;
//     the LSP only emits a ResolvedCall after type-aware resolution
//     succeeded, so a non-matching strategy still represents an
//     exact-resolved QN).
//
// Confidence: VERIFIED. The four pinned strategies are the only ones
// asserted in lsp_test.go (every match site in the file). Future LSP
// strategies that don't match either dispatch family fall through to
// exact-qn-match — safe semantic default; harness can flag novel
// strategy strings via low precision on `exact-qn-match`.
func resolverRuleFromLSPStrategy(strategy string) string {
	switch strategy {
	case "lsp_type_dispatch", "lsp_embed_dispatch":
		return ResolverRuleReceiverQualified
	case "lsp_interface_dispatch", "lsp_interface_resolve":
		return ResolverRuleInterfaceDispatch
	default:
		return ResolverRuleExactQN
	}
}

// resolverRuleFromRegistryStrategy maps a FunctionRegistry resolution
// strategy string to the matching resolver_rule. Strategy values are
// defined in internal/pipeline/resolver.go:
//
//   - "type_dispatch"        → interface-dispatch
//     (TypeMap-driven obj.method resolution; same semantic as Go's
//     receiver-on-known-type dispatch.)
//   - "same_module"          → same-package-shadow
//     (callee found in caller's own module — same-package resolution.)
//   - "import_map" / "import_map_suffix" / "unique_name" / "suffix_match"
//                            → cross-package-heuristic
//     (resolved via cross-package import-map plus heuristic suffix or
//     project-wide name lookup. This is the rule emitting the dominant
//     FN class per Step 2's LLM-Judge taxonomy.)
//   - "fuzzy"                → fuzzy-resolve
//     (last-resort name-only match via FuzzyResolve.)
//   - any other              → unknown
//     (defensive — should never fire on healthy input; non-zero counts
//     indicate a resolver path we haven't covered.)
//
// Confidence: VERIFIED for all four named registry strategies; new
// strategies fall through to ResolverRuleUnknown as a tripwire.
func resolverRuleFromRegistryStrategy(strategy string) string {
	switch strategy {
	case "type_dispatch":
		return ResolverRuleInterfaceDispatch
	case "same_module":
		return ResolverRuleSamePackageShadow
	case "import_map", "import_map_suffix", "unique_name", "suffix_match":
		return ResolverRuleCrossPackageHeuristic
	case "fuzzy":
		return ResolverRuleFuzzyResolve
	default:
		return ResolverRuleUnknown
	}
}
