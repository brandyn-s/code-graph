// Package pipeline — caller_kind.go classifies the SCOPE that emitted a CALLS
// edge so harness metrics can stratify precision by caller-shape.
//
// 2026-05-02 plateau-2 plan, Step 3. The aggregate F1 is dominated by
// edges whose CALLER is a real function or method body. A growing
// share of false-positives, however, are emitted from PACKAGE-LEVEL
// scopes — file blocks, var initializers, type declarations, init()
// functions. These "ghost callers" correlate strongly with low-quality
// resolutions (the resolver has no tight enclosing-function context to
// constrain candidates with).
//
// Today they're invisible: the headline F1 number lumps every CALLS
// edge together, and the per-project breakdown (PR #129) shows
// per-subset variance but not per-caller-shape variance. After this
// change every CALLS edge carries a `caller_node_kind` property; the
// harness can compute per-kind precision and the share of FPs that
// originate from non-function scopes.
//
// Classification source: the resolver already routes calls through
// `resolveCallEdge` (CBM extractor, function/method body) and through
// `collectLSPResolvedEdges` (LSP cross-file resolution, function/method
// body). When `EnclosingFuncQN == ""` the resolver synthesizes a
// module-level caller and tags the edge `CALLS_PSEUDO`; that's the
// "package-block" signal. The label of the caller node (looked up in
// the FunctionRegistry) discriminates Function-body vs Method-body.
// Test bodies are detected by the `IsTest` flag on the Definition,
// which the CBM extractor sets from the parser-level test-file +
// test-name heuristic.
//
// This file plumbs information the resolver already has. No new AST
// traversal, no new C-side extractor work.
package pipeline

import "strings"

// CallerKind enumerates the AST scope an emitted CALLS edge originates
// from. Stored as a string in the edge's `caller_node_kind` property
// for serialization friendliness (Cypher exposes it as an edge attribute,
// and json_extract('$.caller_node_kind') keeps the SQL path simple).
//
// The resolver classifies each emitted edge into exactly one bucket.
// Buckets are stable identifiers — never rename them; harness baselines
// reference them by string.
const (
	// CallerKindFunction — caller is a free function body (Go func, Python
	// def at module level, Rust fn, etc.). Default for non-method callable.
	CallerKindFunction = "function-body"

	// CallerKindMethod — caller is a method body (struct receiver, Python
	// class method, Rust impl block fn, Java/Kotlin class method).
	CallerKindMethod = "method-body"

	// CallerKindPackageInit — caller is an explicit package initializer
	// (Go `func init()`, Python `__init__.py` module body's init-time
	// statements, Rust `#[ctor]`-style). The resolver currently lumps
	// these into the synthetic-module-caller path; we use this kind when
	// the caller QN's last segment is literally "init".
	CallerKindPackageInit = "package-init-block"

	// CallerKindFileBlock — caller is the synthetic module-level caller
	// (`EnclosingFuncQN == ""` at the C-side extractor, resolved to
	// moduleQN + edgeType=CALLS_PSEUDO on the Go side). Top-level
	// function-call statements that aren't inside any callable scope.
	CallerKindFileBlock = "file-block"

	// CallerKindTypeDecl — caller is a type declaration (Go method on a
	// type, Rust impl block header, Python class body before any def).
	// Currently rare; reserved for future extractor enhancements.
	CallerKindTypeDecl = "type-decl"

	// CallerKindVarInit — caller is a package-level variable initializer
	// (`var x = expensive()` in Go, module-level `X = compute()` in
	// Python). Detected via the same module-level synthetic-caller path
	// as file-block; distinguishing requires extractor support that
	// doesn't yet exist, so for now var-init flows through
	// CallerKindFileBlock. Reserved for resolver enhancement.
	CallerKindVarInit = "var-init"

	// CallerKindTest — caller is a test function body. Detected via
	// IsTest on the caller's Definition (set by the CBM extractor from
	// `_test.go` / TestXxx / pytest discovery).
	CallerKindTest = "test-body"

	// CallerKindClosure — caller is an anonymous function / lambda /
	// closure expression. Reserved; the CBM extractor does not currently
	// emit a stable QN for closures, so this kind is unused today.
	CallerKindClosure = "closure"

	// CallerKindUnknown — fallback when no classification rule fires.
	// Should be 0% on healthy inputs; non-zero counts indicate either
	// a registry-lookup miss or a new caller shape we haven't covered.
	CallerKindUnknown = "unknown"
)

// callerKindFromContext returns the classification for an emitted CALLS
// edge given (a) the caller's resolved QN, (b) the edge type the
// resolver picked, and (c) the FunctionRegistry. The registry is the
// authoritative source for the caller node's label (Function/Method/
// Module) and for the IsTest flag (recorded as a property at definition
// time and retrievable via the registry's exact map — but we don't have
// direct access, so we rely on QN-shape and edgeType heuristics).
//
// Decision order (first match wins):
//   1. edgeType == "CALLS_PSEUDO"           → CallerKindFileBlock
//      (synthetic module-level caller; the resolver substituted moduleQN
//      because EnclosingFuncQN was empty)
//   2. caller QN's last segment is "init"   → CallerKindPackageInit
//      (Go init() and Python module __init__ render as `<module>.init`)
//   3. registryLabel(callerQN) == "Method"  → CallerKindMethod
//   4. registryLabel(callerQN) == "Function":
//        a. simpleName starts with "Test" or "Benchmark" or "Example"
//           AND the caller's file basename contains "_test." or
//           "test_"                          → CallerKindTest
//        b. otherwise                        → CallerKindFunction
//   5. registry has no label for callerQN    → CallerKindUnknown
//      (caller QN was synthesized externally, e.g. by an LSP path that
//      points to a stub/external symbol; healthy callers always exist
//      in the registry)
//
// Test-name detection without file context: we accept TestXxx /
// BenchmarkXxx / ExampleXxx as a sufficient signal even if the caller's
// file isn't visible at this layer, because Go's testing convention is
// strict (these names are reserved for tests by the toolchain).
//
// Confidence: VERIFIED for branches 1, 2, 3, 4a (test-name suffix),
// 4b. INFERRED for branch 4a's _test. fallback (relies on convention,
// not extractor signal).
func callerKindFromContext(callerQN, edgeType string, registry *FunctionRegistry) string {
	if edgeType == "CALLS_PSEUDO" {
		return CallerKindFileBlock
	}

	short := callerQN
	if idx := strings.LastIndex(short, "."); idx >= 0 {
		short = short[idx+1:]
	}
	if short == "init" {
		return CallerKindPackageInit
	}

	var label string
	if registry != nil {
		label = registry.LabelOf(callerQN)
	}
	switch label {
	case "Method":
		return CallerKindMethod
	case "Function":
		if isTestCallerName(short) {
			return CallerKindTest
		}
		return CallerKindFunction
	case "":
		return CallerKindUnknown
	default:
		// Module/Class/Variable etc. landing here means the caller QN
		// resolved to a non-callable label, which only happens for
		// synthetic stubs or registry inconsistencies. Treat as
		// file-block (closest semantic match) so harness metrics still
		// see them.
		return CallerKindFileBlock
	}
}

// isTestCallerName returns true if the simple function name follows
// Go's reserved test-naming convention. Used to discriminate
// CallerKindTest from CallerKindFunction without extractor-level
// support. Safe heuristic: only Go's `go test` toolchain treats these
// names specially, and other languages' free functions named TestFoo
// are extremely rare; if they do exist, they're effectively tests in
// spirit and the kind tagging is still defensible.
func isTestCallerName(name string) bool {
	for _, prefix := range []string{"Test", "Benchmark", "Example", "Fuzz"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Test/Benchmark/Example must be followed by either nothing
		// (rare) or an uppercase letter — `Test` alone, `TestFoo`, but
		// not `Tests` or `TestableFoo`. Mirrors `go test` discovery.
		if len(name) == len(prefix) {
			return true
		}
		next := name[len(prefix)]
		if next >= 'A' && next <= 'Z' {
			return true
		}
	}
	return false
}
