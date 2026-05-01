package pipeline

import "testing"

// TestCallerKindFromContext_PseudoEdgeFileBlock — when the resolver
// substituted a moduleQN as the caller and tagged the edge CALLS_PSEUDO,
// the kind is file-block regardless of whether the moduleQN happens to
// have a registry label. This branch fires before any registry lookup.
func TestCallerKindFromContext_PseudoEdgeFileBlock(t *testing.T) {
	reg := NewFunctionRegistry()
	// A module-level node would have label "Module" — but CALLS_PSEUDO
	// short-circuits before that lookup runs.
	reg.Register("main", "github.com/example/foo.main", "Module")

	got := callerKindFromContext("github.com/example/foo", "CALLS_PSEUDO", reg)
	if got != CallerKindFileBlock {
		t.Errorf("CALLS_PSEUDO must be file-block, got %q", got)
	}
}

// TestCallerKindFromContext_InitFunction — Go's `func init()` and
// Python's module __init__ render with a final segment of "init",
// which we map to package-init-block independent of registry label.
func TestCallerKindFromContext_InitFunction(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("init", "github.com/example/foo.init", "Function")

	got := callerKindFromContext("github.com/example/foo.init", "CALLS", reg)
	if got != CallerKindPackageInit {
		t.Errorf("init() must be package-init-block, got %q", got)
	}
}

// TestCallerKindFromContext_MethodBody — caller registered as Method
// produces method-body. Mirror real CBM emission where struct receivers
// route to label "Method".
func TestCallerKindFromContext_MethodBody(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("DoThing", "github.com/example/foo.MyStruct.DoThing", "Method")

	got := callerKindFromContext("github.com/example/foo.MyStruct.DoThing", "CALLS", reg)
	if got != CallerKindMethod {
		t.Errorf("Method label must be method-body, got %q", got)
	}
}

// TestCallerKindFromContext_FreeFunction — caller registered as Function
// with a non-test name produces function-body.
func TestCallerKindFromContext_FreeFunction(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("processOrder", "github.com/example/foo.processOrder", "Function")

	got := callerKindFromContext("github.com/example/foo.processOrder", "CALLS", reg)
	if got != CallerKindFunction {
		t.Errorf("free function must be function-body, got %q", got)
	}
}

// TestCallerKindFromContext_TestFunction — Go test convention: TestXxx /
// BenchmarkXxx / ExampleXxx / FuzzXxx with capital after prefix routes
// to test-body even when registered as Function.
func TestCallerKindFromContext_TestFunction(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("TestFoo", "github.com/example/foo.TestFoo", "Function")

	got := callerKindFromContext("github.com/example/foo.TestFoo", "CALLS", reg)
	if got != CallerKindTest {
		t.Errorf("TestFoo must be test-body, got %q", got)
	}
}

// TestCallerKindFromContext_BenchmarkFunction — Benchmark is also a
// reserved Go test-toolchain prefix.
func TestCallerKindFromContext_BenchmarkFunction(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("BenchmarkParse", "github.com/example/foo.BenchmarkParse", "Function")

	got := callerKindFromContext("github.com/example/foo.BenchmarkParse", "CALLS", reg)
	if got != CallerKindTest {
		t.Errorf("BenchmarkParse must be test-body, got %q", got)
	}
}

// TestCallerKindFromContext_TestablePrefixDoesNotMatch — `Testable...`
// starts with `Test` but the next char is lowercase `a`, so it is a
// regular function, not a test. Catches the over-eager prefix-only check.
func TestCallerKindFromContext_TestablePrefixDoesNotMatch(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Testable", "github.com/example/foo.Testable", "Function")

	got := callerKindFromContext("github.com/example/foo.Testable", "CALLS", reg)
	if got != CallerKindFunction {
		t.Errorf("Testable must be function-body, got %q", got)
	}
}

// TestCallerKindFromContext_UnknownCaller — caller QN not in the
// registry routes to unknown. This fires for synthesized stubs / external
// LSP targets pointed to as callers (rare); harness flags non-zero
// counts as data-quality regression.
func TestCallerKindFromContext_UnknownCaller(t *testing.T) {
	reg := NewFunctionRegistry()

	got := callerKindFromContext("external.symbol.notRegistered", "CALLS", reg)
	if got != CallerKindUnknown {
		t.Errorf("unregistered caller must be unknown, got %q", got)
	}
}

// TestCallerKindFromContext_ModuleLabelFallsBackToFileBlock — if a
// caller QN somehow resolves to label "Module" without the CALLS_PSEUDO
// path firing (registry inconsistency / synthetic stub), treat as
// file-block — closest semantic match. Ensures we never silently swallow
// an unexpected label.
func TestCallerKindFromContext_ModuleLabelFallsBackToFileBlock(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("foo", "github.com/example/foo", "Module")

	got := callerKindFromContext("github.com/example/foo", "CALLS", reg)
	if got != CallerKindFileBlock {
		t.Errorf("Module-label caller must fall back to file-block, got %q", got)
	}
}

// TestCallerKindFromContext_NilRegistry — tests sometimes call without
// a registry; should return unknown rather than panic. Defensive: every
// production call site provides p.registry, but the helper has to be
// safe.
func TestCallerKindFromContext_NilRegistry(t *testing.T) {
	got := callerKindFromContext("github.com/example/foo.bar", "CALLS", nil)
	if got != CallerKindUnknown {
		t.Errorf("nil registry must produce unknown, got %q", got)
	}
}

// TestIsTestCallerName_Matrix — covers Go's full test-discovery name
// table. Mirrors `go test`'s parsing rule: prefix followed by an
// uppercase letter or end-of-name.
func TestIsTestCallerName_Matrix(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"TestFoo", true},
		{"Test", true},
		{"TestX", true},
		{"BenchmarkParse", true},
		{"ExampleNew", true},
		{"FuzzScan", true},
		{"Testable", false},   // Test + lowercase a
		{"Tests", false},      // Test + lowercase s
		{"benchmarkFoo", false}, // lowercase b
		{"helper", false},
		{"", false},
	}
	for _, c := range cases {
		got := isTestCallerName(c.name)
		if got != c.want {
			t.Errorf("isTestCallerName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
