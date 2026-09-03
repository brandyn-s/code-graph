package pipeline

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// allLanguagesForResolverConfig is the set of languages the resolver
// config must remain stable across. Listed explicitly (not derived
// from lang.AllLanguages) so adding a new language doesn't silently
// add an unverified row to the matrix — author has to consciously
// extend this list and confirm the defaults are right.
var allLanguagesForResolverConfig = []lang.Language{
	lang.Python,
	lang.Go,
	lang.Rust,
	lang.TypeScript,
	lang.JavaScript,
	lang.Java,
	lang.CPP,
}

// TestResolverConfig_DefaultsPerLanguage pins the per-language
// defaults table: with every env var unset, ResolverConfigFor must
// return the expected struct for each language. Drift here would
// silently change indexing semantics.
func TestResolverConfig_DefaultsPerLanguage(t *testing.T) {
	// Ensure clean env for every case.
	t.Setenv("RESOLVER_DROP_LOOSE_CROSS_PACKAGE", "")
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "")
	t.Setenv("RESOLVER_DROP_FUZZY_JANUSIAN_CHAINS", "")
	t.Setenv("RESOLVER_EMIT_ENUM_VARIANT_AS_PARENT", "")

	cases := []struct {
		lang lang.Language
		want LanguageResolverConfig
	}{
		// Python: only DropFuzzyJanusianChains defaults true.
		{lang.Python, LanguageResolverConfig{
			DropFuzzyJanusianChains: true,
		}},
		// Rust: only RequireImportsForLooseCrossPackage defaults true.
		{lang.Rust, LanguageResolverConfig{
			RequireImportsForLooseCrossPackage: true,
		}},
		// Go, TS, JS, Java, C++: all defaults false.
		{lang.Go, LanguageResolverConfig{}},
		{lang.TypeScript, LanguageResolverConfig{}},
		{lang.JavaScript, LanguageResolverConfig{}},
		{lang.Java, LanguageResolverConfig{}},
		{lang.CPP, LanguageResolverConfig{}},
	}
	for _, c := range cases {
		got := ResolverConfigFor(c.lang)
		if got != c.want {
			t.Errorf("language=%v: got %+v, want %+v", c.lang, got, c.want)
		}
	}
}

// TestResolverConfig_BitEquivalence_RequireImports proves the new
// loader returns the same boolean as the existing
// shouldRequireImportsForLooseCrossPackage helper across the env-var
// matrix × all languages. Failure here means PR2's call-site
// migration would shift behavior.
func TestResolverConfig_BitEquivalence_RequireImports(t *testing.T) {
	values := []string{"", "1", "0", "true", "false", "yes", "no", "on", "off"}
	for _, v := range values {
		t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", v)
		for _, l := range allLanguagesForResolverConfig {
			legacy := shouldRequireImportsForLooseCrossPackage(l)
			fromConfig := ResolverConfigFor(l).RequireImportsForLooseCrossPackage
			if legacy != fromConfig {
				t.Errorf("env=%q lang=%v: legacy=%v fromConfig=%v", v, l, legacy, fromConfig)
			}
		}
	}
}

// TestResolverConfig_BitEquivalence_FuzzyJanusianChains proves the
// new loader returns the same boolean as shouldDropFuzzyJanusianChains
// across the env-policy tri-state × all languages.
func TestResolverConfig_BitEquivalence_FuzzyJanusianChains(t *testing.T) {
	// Cover unset, force-on values (1/true/yes/on/anything-else),
	// and force-off values (anything starting with 0/f/F/n/N).
	values := []string{"", "1", "true", "yes", "TRUE", "0", "false", "FALSE", "no", "n"}
	for _, v := range values {
		t.Setenv("RESOLVER_DROP_FUZZY_JANUSIAN_CHAINS", v)
		for _, l := range allLanguagesForResolverConfig {
			// shouldDropFuzzyJanusianChains reads the package-level var
			// fuzzyJanusianChainEnvPolicy which is computed at init time
			// from the original env, so it won't reflect t.Setenv.
			// Compare the new loader against a fresh parse instead.
			expected := freshDecideFuzzyJanusianChains(l, v)
			fromConfig := ResolverConfigFor(l).DropFuzzyJanusianChains
			if expected != fromConfig {
				t.Errorf("env=%q lang=%v: expected=%v fromConfig=%v", v, l, expected, fromConfig)
			}
		}
	}
}

// freshDecideFuzzyJanusianChains is a test-local reference
// implementation matching the documented contract of
// shouldDropFuzzyJanusianChains. Re-parses the env value instead of
// reading the init-cached var so t.Setenv values are honored.
func freshDecideFuzzyJanusianChains(language lang.Language, envValue string) bool {
	if envValue == "" {
		return language == lang.Python
	}
	switch envValue[0] {
	case '0', 'f', 'F', 'n', 'N':
		return false
	}
	return true
}

// TestResolverConfig_DropLooseCrossPackage_PresenceOnly verifies the
// simple presence-based env var has no language scoping and reacts
// to any non-empty value.
func TestResolverConfig_DropLooseCrossPackage_PresenceOnly(t *testing.T) {
	for _, v := range []string{"", "1", "0", "true", "false", "anything"} {
		t.Setenv("RESOLVER_DROP_LOOSE_CROSS_PACKAGE", v)
		expected := v != ""
		for _, l := range allLanguagesForResolverConfig {
			got := ResolverConfigFor(l).DropLooseCrossPackage
			if got != expected {
				t.Errorf("env=%q lang=%v: expected=%v got=%v", v, l, expected, got)
			}
		}
	}
}

// TestResolverConfig_EmitEnumVariantAsParent_PresenceOnly same shape
// as DropLooseCrossPackage — any non-empty value flips on.
func TestResolverConfig_EmitEnumVariantAsParent_PresenceOnly(t *testing.T) {
	for _, v := range []string{"", "1", "0", "true", "false", "anything"} {
		t.Setenv("RESOLVER_EMIT_ENUM_VARIANT_AS_PARENT", v)
		expected := v != ""
		for _, l := range allLanguagesForResolverConfig {
			got := ResolverConfigFor(l).EmitEnumVariantAsParent
			if got != expected {
				t.Errorf("env=%q lang=%v: expected=%v got=%v", v, l, expected, got)
			}
		}
	}
}

// TestResolverConfig_CombinedOverrides verifies multiple env vars
// can be set simultaneously and produce the expected struct shape.
// Catches accidental cross-field coupling in the loader.
func TestResolverConfig_CombinedOverrides(t *testing.T) {
	t.Setenv("RESOLVER_DROP_LOOSE_CROSS_PACKAGE", "1")
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "1")
	t.Setenv("RESOLVER_DROP_FUZZY_JANUSIAN_CHAINS", "1")
	t.Setenv("RESOLVER_EMIT_ENUM_VARIANT_AS_PARENT", "1")
	for _, l := range allLanguagesForResolverConfig {
		got := ResolverConfigFor(l)
		want := LanguageResolverConfig{
			DropLooseCrossPackage:              true,
			RequireImportsForLooseCrossPackage: true,
			DropFuzzyJanusianChains:            true,
			EmitEnumVariantAsParent:            true,
		}
		if got != want {
			t.Errorf("language=%v all-on: got %+v, want %+v", l, got, want)
		}
	}
}

// TestResolverConfig_ForceOffFuzzy_OverridesPythonDefault confirms
// the force-off path of the fuzzy-Janusian env policy beats Python's
// language default. This is the operator's escape hatch when the
// Python default-on bites a specific repo and they want to disable
// it without code changes.
func TestResolverConfig_ForceOffFuzzy_OverridesPythonDefault(t *testing.T) {
	for _, v := range []string{"0", "false", "no", "FALSE", "N"} {
		t.Setenv("RESOLVER_DROP_FUZZY_JANUSIAN_CHAINS", v)
		got := ResolverConfigFor(lang.Python).DropFuzzyJanusianChains
		if got {
			t.Errorf("env=%q on Python: force-off must override default-on, got DropFuzzyJanusianChains=true", v)
		}
	}
}
