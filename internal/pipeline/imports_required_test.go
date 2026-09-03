package pipeline

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// Phase F (Plan 8-Phase Arc, 2026-05-09) —
// RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE env var gate.
//
// Background: the existing CG-1 Python drop (shouldDropCrossPackageSuffix)
// kills the entire cross-package-suffix bucket for Python — improving
// precision but losing recall on legitimate same-bucket emissions where
// the call site DOES have an explicit IMPORTS edge to the target.
// Phase F adds an orthogonal env-var gate: when set, drop candidates
// from unique_name and suffix_match that aren't import-reachable, even
// for non-Python languages. The intent is precision tightening at recall
// cost on languages where the bucket is currently emit-at-half-confidence
// (Go, Rust). Default unset = current behavior.

// TestRequireImports_UniqueNameDropped — when env var is set and the unique
// project-wide candidate is NOT import-reachable, the unique_name strategy
// drops instead of emitting at halved confidence.
func TestRequireImports_UniqueNameDropped(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "1")
	r := NewFunctionRegistry()
	// Single project-wide `Process` — unique_name strategy fires.
	r.Register("Process", "github.com/x/utils.Process", "Function")

	// Caller's import map does NOT mention utils — Process is not import-reachable.
	importMap := map[string]string{
		"helpers": "github.com/x/helpers",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "Process",
		ModuleQN:   "github.com/x/app",
		ImportMap:  importMap,
		Language:   lang.Go,
	})
	if got.QualifiedName != "" {
		t.Fatalf("with gate enabled, unique_name must drop when not import-reachable; got QN=%q strategy=%q",
			got.QualifiedName, got.Strategy)
	}
}

// TestRequireImports_UniqueNameEmits_WhenImportReachable — gate enabled
// but the unique candidate IS import-reachable: emit at full confidence.
func TestRequireImports_UniqueNameEmits_WhenImportReachable(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "1")
	r := NewFunctionRegistry()
	r.Register("Process", "github.com/x/utils.Process", "Function")

	// Import map includes utils → Process IS import-reachable.
	importMap := map[string]string{
		"utils": "github.com/x/utils",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "Process",
		ModuleQN:   "github.com/x/app",
		ImportMap:  importMap,
		Language:   lang.Go,
	})
	if got.QualifiedName != "github.com/x/utils.Process" {
		t.Fatalf("import-reachable unique_name must emit even with gate set; got %q", got.QualifiedName)
	}
}

// TestRequireImports_GateUnsetPreservesLegacyBehavior — when env var is
// NOT set, the resolver must behave exactly as before (emit at halved
// confidence for non-import-reachable candidates).
func TestRequireImports_GateUnsetPreservesLegacyBehavior(t *testing.T) {
	// Explicitly unset the env var to make the test self-contained.
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "")
	r := NewFunctionRegistry()
	r.Register("Process", "github.com/x/utils.Process", "Function")

	importMap := map[string]string{
		"helpers": "github.com/x/helpers",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "Process",
		ModuleQN:   "github.com/x/app",
		ImportMap:  importMap,
		Language:   lang.Go,
	})
	if got.QualifiedName != "github.com/x/utils.Process" {
		t.Fatalf("with gate UNSET, unique_name must still emit (legacy); got %q", got.QualifiedName)
	}
	// Confidence halved because not import-reachable
	if got.Confidence > 0.5 {
		t.Errorf("expected halved confidence for non-import-reachable; got %.2f", got.Confidence)
	}
}

// TestRequireImports_SuffixMatchDropped — when gate set and ALL
// suffix candidates are non-import-reachable, suffix_match drops them.
func TestRequireImports_SuffixMatchDropped(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "1")
	r := NewFunctionRegistry()
	// Two `Process` candidates so we don't hit unique_name.
	r.Register("Process", "github.com/x/utils.Process", "Function")
	r.Register("Process", "github.com/x/helpers.Process", "Function")

	// Import map mentions NEITHER — both candidates non-import-reachable.
	importMap := map[string]string{
		"unrelated": "github.com/x/unrelated",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "Process",
		ModuleQN:   "github.com/x/app",
		ImportMap:  importMap,
		Language:   lang.Go,
	})
	if got.QualifiedName != "" {
		t.Fatalf("with gate enabled and zero import-reachable candidates, suffix_match must drop; got %q", got.QualifiedName)
	}
}

// TestRequireImports_PythonSuffixDropStillFires — Phase F gate is
// orthogonal to the existing Python language-drop on the suffix bucket.
// With multiple candidates (suffix_match path) on Python, the existing
// language-drop fires regardless of the gate's import-reachability check.
func TestRequireImports_PythonSuffixDropStillFires(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "1")
	r := NewFunctionRegistry()
	// Multiple candidates → exits unique_name path, enters suffix bucket
	// where Python's language-drop applies independently.
	r.Register("process", "flask.utils.process", "Function")
	r.Register("process", "flask.helpers.process", "Function")

	got := r.ResolveCtx(CallContext{
		CalleeName: "process",
		ModuleQN:   "flask.app",
		ImportMap:  nil,
		Language:   lang.Python,
	})
	if got.QualifiedName != "" {
		t.Fatalf("Python suffix bucket must drop (existing CG-1 + Phase F gate); got %q", got.QualifiedName)
	}
}

// TestRequireImports_UniqueNameNoImportMap — when ImportMap is nil
// AND gate is set, the gate does NOT drop unique_name results. Without
// an import map, we can't tell if a candidate is import-reachable, so
// preserve the legacy emit-at-full-confidence behavior. The gate is
// scoped to "we have an import map and the candidate isn't in it".
func TestRequireImports_UniqueNameNoImportMap(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "1")
	r := NewFunctionRegistry()
	r.Register("Process", "github.com/x/utils.Process", "Function")

	got := r.ResolveCtx(CallContext{
		CalleeName: "Process",
		ModuleQN:   "github.com/x/app",
		ImportMap:  nil, // no import map — can't apply the gate
		Language:   lang.Go,
	})
	// Single project-wide candidate, no import map to check — emit.
	if got.QualifiedName != "github.com/x/utils.Process" {
		t.Fatalf("no-import-map unique_name must emit (gate scope is import-map-present); got %q", got.QualifiedName)
	}
}

// TestRequireImports_PickBestCandidateDrops — when gate set and the
// import filter eliminates every candidate, pickBestCandidate drops
// instead of falling back to bestByImportDistance.
func TestRequireImports_PickBestCandidateDrops(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "1")
	r := NewFunctionRegistry()
	// Multiple candidates with the same suffix-name where neither is
	// import-reachable. resolveSuffixMatch drops them at the per-candidate
	// gate; pickBestCandidate is then called with all candidates, but
	// filterImportReachable returns empty, and our new gate drops.
	r.Register("DoThing", "github.com/x/utils.X.DoThing", "Method")
	r.Register("DoThing", "github.com/x/helpers.Y.DoThing", "Method")

	importMap := map[string]string{
		"unrelated": "github.com/x/unrelated",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "DoThing",
		ModuleQN:   "github.com/x/app",
		ImportMap:  importMap,
		Language:   lang.Go,
	})
	if got.QualifiedName != "" {
		t.Fatalf("with gate set, pickBestCandidate must drop when no import-reachable candidate; got %q", got.QualifiedName)
	}
}

// TestRequireImports_RustDefaultOn — env var UNSET, but the per-language
// default for Rust is now ON (B2 audit, 2026-05-09). Cross-language /
// cross-crate-no-import phantoms must drop without explicit env var.
func TestRequireImports_RustDefaultOn(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "")
	r := NewFunctionRegistry()
	// Two `sleep` candidates: one in a Rust crate not imported, one in a
	// JS file that just happens to have the same suffix.
	r.Register("sleep", "libmetrics.sink.sleep", "Function")
	r.Register("sleep", "mithrandir.e2e.trackTestScript.sleep", "Function")

	// Caller imports tokio::time::sleep — neither candidate matches.
	importMap := map[string]string{
		"sleep": "tokio.time.sleep",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "sleep",
		ModuleQN:   "reloadd.handlers.handle_reboot_request",
		ImportMap:  importMap,
		Language:   lang.Rust,
	})
	if got.QualifiedName != "" {
		t.Fatalf("Rust default-on gate must drop non-import-reachable candidates; got QN=%q", got.QualifiedName)
	}
}

// TestRequireImports_RustDefaultOn_EmitsWhenImportReachable — Rust default-
// on must NOT regress recall on real intra-workspace calls where the caller
// has an explicit `use` statement reaching the candidate.
func TestRequireImports_RustDefaultOn_EmitsWhenImportReachable(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "")
	r := NewFunctionRegistry()
	r.Register("wrap_half_range", "libtransform.angles.wrap_half_range", "Function")

	// Caller has `use libtransform::angles::wrap_half_range`.
	importMap := map[string]string{
		"wrap_half_range": "libtransform.angles.wrap_half_range",
		"angles":          "libtransform.angles",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "wrap_half_range",
		ModuleQN:   "libais.types.ais",
		ImportMap:  importMap,
		Language:   lang.Rust,
	})
	if got.QualifiedName != "libtransform.angles.wrap_half_range" {
		t.Fatalf("import-reachable Rust call must emit even with default-on gate; got %q", got.QualifiedName)
	}
}

// TestRequireImports_GoDefaultOff — for non-Rust languages with no env
// var, the gate stays OFF (preserve current emit-at-half behavior).
func TestRequireImports_GoDefaultOff(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "")
	r := NewFunctionRegistry()
	r.Register("Process", "github.com/x/utils.Process", "Function")

	// Caller's import map does NOT mention utils — non-import-reachable.
	importMap := map[string]string{
		"helpers": "github.com/x/helpers",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "Process",
		ModuleQN:   "github.com/x/app",
		ImportMap:  importMap,
		Language:   lang.Go,
	})
	// Go default-off: still emit, at halved confidence.
	if got.QualifiedName != "github.com/x/utils.Process" {
		t.Fatalf("Go default-off must preserve emit-at-half behavior; got %q", got.QualifiedName)
	}
	if got.Confidence > 0.5 {
		t.Errorf("expected halved confidence on Go default-off path; got %.2f", got.Confidence)
	}
}

// TestRequireImports_RustDefaultOn_NoImportMap — Rust default-on must NOT
// drop when caller has no import map (gate scope is import-map-present).
func TestRequireImports_RustDefaultOn_NoImportMap(t *testing.T) {
	t.Setenv("RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE", "")
	r := NewFunctionRegistry()
	r.Register("Process", "libutils.Process", "Function")

	got := r.ResolveCtx(CallContext{
		CalleeName: "Process",
		ModuleQN:   "libapp.run",
		ImportMap:  nil, // no import map — can't apply the gate
		Language:   lang.Rust,
	})
	if got.QualifiedName != "libutils.Process" {
		t.Fatalf("no-import-map Rust call must emit (gate scope is import-map-present); got %q", got.QualifiedName)
	}
}
