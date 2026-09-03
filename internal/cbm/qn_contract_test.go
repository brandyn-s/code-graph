// Family A — CBM ↔ resolver QN-format contract test.
//
// This is the deferred "incident #2" coverage from PR #182's
// README_FAMILY_A.md. The contract: the QualifiedName CBM emits at
// definition-extraction time must be exactly the QN the resolver
// expects to find at call-resolution time.
//
// The shape of incident #2 (2026-05-02): CBM emitted Go function
// definitions with QN format `pkg.func` while resolver indexed
// `pkg.go.func`. Same-module lookups (resolveViaSameModule in
// pipeline/resolver.go:237) construct the lookup as
// `ctx.ModuleQN + "." + callee_name` and require an exact match in
// the registry. Any QN-format drift between CBM and the resolver's
// expectation makes 100% of same-package CALLS edges silently miss.
//
// The contract this test enforces:
//
//	Definition.QualifiedName == fqn.Compute(project, relPath, "") + "." + Definition.Name
//
// for every Definition CBM emits. fqn.Compute is the canonical QN
// computation used at storage time and at resolver-lookup time, so
// CBM's emitted QNs must agree with it.
//
// Per the 2026-05-04 incident-backport experiment, this closes the
// last Family A gap (incident #2). Combined with PR #182 (scorer
// fixtures) and PR #184 / #185 (refusal gate + provenance), all 7
// documented instrument incidents are covered by mechanical gates.
package cbm

import (
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/fqn"
	"github.com/brandyn-s/code-graph/internal/lang"
)

// goFixtureCases enumerates the QN-format shapes that have historically
// drifted between CBM emission and resolver expectation. Each case
// extracts a small Go source file and asserts every emitted Definition
// satisfies the CBM-fqn contract.
//
// The cases cover: free functions in main, methods on a receiver type,
// multi-file packages, package-init functions, and the doubled-package
// shape that bit ACC-009 / oracle-go-ast (helpers/helpers.go).
type goFixtureCase struct {
	name    string
	relPath string
	source  string
	// minDefs guards against the silent-zero failure mode where the
	// extractor produces no definitions for a file (which would make
	// the per-def assertion vacuously pass).
	minDefs int
}

func goFixtureCases() []goFixtureCase {
	return []goFixtureCase{
		{
			name:    "main_package_free_functions",
			relPath: "cmd/server/main.go",
			source: `package main

func entry() {}
func leaf()  {}
func main()  {}
`,
			minDefs: 3,
		},
		{
			name:    "method_on_receiver",
			relPath: "internal/store/router.go",
			source: `package store

type Router struct{}

func (r *Router) Route() {}
func (r *Router) Lookup() {}

func free() {}
`,
			minDefs: 3, // 2 methods + 1 free fn
		},
		{
			name:    "doubled_package_shape",
			relPath: "helpers/helpers.go",
			source: `package helpers

func Greet(name string)      {}
func innerGreet(name string) {}
`,
			// The historical ACC-009 oracle bug emitted
			// "go-minimal.helpers.helpers.Greet" instead of
			// "go-minimal.helpers.Greet" for files matching the
			// dir-name == file-name pattern. The CBM-fqn contract
			// catches the same shape if it ever drifts on the
			// indexer side.
			minDefs: 2,
		},
		{
			name:    "package_init_function",
			relPath: "internal/config/loader.go",
			source: `package config

func init() {
	_ = "side-effecting init"
}

func Load() string { return "" }
`,
			minDefs: 1, // Load (init may or may not be emitted; we don't strictly require it)
		},
		{
			name:    "test_file_keeps_test_suffix",
			relPath: "internal/store/router_test.go",
			source: `package store

import "testing"

func TestRouter(t *testing.T) {}

func helperForTest() {}
`,
			minDefs: 2,
		},
	}
}

// TestCBMResolverQNContract_Go is the contract test. For each Go fixture,
// every emitted Definition's QualifiedName must equal
// `fqn.Compute(project, relPath, "") + "." + Name` — modulo a
// receiver-qualified intermediate for methods (`<module>.<Receiver>.<name>`).
//
// This is the test that would have caught incident #2 at PR time:
// any drift in CBM's QN format relative to fqn.Compute's would fail
// here on the relevant fixture shape.
func TestCBMResolverQNContract_Go(t *testing.T) {
	const project = "test-project"

	for _, tc := range goFixtureCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fr, err := ExtractFile([]byte(tc.source), lang.Go, project, tc.relPath)
			if err != nil {
				t.Fatalf("ExtractFile failed: %v", err)
			}
			if got := len(fr.Definitions); got < tc.minDefs {
				t.Fatalf("expected >=%d Definitions, got %d (silent-zero failure mode)",
					tc.minDefs, got)
			}

			expectedPrefix := fqn.Compute(project, tc.relPath, "")
			if expectedPrefix == "" {
				t.Fatalf("fqn.Compute returned empty prefix for project=%q relPath=%q",
					project, tc.relPath)
			}

			for _, def := range fr.Definitions {
				if def.QualifiedName == "" {
					t.Errorf("def %q has empty QualifiedName", def.Name)
					continue
				}
				if def.Name == "" {
					// Anonymous definitions (e.g. package init) — skip
					continue
				}
				// Module-label definitions are file-level entities
				// whose Name is the relPath itself; the contract
				// (ends-with .simpleName, prefix-equality with
				// fqn.Compute) doesn't apply. They're tested
				// separately as part of the registry-prefix lookup
				// path. Skip here.
				if def.Label == "Module" {
					continue
				}

				// Contract: QN must START with the canonical module
				// prefix (fqn.Compute output). The full QN may
				// include an intermediate receiver segment for
				// methods — e.g. `<module>.Router.Route` for
				// `func (r *Router) Route()`. What MUST hold: the
				// prefix is fqn.Compute's output, and the suffix
				// ends with `.<def.Name>`.
				if !strings.HasPrefix(def.QualifiedName, expectedPrefix+".") {
					t.Errorf(
						"QN-format contract violation on def %q (label=%q): "+
							"QualifiedName=%q does not start with %q+\".\". "+
							"This is the shape of incident #2 (2026-05-02): "+
							"CBM emits a QN format the resolver's "+
							"resolveViaSameModule (constructed as "+
							"ctx.ModuleQN + \".\" + callee_name) cannot match.",
						def.Name, def.Label, def.QualifiedName, expectedPrefix)
				}
				if !strings.HasSuffix(def.QualifiedName, "."+def.Name) {
					t.Errorf(
						"QN-format contract violation on def %q (label=%q): "+
							"QualifiedName=%q does not end with \".\"+name. "+
							"Same-module callee lookup requires the QN to "+
							"end with the simple name.",
						def.Name, def.Label, def.QualifiedName)
				}
			}
		})
	}
}

// TestCBMResolverQNContract_DirNameMatchesBasename specifically tests
// the `helpers/helpers.go` shape (file basename equals containing
// directory). Both CBM and fqn.Compute use the multi-segment dotted-
// path formula and produce a doubled segment ("go-min.helpers.helpers")
// for this layout. The CONTRACT is that they AGREE on this shape so
// resolveViaSameModule's lookup matches CBM's emitted QNs.
//
// This is distinct from the oracle-go-ast bug fixed in PR #179, where
// the SYNTHETIC-FIXTURE oracle was emitting a different format
// (filepath.Base only) than fqn.Compute. That fix was on the oracle
// side; CBM and fqn.Compute have always used the multi-segment form.
//
// What this test guards against: any future drift where CBM and
// fqn.Compute disagree on the dir-name-matches-basename layout —
// either side flipping would silently break same-module resolution
// on every dir/file.go pair in the indexed codebase.
func TestCBMResolverQNContract_DirNameMatchesBasename(t *testing.T) {
	const project = "go-min"
	const relPath = "helpers/helpers.go"
	source := `package helpers

func Greet(name string) {}
`
	fr, err := ExtractFile([]byte(source), lang.Go, project, relPath)
	if err != nil {
		t.Fatalf("ExtractFile failed: %v", err)
	}
	if len(fr.Definitions) == 0 {
		t.Fatal("no Definitions emitted")
	}

	expectedPrefix := fqn.Compute(project, relPath, "")
	expectedQN := expectedPrefix + ".Greet"

	for _, def := range fr.Definitions {
		if def.Name != "Greet" || def.Label == "Module" {
			continue
		}
		if def.QualifiedName != expectedQN {
			t.Errorf(
				"CBM↔fqn drift on dir-name-matches-basename layout "+
					"(file=%q, dir=helpers): CBM emitted %q, fqn.Compute "+
					"expects %q. The two MUST agree for "+
					"resolveViaSameModule to match same-package callees. "+
					"Either side flipping silently breaks every dir/file.go "+
					"shape in the indexed codebase — incident #2 territory.",
				relPath, def.QualifiedName, expectedQN)
		}
	}
}

// TestCBMResolverQNContract_SameModuleResolutionShape verifies the
// specific QN structure resolveViaSameModule needs to find. Given a
// file with two functions in the same package, both definitions
// must share a common module prefix that, when concatenated with
// `.<name>`, produces an exact-match QN.
func TestCBMResolverQNContract_SameModuleResolutionShape(t *testing.T) {
	const project = "myapp"
	const relPath = "internal/auth/jwt.go"
	source := `package auth

func Sign(s string) string   { return helper(s) }
func helper(s string) string { return s }
`
	fr, err := ExtractFile([]byte(source), lang.Go, project, relPath)
	if err != nil {
		t.Fatalf("ExtractFile failed: %v", err)
	}

	// Build a name → QN map (mimics what FunctionRegistry.byName holds)
	byName := map[string][]string{}
	for _, def := range fr.Definitions {
		byName[def.Name] = append(byName[def.Name], def.QualifiedName)
	}

	// Compute what resolveViaSameModule would look up for "Sign" calling
	// "helper" inside the same module.
	moduleQN := fqn.Compute(project, relPath, "")
	expectedHelperLookup := moduleQN + "." + "helper"

	helperQNs, ok := byName["helper"]
	if !ok || len(helperQNs) == 0 {
		t.Fatal("helper not in registry; cannot test same-module shape")
	}

	found := false
	for _, qn := range helperQNs {
		if qn == expectedHelperLookup {
			found = true
			break
		}
	}
	if !found {
		t.Errorf(
			"same-module resolution shape violated: resolveViaSameModule "+
				"would look up %q for caller in %q calling 'helper'; "+
				"registered QNs for 'helper' are %v. None matches the "+
				"expected lookup. This is exactly the resolveViaSameModule "+
				"miss that surfaced incident #2.",
			expectedHelperLookup, moduleQN, helperQNs)
	}
}
