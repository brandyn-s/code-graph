package pipeline

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// CG-1 (2026-05-06) — drop-on-no-match for Python cross-package-suffix.
//
// Background: 2026-05-06 baselines (`bench/accuracy/baselines/
// 2026-05-06-adversarial-rerun-finding.md`) show the cross-package-suffix
// resolver bucket has 0.07 precision on flask-adversarial and 0.23 on
// requests-adversarial — essentially noise on Python. The same bucket
// scores 0.85-0.95 on Go and Rust fixtures. Drop-on-no-match for Python
// only.
//
// These tests pin the language-aware behavior at the resolver level
// (FunctionRegistry methods) rather than through the full pipeline so
// the policy is verifiable without an indexer setup.

// TestSuffixMatch_DropsForPython — when language=Python, the suffix_match
// strategy returns empty (drop) instead of emitting a phantom edge.
func TestSuffixMatch_DropsForPython(t *testing.T) {
	r := NewFunctionRegistry()
	// Two Python modules with `process` in their name. Both end with
	// `.process` so a bare-name `process` lookup will produce two
	// suffix candidates — exactly the ambiguity that produces flask-style
	// 0.07-precision phantoms.
	r.Register("process", "flask.app.process", "Function")
	r.Register("process", "flask.helpers.process", "Function")

	got := r.ResolveCtx(CallContext{
		CalleeName: "process",
		ModuleQN:   "flask.unrelated",
		ImportMap:  nil,
		Language:   lang.Python,
	})
	if got.QualifiedName != "" {
		t.Fatalf("Python suffix_match must drop; got QN=%q strategy=%q",
			got.QualifiedName, got.Strategy)
	}
}

// TestSuffixMatch_EmitsForGo — same shape but Go language: the bucket
// scores 0.91+ on Go fixtures so we keep current behavior.
func TestSuffixMatch_EmitsForGo(t *testing.T) {
	r := NewFunctionRegistry()
	r.Register("Process", "github.com/x/pkg.Process", "Function")
	r.Register("Process", "github.com/x/helpers.Process", "Function")

	got := r.ResolveCtx(CallContext{
		CalleeName: "Process",
		ModuleQN:   "github.com/x/unrelated",
		ImportMap:  nil,
		Language:   lang.Go,
	})
	if got.QualifiedName == "" {
		t.Fatalf("Go suffix_match must emit; got empty result. Drop policy is Python-only.")
	}
}

// TestSuffixMatch_EmitsForUnknownLanguage — empty Language field
// (legacy Resolve() callers) preserves current behavior. The drop fires
// only on the explicit Python branch.
func TestSuffixMatch_EmitsForUnknownLanguage(t *testing.T) {
	r := NewFunctionRegistry()
	r.Register("process", "proj.app.process", "Function")
	r.Register("process", "proj.helpers.process", "Function")

	got := r.ResolveCtx(CallContext{
		CalleeName: "process",
		ModuleQN:   "proj.unrelated",
		ImportMap:  nil,
		// Language unset — legacy behavior path
	})
	if got.QualifiedName == "" {
		t.Fatalf("unknown-language suffix_match must emit (legacy compatibility); got empty")
	}
}

// TestImportMapSuffix_DropsForPython — when an import-map lookup
// produces a suffix-matched candidate (the dangerous import_map_suffix
// strategy), Python drops it. Multiple candidates registered so that
// unique_name doesn't fire as a fallback (its precision is currently
// considered acceptable).
func TestImportMapSuffix_DropsForPython(t *testing.T) {
	r := NewFunctionRegistry()
	// `from flask import helpers` then `helpers.process()`:
	// `helpers` is in import map (resolves to `flask.helpers`), but
	// `flask.helpers.process` doesn't exist exactly. The resolver falls
	// through to import_map_suffix and finds nested `flask.helpers.X.process`
	// candidates. With multiple X-candidates, unique_name also doesn't
	// match (need exactly 1 project-wide). On Python, this is the
	// catastrophic-precision pattern — drop should kill the edge.
	r.Register("process", "flask.helpers.utils.process", "Function")
	r.Register("process", "flask.helpers.tools.process", "Function")
	r.Register("process", "flask.unrelated.process", "Function")

	importMap := map[string]string{
		"helpers": "flask.helpers",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "helpers.process",
		ModuleQN:   "flask.app",
		ImportMap:  importMap,
		Language:   lang.Python,
	})
	if got.QualifiedName != "" {
		t.Fatalf("Python import_map_suffix must drop; got QN=%q strategy=%q",
			got.QualifiedName, got.Strategy)
	}
}

// TestImportMapSuffix_EmitsForGo — Go retains current behavior on the
// dangerous suffix path (precision 0.85-0.95 on Go fixtures, workable).
func TestImportMapSuffix_EmitsForGo(t *testing.T) {
	r := NewFunctionRegistry()
	r.Register("Process", "github.com/x/helpers/utils.Process", "Function")
	r.Register("Process", "github.com/x/helpers/tools.Process", "Function")

	importMap := map[string]string{
		"helpers": "github.com/x/helpers",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "helpers.Process",
		ModuleQN:   "github.com/x/app",
		ImportMap:  importMap,
		Language:   lang.Go,
	})
	if got.QualifiedName == "" {
		t.Fatalf("Go import_map_suffix must emit; got empty result")
	}
}

// TestExactImportMap_EmitsForPython — the precise import_map strategy
// (NOT the dangerous suffix variant) is preserved for Python. Python
// dropping must NOT crater recall on legitimate `from foo import bar`
// + `bar()` patterns where `foo.bar` exists exactly.
func TestExactImportMap_EmitsForPython(t *testing.T) {
	r := NewFunctionRegistry()
	r.Register("foo", "flask.helpers.foo", "Function")

	importMap := map[string]string{
		"foo": "flask.helpers.foo",
	}
	got := r.ResolveCtx(CallContext{
		CalleeName: "foo",
		ModuleQN:   "flask.app",
		ImportMap:  importMap,
		Language:   lang.Python,
	})
	if got.QualifiedName != "flask.helpers.foo" {
		t.Fatalf("Python import_map (exact) must emit; got QN=%q strategy=%q",
			got.QualifiedName, got.Strategy)
	}
	if got.Strategy != "import_map" {
		t.Errorf("expected strategy=import_map, got %q", got.Strategy)
	}
}

// TestSameModule_EmitsForPython — same-module shadowing (precise) is
// preserved for Python. Drop policy is scoped to cross-package-suffix
// only; same-module is a different bucket.
func TestSameModule_EmitsForPython(t *testing.T) {
	r := NewFunctionRegistry()
	r.Register("helper", "flask.app.helper", "Function")

	got := r.ResolveCtx(CallContext{
		CalleeName: "helper",
		ModuleQN:   "flask.app",
		ImportMap:  nil,
		Language:   lang.Python,
	})
	if got.QualifiedName != "flask.app.helper" {
		t.Fatalf("Python same_module must emit; got QN=%q strategy=%q",
			got.QualifiedName, got.Strategy)
	}
}
