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

	// ResolverRuleCrossPackageImportMap — registry "import_map" strategy.
	// Resolved through an EXPLICIT import: the imported alias was looked
	// up in the per-file import-map and the alias' definition was found.
	// This is the precise sub-bucket of the cross-package family;
	// 2026-05-06 baselines show 0.88-0.95 precision on Go fixtures.
	ResolverRuleCrossPackageImportMap = "cross-package-import-map"

	// ResolverRuleCrossPackageUniqueName — registry "unique_name" strategy.
	// Resolved by project-wide unique-name lookup (callee's simple name
	// has exactly one definition project-wide). Distinct from
	// import-map because it doesn't require the call site to import the
	// definition's module — the uniqueness of the name is the only
	// resolution signal.
	ResolverRuleCrossPackageUniqueName = "cross-package-unique-name"

	// ResolverRuleCrossPackageSuffix — registry "suffix_match" or
	// "import_map_suffix" strategies. The DANGEROUS sub-bucket: the
	// resolver matched the callee's simple name against the suffix of a
	// project-wide qualified name. This is the fall-through path that
	// produced the PR #165 phantom regression class (155+ phantom edges
	// from normalized Rust `Foo::new` matching against unrelated `.new`
	// methods). Drop-on-no-match is the targeted Rec 1 fix for this
	// sub-bucket. 2026-05-06 baselines show this bucket has 0.07-0.23
	// precision on Python adversarial fixtures (essentially noise).
	ResolverRuleCrossPackageSuffix = "cross-package-suffix"

	// ResolverRuleCrossPackageHeuristic — DEPRECATED 2026-05-06. The
	// original lumped bucket was split into ImportMap / UniqueName /
	// Suffix because per-fixture precision varied by an order of
	// magnitude (0.07 on flask vs 0.95 on code-graph), which the lumped
	// bucket couldn't surface. New code should NOT emit this string;
	// existing code paths that historically referenced it have been
	// updated to use the appropriate sub-bucket. Constant retained as
	// a compile-time anchor for legacy baselines (which carry the old
	// string in their JSON dumps) and as the canonical name for the
	// FAMILY across all three sub-buckets in helper predicates like
	// `isCrossPackageRule`.
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
//   - "import_map"           → cross-package-import-map
//     (precise: imported-alias lookup + alias-definition resolved via
//     per-file import map.)
//   - "unique_name"          → cross-package-unique-name
//     (project-wide unique-name lookup — callee's simple name has
//     exactly one definition project-wide.)
//   - "import_map_suffix" / "suffix_match"
//     → cross-package-suffix
//     (fall-through path: callee's simple name matched against suffix
//     of a project-wide QN. The dangerous sub-bucket — produced the
//     PR #165 phantom regression class. Drop-on-no-match target.)
//   - "fuzzy"                → fuzzy-resolve
//     (last-resort name-only match via FuzzyResolve.)
//   - any other              → unknown
//     (defensive — should never fire on healthy input; non-zero counts
//     indicate a resolver path we haven't covered.)
//
// 2026-05-06 split: previously all four cross-package strategies
// returned the lumped ResolverRuleCrossPackageHeuristic. Per-fixture
// precision varied by an order of magnitude (0.07 on flask-adversarial
// vs 0.95 on code-graph-go), which the lumped bucket couldn't surface.
// The split lets harness queries distinguish which sub-strategy is
// emitting the phantoms and lets the resolver apply drop-on-no-match
// selectively to the suffix-match variants without cratering recall on
// the precise `import_map` path.
//
// Confidence: VERIFIED for all four named registry strategies; new
// strategies fall through to ResolverRuleUnknown as a tripwire.
func resolverRuleFromRegistryStrategy(strategy string) string {
	switch strategy {
	case "type_dispatch":
		return ResolverRuleInterfaceDispatch
	case "lsp_local_inherited":
		// Hybrid lsp_local tier: receiver type known, method found on a base
		// class. Receiver-qualified like the Go LSP type dispatch.
		return ResolverRuleReceiverQualified
	case "same_module":
		return ResolverRuleSamePackageShadow
	case "import_map":
		return ResolverRuleCrossPackageImportMap
	case "unique_name":
		return ResolverRuleCrossPackageUniqueName
	case "import_map_suffix", "suffix_match":
		return ResolverRuleCrossPackageSuffix
	case "type_static_dispatch":
		// Rust `Foo::new` resolved to <class_qn>.new structurally.
		// Single-target by construction; semantically an exact match.
		return ResolverRuleExactQN
	case "fuzzy":
		return ResolverRuleFuzzyResolve
	default:
		return ResolverRuleUnknown
	}
}

// isCrossPackageRule returns true if `rule` is any of the three
// cross-package sub-buckets (or the legacy lumped name, for safety on
// rows from older indexes). Used by emit-side checks (e.g. the Janusian
// candidate-set ambiguity penalty in pipeline_cbm.go) that need to fire
// on the FAMILY, not a specific sub-bucket.
func isCrossPackageRule(rule string) bool {
	switch rule {
	case ResolverRuleCrossPackageImportMap,
		ResolverRuleCrossPackageUniqueName,
		ResolverRuleCrossPackageSuffix,
		ResolverRuleCrossPackageHeuristic:
		return true
	}
	return false
}

// isLooseCrossPackageRule returns true if `rule` is one of the two
// LOW-PRECISION cross-package sub-buckets — `unique-name` (project-wide
// simple-name lookup) or `suffix` (suffix-match fall-through). These
// are the buckets the 2026-05-06 sub-bucket-split measurement found
// have catastrophic precision on Python adversarial fixtures (0.00-0.35)
// and merely-poor precision on Go (0.48 on gin's suffix bucket).
//
// The PRECISE sub-bucket `cross-package-import-map` is excluded — it
// resolves through an explicit imported-alias binding and is high-
// confidence by construction.
//
// Used by RESOLVER_DROP_LOOSE_CROSS_PACKAGE env-var gate (Rec 1
// implementation, 2026-05-06): when the env var is set, edges in these
// buckets are dropped at emit time. The eval harness sets the env var
// for Python fixtures (where the buckets are noise) and leaves it unset
// for Go (where unique-name carries 88-95% precision and dropping
// would crater recall by 57-68% of TPs).
func isLooseCrossPackageRule(rule string) bool {
	switch rule {
	case ResolverRuleCrossPackageUniqueName, ResolverRuleCrossPackageSuffix:
		return true
	}
	return false
}
