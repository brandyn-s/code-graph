package pipeline

import (
	"math"
	"testing"
)

func TestFuzzyResolve_SingleCandidate(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("CreateOrder", "svcA.handlers.CreateOrder", "Function")
	reg.Register("ValidateOrder", "svcB.validators.ValidateOrder", "Function")

	// Normal resolve with no import map should find unique name
	result := reg.Resolve("CreateOrder", "svcC.caller", nil)
	if result.QualifiedName != "svcA.handlers.CreateOrder" {
		t.Errorf("Resolve: expected svcA.handlers.CreateOrder, got %s", result.QualifiedName)
	}

	// FuzzyResolve should find by simple name even with unknown prefix
	fuzzyResult, ok := reg.FuzzyResolve("unknownPkg.CreateOrder", "svcC.caller", nil)
	if !ok {
		t.Fatal("expected fuzzy match")
	}
	if fuzzyResult.QualifiedName != "svcA.handlers.CreateOrder" {
		t.Errorf("expected svcA.handlers.CreateOrder, got %s", fuzzyResult.QualifiedName)
	}
}

func TestFuzzyResolve_NonExistentName(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("CreateOrder", "svcA.handlers.CreateOrder", "Function")

	_, ok := reg.FuzzyResolve("NonExistent", "svcC.caller", nil)
	if ok {
		t.Fatal("expected no fuzzy match for non-existent name")
	}
}

func TestFuzzyResolve_MultipleCandidates_BestByDistance(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Process", "svcA.handlers.Process", "Function")
	reg.Register("Process", "svcB.handlers.Process", "Function")

	// Caller is in svcA — should prefer svcA.handlers.Process
	fuzzyResult, ok := reg.FuzzyResolve("unknown.Process", "svcA.other", nil)
	if !ok {
		t.Fatal("expected fuzzy match")
	}
	if fuzzyResult.QualifiedName != "svcA.handlers.Process" {
		t.Errorf("expected svcA.handlers.Process, got %s", fuzzyResult.QualifiedName)
	}

	// Caller is in svcB — should prefer svcB.handlers.Process
	fuzzyResult, ok = reg.FuzzyResolve("unknown.Process", "svcB.other", nil)
	if !ok {
		t.Fatal("expected fuzzy match")
	}
	if fuzzyResult.QualifiedName != "svcB.handlers.Process" {
		t.Errorf("expected svcB.handlers.Process, got %s", fuzzyResult.QualifiedName)
	}
}

func TestFuzzyResolve_SimpleNameExtraction(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("DoWork", "myproject.utils.DoWork", "Function")

	// Deeply qualified callee — should extract "DoWork" as simple name
	fuzzyResult, ok := reg.FuzzyResolve("some.deep.module.DoWork", "myproject.caller", nil)
	if !ok {
		t.Fatal("expected fuzzy match")
	}
	if fuzzyResult.QualifiedName != "myproject.utils.DoWork" {
		t.Errorf("expected myproject.utils.DoWork, got %s", fuzzyResult.QualifiedName)
	}
}

func TestFuzzyResolve_NoMatchForBareName(t *testing.T) {
	reg := NewFunctionRegistry()
	// Register nothing

	_, ok := reg.FuzzyResolve("SomeFunc", "myproject.caller", nil)
	if ok {
		t.Fatal("expected no fuzzy match on empty registry")
	}
}

func TestRegistryExists(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Foo", "pkg.module.Foo", "Function")
	reg.Register("Bar", "pkg.module.Bar", "Method")

	if !reg.Exists("pkg.module.Foo") {
		t.Error("expected Foo to exist")
	}
	if !reg.Exists("pkg.module.Bar") {
		t.Error("expected Bar to exist")
	}
	if reg.Exists("pkg.module.Missing") {
		t.Error("expected Missing to not exist")
	}
	if reg.Exists("") {
		t.Error("expected empty string to not exist")
	}
}

// --- Phase 1: Confidence scoring tests ---

func assertConfidence(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.01 {
		t.Errorf("%s: confidence = %.2f, want %.2f", label, got, want)
	}
}

func TestResolveConfidence_ImportMap(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Foo", "proj.other.Foo", "Function")

	imports := map[string]string{"other": "proj.other"}
	result := reg.Resolve("other.Foo", "proj.pkg", imports)
	if result.QualifiedName != "proj.other.Foo" {
		t.Fatalf("expected proj.other.Foo, got %s", result.QualifiedName)
	}
	assertConfidence(t, "import_map", result.Confidence, 0.95)
	if result.Strategy != "import_map" {
		t.Errorf("strategy = %s, want import_map", result.Strategy)
	}
}

func TestResolveConfidence_ImportMapSuffix(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Foo", "proj.other.sub.Foo", "Function")

	imports := map[string]string{"other": "proj.other"}
	result := reg.Resolve("other.Foo", "proj.pkg", imports)
	if result.QualifiedName != "proj.other.sub.Foo" {
		t.Fatalf("expected proj.other.sub.Foo, got %s", result.QualifiedName)
	}
	assertConfidence(t, "import_map_suffix", result.Confidence, 0.85)
	if result.Strategy != "import_map_suffix" {
		t.Errorf("strategy = %s, want import_map_suffix", result.Strategy)
	}
}

func TestResolveConfidence_SameModule(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Foo", "proj.pkg.Foo", "Function")

	result := reg.Resolve("Foo", "proj.pkg", nil)
	if result.QualifiedName != "proj.pkg.Foo" {
		t.Fatalf("expected proj.pkg.Foo, got %s", result.QualifiedName)
	}
	assertConfidence(t, "same_module", result.Confidence, 0.90)
	if result.Strategy != "same_module" {
		t.Errorf("strategy = %s, want same_module", result.Strategy)
	}
}

func TestResolveConfidence_UniqueName(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Bar", "proj.pkg.Bar", "Function")

	result := reg.Resolve("Bar", "proj.unrelated", nil)
	if result.QualifiedName != "proj.pkg.Bar" {
		t.Fatalf("expected proj.pkg.Bar, got %s", result.QualifiedName)
	}
	assertConfidence(t, "unique_name", result.Confidence, 0.75)
	if result.Strategy != "unique_name" {
		t.Errorf("strategy = %s, want unique_name", result.Strategy)
	}
}

func TestResolveConfidence_SuffixMatch(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Process", "proj.svcA.Process", "Function")
	reg.Register("Process", "proj.svcB.Process", "Function")

	result := reg.Resolve("Process", "proj.svcA.caller", nil)
	if result.QualifiedName != "proj.svcA.Process" {
		t.Fatalf("expected proj.svcA.Process, got %s", result.QualifiedName)
	}
	assertConfidence(t, "suffix_match", result.Confidence, 0.55)
	if result.Strategy != "suffix_match" {
		t.Errorf("strategy = %s, want suffix_match", result.Strategy)
	}
}

func TestFuzzyResolveConfidence_Single(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Handler", "proj.svc.Handler", "Function")

	result, ok := reg.FuzzyResolve("unknownPkg.Handler", "proj.caller", nil)
	if !ok {
		t.Fatal("expected fuzzy match")
	}
	assertConfidence(t, "fuzzy_single", result.Confidence, 0.40)
	if result.Strategy != "fuzzy" {
		t.Errorf("strategy = %s, want fuzzy", result.Strategy)
	}
}

func TestFuzzyResolveConfidence_Distance(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Process", "proj.svcA.Process", "Function")
	reg.Register("Process", "proj.svcB.Process", "Function")

	result, ok := reg.FuzzyResolve("unknownPkg.Process", "proj.svcA.other", nil)
	if !ok {
		t.Fatal("expected fuzzy match")
	}
	assertConfidence(t, "fuzzy_distance", result.Confidence, 0.30)
	if result.Strategy != "fuzzy" {
		t.Errorf("strategy = %s, want fuzzy", result.Strategy)
	}
}

// --- Phase 3: Negative import evidence tests ---

func TestNegativeImportEvidence_RejectsUnimported(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Process", "proj.billing.Process", "Function")
	reg.Register("Process", "proj.handler.Process", "Function")

	// Import only handler's module — should prefer handler
	imports := map[string]string{"handler": "proj.handler"}
	result := reg.Resolve("Process", "proj.caller", imports)
	if result.QualifiedName != "proj.handler.Process" {
		t.Errorf("expected proj.handler.Process, got %s", result.QualifiedName)
	}
}

func TestNegativeImportEvidence_FuzzyPenalty(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Handler", "proj.billing.Handler", "Function")

	// No import for billing module — confidence should be halved
	imports := map[string]string{"other": "proj.other"}
	result, ok := reg.FuzzyResolve("unknown.Handler", "proj.caller", imports)
	if !ok {
		t.Fatal("expected fuzzy match")
	}
	// 0.40 * 0.5 = 0.20 (penalty for unreachable import)
	assertConfidence(t, "fuzzy_penalty", result.Confidence, 0.20)
}

func TestNegativeImportEvidence_NoImportMapPassthrough(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Handler", "proj.billing.Handler", "Function")

	// nil import map — no filtering, full confidence
	result, ok := reg.FuzzyResolve("unknown.Handler", "proj.caller", nil)
	if !ok {
		t.Fatal("expected fuzzy match")
	}
	assertConfidence(t, "no_importmap", result.Confidence, 0.40)
}

func TestConfidenceBand(t *testing.T) {
	tests := []struct {
		score float64
		want  string
	}{
		{0.95, "high"},
		{0.70, "high"},
		{0.55, "medium"},
		{0.45, "medium"},
		{0.40, "speculative"},
		{0.20, "speculative"},
	}
	for _, tt := range tests {
		got := confidenceBand(tt.score)
		if got != tt.want {
			t.Errorf("confidenceBand(%.2f) = %s, want %s", tt.score, got, tt.want)
		}
	}
}

func TestIsImportReachable(t *testing.T) {
	imports := map[string]string{
		"handler": "proj.handler",
		"utils":   "proj.shared.utils",
	}

	tests := []struct {
		candidate string
		want      bool
	}{
		{"proj.handler.Process", true},
		{"proj.handler.sub.Process", true},
		{"proj.shared.utils.Helper", true},
		{"proj.billing.Process", false},
		{"unrelated.pkg.Func", false},
	}
	for _, tt := range tests {
		got := isImportReachable(tt.candidate, imports)
		if got != tt.want {
			t.Errorf("isImportReachable(%s) = %v, want %v", tt.candidate, got, tt.want)
		}
	}
}

// --- registry.Resolve consolidation: Phase 1 forwarding equivalence ---

// TestResolveCtx_ForwardsToLegacy pins the Phase 1 invariant: ResolveCtx
// must produce the same ResolutionResult as the legacy Resolve(calleeName,
// moduleQN, importMap) signature for the same inputs. The Phase 2+ fields
// on CallContext (ReceiverType, ImportBindings, Aliases) are reserved and
// must NOT influence resolution today.
//
// If this test fails, Phase 1 has introduced a behavior change — the
// negative-fixture corpus baseline at bench/accuracy/negative_baselines.json
// is no longer the right comparator and the consolidation has lost its
// no-op guarantee.
func TestResolveCtx_ForwardsToLegacy(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("CreateOrder", "svcA.handlers.CreateOrder", "Function")
	reg.Register("Process", "svcB.workers.Process", "Function")
	reg.Register("Process", "svcC.helpers.Process", "Function")
	reg.Register("Validate", "svcA.validators.Validate", "Method")

	imports := map[string]string{
		"svcA": "svcA",
		"svcB": "svcB",
	}

	cases := []struct {
		name       string
		calleeName string
		moduleQN   string
		importMap  map[string]string
	}{
		{"unique-name-no-imports", "CreateOrder", "svcZ.caller", nil},
		{"unique-name-with-imports", "CreateOrder", "svcZ.caller", imports},
		{"qualified-import-map-hit", "svcA.CreateOrder", "svcZ.caller", imports},
		{"multi-candidate-suffix-match", "Process", "svcB.caller", imports},
		{"unresolvable-bare-name", "Nonexistent", "svcZ.caller", nil},
		{"empty-callee", "", "svcZ.caller", nil},
		{"method-call-shape", "v.Validate", "svcA.caller", imports},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy := reg.Resolve(tc.calleeName, tc.moduleQN, tc.importMap)
			ctx := CallContext{
				CalleeName: tc.calleeName,
				CallerQN:   "svcZ.caller.SomeFunc", // Phase 1: unused
				ModuleQN:   tc.moduleQN,
				ImportMap:  tc.importMap,
				// Phase 2+ fields: deliberately set to non-empty values that
				// MUST NOT influence resolution today. If they ever do,
				// this test catches it.
				ReceiverType:   "svcA.handlers.SomeStruct",
				ImportBindings: map[string]string{"CreateOrder": "external.lib.CreateOrder"},
				Aliases:        map[string]string{"alias": "svcA.something"},
			}
			ctxResult := reg.ResolveCtx(ctx)
			if ctxResult != legacy {
				t.Errorf("ResolveCtx diverged from Resolve\n  legacy = %#v\n  ctx    = %#v", legacy, ctxResult)
			}
		})
	}
}

// TestCallContext_FieldsArePresent guards against accidental removal of the
// Phase 2+ fields. Their existence is the Phase 1 commitment: callers can
// populate them today (a no-op) and Phase 2+ ships consumption without
// requiring upstream signature changes.
func TestCallContext_FieldsArePresent(t *testing.T) {
	// Construct with every field set. Compile-failure here means a field
	// was renamed or removed.
	_ = CallContext{
		CalleeName:     "x",
		CallerQN:       "x",
		ModuleQN:       "x",
		ImportMap:      map[string]string{},
		ReceiverType:   "x",
		ImportBindings: map[string]string{},
		Aliases:        map[string]string{},
	}
}

// TestFuzzyResolveCtx_ForwardsToLegacy pins the Phase 2 invariant for the
// fuzzy path: FuzzyResolveCtx must produce the same (ResolutionResult,
// ok) tuple as the legacy FuzzyResolve(calleeName, moduleQN, importMap)
// signature for the same inputs. The Phase 2+ fields on CallContext must
// not influence resolution today.
func TestFuzzyResolveCtx_ForwardsToLegacy(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("CreateOrder", "svcA.handlers.CreateOrder", "Function")
	reg.Register("Process", "svcB.workers.Process", "Function")
	reg.Register("Process", "svcC.helpers.Process", "Function")

	imports := map[string]string{"svcA": "svcA"}

	cases := []struct {
		name       string
		calleeName string
		moduleQN   string
		importMap  map[string]string
	}{
		{"unique-name", "CreateOrder", "svcZ.caller", nil},
		{"qualified-callee", "unknown.CreateOrder", "svcZ.caller", nil},
		{"multi-candidate-by-distance-A", "Process", "svcB.x", imports},
		{"multi-candidate-by-distance-C", "Process", "svcC.x", imports},
		{"unresolvable-bare", "Nonexistent", "svcZ.caller", nil},
		{"deep-qualified", "some.deep.path.CreateOrder", "svcZ.caller", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacyRes, legacyOk := reg.FuzzyResolve(tc.calleeName, tc.moduleQN, tc.importMap)
			ctx := CallContext{
				CalleeName: tc.calleeName,
				CallerQN:   "svcZ.caller.SomeFunc",
				ModuleQN:   tc.moduleQN,
				ImportMap:  tc.importMap,
				// Phase 2+ fields populated to prove they don't influence today.
				ReceiverType:   "svcA.handlers.SomeStruct",
				ImportBindings: map[string]string{"CreateOrder": "external.lib.CreateOrder"},
				Aliases:        map[string]string{"alias": "svcA.something"},
			}
			ctxRes, ctxOk := reg.FuzzyResolveCtx(ctx)
			if ctxOk != legacyOk {
				t.Errorf("FuzzyResolveCtx ok diverged: legacy=%v ctx=%v", legacyOk, ctxOk)
			}
			if ctxRes != legacyRes {
				t.Errorf("FuzzyResolveCtx result diverged\n  legacy = %#v\n  ctx    = %#v", legacyRes, ctxRes)
			}
		})
	}
}

// TestSplitCalleeName pins the helper extracted in Phase 2 — every strategy
// that needs prefix/suffix relies on it. Behavior must match the inline
// code each strategy used pre-Phase-2.
func TestSplitCalleeName(t *testing.T) {
	cases := []struct {
		in            string
		wantPrefix    string
		wantSuffix    string
	}{
		{"", "", ""},
		{"bare", "bare", ""},
		{"pkg.Func", "pkg", "Func"},
		{"obj.field.method", "obj", "field.method"}, // SplitN(2) keeps tail intact
		{".leading", "", "leading"},
		{"trailing.", "trailing", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			gotPrefix, gotSuffix := splitCalleeName(tc.in)
			if gotPrefix != tc.wantPrefix || gotSuffix != tc.wantSuffix {
				t.Errorf("splitCalleeName(%q) = (%q, %q), want (%q, %q)",
					tc.in, gotPrefix, gotSuffix, tc.wantPrefix, tc.wantSuffix)
			}
		})
	}
}
