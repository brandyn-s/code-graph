package pipeline

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// TestImportBindingFilter_SkipsRustSuperPaths verifies that `super::`-prefixed
// ImportBinding targets are treated as intra-crate (NOT external) and do
// NOT trigger the "drop internal candidates" verdict.
//
// Regression guard: post code-graph PR #350 grouped-imports fix, the C
// extractor correctly decomposes `use super::{audio::{arm, disarm}};` into
// per-leaf CBMImports with module_path="super::audio::arm" etc. Pre-fix,
// the bindings didn't exist and resolveViaNameLookup's suffix-match found
// the in-graph candidate. Post-fix without this filter-bypass, the
// applyImportBindingFilter treated `super::` as external (since the
// resolver couldn't resolve `super::` to an absolute QN), DROPPED internal
// candidates, and produced 35 new FNs on apid (-7.6pp F1).
//
// The bypass: when target starts with `super::` or `self::`, the filter
// returns the original candidate set unchanged. Suffix-match downstream
// resolves the call correctly.
func TestImportBindingFilter_SkipsRustSuperPaths(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("arm", "apid.src.v1.command.audio.arm", "Function")

	ctx := CallContext{
		CalleeName: "arm",
		CallerQN:   "apid.src.v1.command.mcs_proxy.mcs_command",
		ModuleQN:   "apid.src.v1.command.mcs_proxy",
		ImportBindings: map[string]string{
			"arm": "super::audio::arm",
		},
		Language: lang.Rust,
	}
	candidates := []string{"apid.src.v1.command.audio.arm"}

	filtered, applied, dropAll := reg.applyImportBindingFilter(ctx, candidates)
	if dropAll {
		t.Fatalf("filter wrongly dropped all candidates for super::-rooted binding; "+
			"`super::audio::arm` is intra-crate and must NOT be treated as external. "+
			"applied=%q filtered=%v", applied, filtered)
	}
	if applied != "" {
		t.Errorf("expected applied=\"\" (no filtering for super:: paths), got %q", applied)
	}
	if len(filtered) != len(candidates) {
		t.Errorf("expected unchanged candidate set, got len=%d (want %d)", len(filtered), len(candidates))
	}
}

// TestImportBindingFilter_SkipsRustSelfPaths verifies the same for `self::`
// relative paths (`use self::sibling::fn;` form).
func TestImportBindingFilter_SkipsRustSelfPaths(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("helper", "myapp.src.module.helper", "Function")

	ctx := CallContext{
		CalleeName: "helper",
		CallerQN:   "myapp.src.module.entry",
		ModuleQN:   "myapp.src.module",
		ImportBindings: map[string]string{
			"helper": "self::helper",
		},
		Language: lang.Rust,
	}
	candidates := []string{"myapp.src.module.helper"}

	filtered, applied, dropAll := reg.applyImportBindingFilter(ctx, candidates)
	if dropAll {
		t.Fatalf("filter wrongly dropped self::-rooted binding; applied=%q filtered=%v",
			applied, filtered)
	}
	if applied != "" {
		t.Errorf("expected applied=\"\" for self:: paths, got %q", applied)
	}
	if len(filtered) != len(candidates) {
		t.Errorf("expected unchanged candidate set, got len=%d (want %d)", len(filtered), len(candidates))
	}
}

// TestImportBindingFilter_StillCatchesExternalCratePaths verifies the filter
// still correctly identifies and drops external-crate bindings (the
// original Phase 3b discrimination). The super::/self:: bypass is
// specific to those two prefixes — `external_crate::foo` should still
// trigger the external-drop verdict.
func TestImportBindingFilter_StillCatchesExternalCratePaths(t *testing.T) {
	reg := NewFunctionRegistry()
	// Internal `ready` exists, but the call site imports an external one.
	reg.Register("ready", "myapp.src.internal.ready", "Function")

	ctx := CallContext{
		CalleeName: "ready",
		CallerQN:   "myapp.src.entry.caller",
		ModuleQN:   "myapp.src.entry",
		ImportBindings: map[string]string{
			"ready": "futures_util::future::ready",
		},
		Language: lang.Rust,
	}
	candidates := []string{"myapp.src.internal.ready"}

	_, applied, dropAll := reg.applyImportBindingFilter(ctx, candidates)
	if !dropAll {
		t.Errorf("expected dropAll=true for external-crate binding "+
			"`futures_util::future::ready`; got dropAll=false applied=%q", applied)
	}
	if applied != "import-binding-external" {
		t.Errorf("expected applied=\"import-binding-external\", got %q", applied)
	}
}

// TestImportBindingFilter_StillResolvesInternalCratePaths verifies the
// filter still positively matches `crate::`-rooted internal bindings —
// the crate.-strip path (line 878) shouldn't have regressed.
func TestImportBindingFilter_StillResolvesInternalCratePaths(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("error_response", "myapp.src.util.error_response", "Function")

	ctx := CallContext{
		CalleeName: "error_response",
		CallerQN:   "myapp.src.handler.do_thing",
		ModuleQN:   "myapp.src.handler",
		ImportBindings: map[string]string{
			"error_response": "crate::util::error_response",
		},
		Language: lang.Rust,
	}
	candidates := []string{"myapp.src.util.error_response"}

	filtered, applied, dropAll := reg.applyImportBindingFilter(ctx, candidates)
	if dropAll {
		t.Errorf("crate::-rooted binding shouldn't be classified external; "+
			"applied=%q filtered=%v", applied, filtered)
	}
	if applied != "import-binding-match" {
		t.Errorf("expected applied=\"import-binding-match\", got %q", applied)
	}
	if len(filtered) != 1 || filtered[0] != "myapp.src.util.error_response" {
		t.Errorf("expected single match `myapp.src.util.error_response`, got %v", filtered)
	}
}
