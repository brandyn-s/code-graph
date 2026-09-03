package pipeline

import (
	"os"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// LanguageResolverConfig holds the per-language gates the resolver
// consults during call resolution. Built once per (language, env)
// pair by ResolverConfigFor; consult the struct's bool fields at
// resolve time instead of scattered os.Getenv calls.
//
// PR1 of the resolver consolidation arc — INTRODUCES this type and
// the loader, with bit-equivalence tests proving the new path
// produces the same outputs as the existing scattered env reads.
// PR2 will migrate the 17 call sites in resolver.go +
// pipeline_cbm.go to consume the struct. Until then, this type is
// dead code from production's perspective; only tests exercise it.
//
// Backward-compatibility contract: the four env vars listed below
// remain the public configuration API. Operator runbooks, CI
// configs, and the Phase A””-2 / E playbooks continue to work
// unchanged. Internal call sites just stop reading os.Getenv
// inline.
type LanguageResolverConfig struct {
	// DropLooseCrossPackage drops emissions in the
	// cross-package-unique-name and cross-package-suffix
	// resolver-rule sub-buckets. Production default: unset (emit).
	// Flip via RESOLVER_DROP_LOOSE_CROSS_PACKAGE=<any non-empty>.
	// The eval harness sets this for Python fixtures (per CLAUDE.md
	// "Resolver env vars") to suppress catastrophic-precision
	// emissions without affecting Go. No language scoping at the
	// read site today; the struct preserves that.
	DropLooseCrossPackage bool

	// RequireImportsForLooseCrossPackage drops candidates that
	// aren't import-reachable from the call site's module via an
	// explicit IMPORTS edge. Default by language: Rust=true,
	// others=false (Phase F, 2026-05-09).
	// RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE=<non-empty>
	// forces true for all languages.
	RequireImportsForLooseCrossPackage bool

	// DropFuzzyJanusianChains drops fuzzy resolutions whose
	// candidate set matches the empirical Janusian-co-hallucination
	// signature. Default by language: Python=true, others=false
	// (Phase E, 2026-05-14, after PSM Rust assetman regressed
	// -2.2pp F1 under global default-on).
	// RESOLVER_DROP_FUZZY_JANUSIAN_CHAINS=1/true/yes forces ON for
	// all languages; =0/false/no forces OFF for all languages;
	// unset = language default.
	DropFuzzyJanusianChains bool

	// EmitEnumVariantAsParent rewrites Enum::Variant call sites
	// (where the variant child node doesn't exist in the registry)
	// to emit CALLS edges targeting the parent Enum's QN. Default
	// false (off); opt-in via RESOLVER_EMIT_ENUM_VARIANT_AS_PARENT=
	// <non-empty> (Phase A''''-2 opt-in, 2026-05-14). No language
	// scoping today.
	EmitEnumVariantAsParent bool
}

// ResolverConfigFor returns the effective resolver config for the
// given language, applying per-language defaults and any operator
// env-var overrides. Read sites that today call os.Getenv inline
// should (in PR2) be migrated to call this once per resolve pass
// and consult the returned struct's fields.
//
// IMPORTANT: this function MUST produce the same boolean values as
// the existing scattered env reads for every (env, language) pair
// — that invariant is pinned by TestResolverConfig_BitEquivalence_*
// tests below. Drift would silently change indexing behavior and
// invalidate the defended Loc-Bench baseline (CLAUDE.md). Any future
// change here that intentionally diverges from the legacy helpers
// must also update those helpers in the same PR and update the
// test matrix.
func ResolverConfigFor(language lang.Language) LanguageResolverConfig {
	return LanguageResolverConfig{
		DropLooseCrossPackage:              envFlagPresent("RESOLVER_DROP_LOOSE_CROSS_PACKAGE"),
		RequireImportsForLooseCrossPackage: resolveRequireImports(language),
		DropFuzzyJanusianChains:            resolveFuzzyJanusianChainsDrop(language),
		EmitEnumVariantAsParent:            envFlagPresent("RESOLVER_EMIT_ENUM_VARIANT_AS_PARENT"),
	}
}

// envFlagPresent returns true if the env var is set to any non-empty
// string. Mirrors the `os.Getenv(X) != ""` pattern used at every
// presence-based read site.
func envFlagPresent(name string) bool {
	return os.Getenv(name) != ""
}

// resolveRequireImports mirrors shouldRequireImportsForLooseCrossPackage
// (resolver.go) — Rust gets true by default, env var forces all.
func resolveRequireImports(language lang.Language) bool {
	if envFlagPresent("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE") {
		return true
	}
	return language == lang.Rust
}

// resolveFuzzyJanusianChainsDrop mirrors shouldDropFuzzyJanusianChains
// (resolver.go). Re-reads the env var rather than relying on the
// init-time package var so test t.Setenv calls take effect. The
// migration in PR2 will switch the production call site between these
// two patterns; the bit-equivalence test below verifies they agree.
func resolveFuzzyJanusianChainsDrop(language lang.Language) bool {
	switch parseEnvPolicy("RESOLVER_DROP_FUZZY_JANUSIAN_CHAINS") {
	case envPolicyForceOn:
		return true
	case envPolicyForceOff:
		return false
	}
	return language == lang.Python
}
