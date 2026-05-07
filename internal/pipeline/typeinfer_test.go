package pipeline

import "testing"

// TestResolveAsClass_AcceptsAllClassLikeLabels pins the label set
// accepted by resolveAsClass. Regression for A2 (2026-05-07): Rust
// extractor labels traits as "Trait" and structs as "Struct" — the
// previous label allowlist (Class / Type / Interface / Enum) silently
// dropped both, so implementsRust never emitted IMPLEMENTS edges for
// any Rust impl-block.
func TestResolveAsClass_AcceptsAllClassLikeLabels(t *testing.T) {
	registry := NewFunctionRegistry()

	cases := []struct {
		label  string
		qn     string
		simple string
	}{
		{"Class", "pkg.MyClass", "MyClass"},
		{"Type", "pkg.MyType", "MyType"},
		{"Interface", "pkg.MyIface", "MyIface"},
		{"Enum", "pkg.MyEnum", "MyEnum"},
		{"Struct", "pkg.MyStruct", "MyStruct"},
		{"Trait", "pkg.MyTrait", "MyTrait"},
	}
	for _, c := range cases {
		registry.Register(c.simple, c.qn, c.label)
	}

	for _, c := range cases {
		got := resolveAsClass(c.simple, registry, "pkg.module", nil)
		if got != c.qn {
			t.Errorf("resolveAsClass(%q [label=%s]) = %q, want %q",
				c.simple, c.label, got, c.qn)
		}
	}
}

// TestResolveAsClass_RejectsNonClassLikeLabels pins the negative side:
// Function and Method labels must NOT resolve via resolveAsClass.
// Otherwise `resolveAsClass(callerName)` would incorrectly bind methods
// to themselves as if they were types.
func TestResolveAsClass_RejectsNonClassLikeLabels(t *testing.T) {
	registry := NewFunctionRegistry()
	registry.Register("doStuff", "pkg.doStuff", "Function")
	registry.Register("Run", "pkg.MyClass.Run", "Method")
	registry.Register("api", "pkg.api", "Module")

	for _, name := range []string{"doStuff", "Run", "api"} {
		got := resolveAsClass(name, registry, "pkg.module", nil)
		if got != "" {
			t.Errorf("resolveAsClass(%q) = %q, want empty (not a class-like label)", name, got)
		}
	}
}

// TestResolveAsClass_RustTraitStructPair simulates the Rust impl-block
// resolution path that was broken before A2: a Trait registered under
// label "Trait", a Struct registered under label "Struct", and the
// resolver must return both QNs so implementsRust emits the IMPLEMENTS
// edge between them.
func TestResolveAsClass_RustTraitStructPair(t *testing.T) {
	registry := NewFunctionRegistry()
	// Mimic the PSM 2026-05-07 case: trait CradlepointClientSync,
	// struct CradlepointApiClientSync.
	registry.Register("CradlepointClientSync", "myproj.api.CradlepointClientSync", "Trait")
	registry.Register("CradlepointApiClientSync", "myproj.impl.CradlepointApiClientSync", "Struct")

	traitQN := resolveAsClass("CradlepointClientSync", registry, "myproj.impl", nil)
	if traitQN != "myproj.api.CradlepointClientSync" {
		t.Errorf("trait QN = %q, want myproj.api.CradlepointClientSync", traitQN)
	}
	structQN := resolveAsClass("CradlepointApiClientSync", registry, "myproj.impl", nil)
	if structQN != "myproj.impl.CradlepointApiClientSync" {
		t.Errorf("struct QN = %q, want myproj.impl.CradlepointApiClientSync", structQN)
	}
}
