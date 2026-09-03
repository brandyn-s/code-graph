package pipeline

import (
	"bytes"
	"log/slog"
	"math"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
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

func TestResolve_AmbiguousTieIsIndependentOfRegistrationOrder(t *testing.T) {
	const (
		caller = "proj.backend.chainlit.server"
		first  = "proj.backend.chainlit.chat_context.ChatContext.get"
		second = "proj.backend.chainlit.session.WebsocketSession.get"
	)

	resolve := func(order []string) string {
		t.Helper()
		reg := NewFunctionRegistry()
		for _, qn := range order {
			reg.Register("get", qn, "Method")
		}
		return reg.Resolve("router.get", caller, nil).QualifiedName
	}

	forward := resolve([]string{first, second})
	reverse := resolve([]string{second, first})
	if forward != reverse {
		t.Fatalf("ambiguous resolution changed with registry order: forward=%q reverse=%q", forward, reverse)
	}
	if forward != first {
		t.Fatalf("ambiguous resolution = %q, want deterministic lexical target %q", forward, first)
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

// TestResolveCtx_ForwardsToLegacy pins the forwarding equivalence
// when CallContext's discrimination fields are empty. As of Phase 3a,
// ReceiverType DOES influence resolution when set — this test is now
// scoped to the "no receiver-type info" path that legacy callers hit.
//
// The Phase 3a discrimination behavior is pinned separately by
// TestApplyReceiverTypeFilter_* and TestResolveCtx_ReceiverType*.
//
// If this test fails, the legacy Resolve(calleeName, moduleQN,
// importMap) wrapper has stopped producing the same result as
// ResolveCtx with empty discrimination fields.
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
				CallerQN:   "svcZ.caller.SomeFunc",
				ModuleQN:   tc.moduleQN,
				ImportMap:  tc.importMap,
				// Phase 3a+ discrimination fields left empty: this
				// test pins the legacy-shape invariant only.
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

// TestFuzzyResolveCtx_ForwardsToLegacy pins the legacy-shape forwarding
// invariant for the fuzzy path: with empty discrimination fields,
// FuzzyResolveCtx must produce the same tuple as legacy FuzzyResolve.
// Phase 3a discrimination is pinned separately.
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
				// Phase 3a+ discrimination fields left empty.
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

// --- Phase 3a: receiver-type discrimination ---

// TestApplyReceiverTypeFilter_PrefersMatchingMethod proves that when
// ctx.ReceiverType matches one Method candidate's parent class,
// the filter selects only that candidate.
func TestApplyReceiverTypeFilter_PrefersMatchingMethod(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("get_result", "proj.repo.AssetRepo.get_result", "Method")
	reg.Register("get_result", "proj.svc.IntrospectService.get_result", "Method")

	candidates := []string{
		"proj.repo.AssetRepo.get_result",
		"proj.svc.IntrospectService.get_result",
	}

	ctx := CallContext{
		CalleeName:   "repo.get_result", // method-call shape
		ReceiverType: "proj.repo.AssetRepo",
	}

	filtered, applied, dropAll := reg.applyReceiverTypeFilter(ctx, candidates)
	if applied != "receiver-type-match" {
		t.Errorf("applied = %q, want %q", applied, "receiver-type-match")
	}
	if dropAll {
		t.Error("dropAll = true, want false")
	}
	if len(filtered) != 1 || filtered[0] != "proj.repo.AssetRepo.get_result" {
		t.Errorf("filtered = %v, want [proj.repo.AssetRepo.get_result]", filtered)
	}
}

// TestApplyReceiverTypeFilter_DropsExternalCall proves that when
// ctx.ReceiverType is set but no Method candidate's parent class
// matches, the filter signals "drop the binding". This is the case
// that eliminates phantom emissions for external chain calls.
func TestApplyReceiverTypeFilter_DropsExternalCall(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("execute", "proj.repo.AssetRepo.execute", "Method")
	reg.Register("execute", "proj.cache.Cache.execute", "Method")

	candidates := []string{
		"proj.repo.AssetRepo.execute",
		"proj.cache.Cache.execute",
	}

	ctx := CallContext{
		CalleeName:   "users.execute",      // method-call shape
		ReceiverType: "diesel.query.Users", // external — no internal candidate matches
	}

	_, applied, dropAll := reg.applyReceiverTypeFilter(ctx, candidates)
	if applied != "receiver-type-no-internal-match" {
		t.Errorf("applied = %q, want %q", applied, "receiver-type-no-internal-match")
	}
	if !dropAll {
		t.Error("dropAll = false, want true (external receiver)")
	}
}

// TestApplyReceiverTypeFilter_BareNameCallPassesThrough proves that
// free-function calls (no dot in calleeName) are NOT filtered by
// receiver-type — Phase 3b's import-binding tier handles those.
func TestApplyReceiverTypeFilter_BareNameCallPassesThrough(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("ready", "proj.state.ready", "Function")

	candidates := []string{"proj.state.ready"}

	ctx := CallContext{
		CalleeName:   "ready", // bare-name shape
		ReceiverType: "futures.future.ReadyFuture",
	}

	filtered, applied, dropAll := reg.applyReceiverTypeFilter(ctx, candidates)
	if applied != "" {
		t.Errorf("applied = %q, want empty (bare-name call should pass through)", applied)
	}
	if dropAll {
		t.Error("dropAll = true, want false")
	}
	if len(filtered) != 1 {
		t.Errorf("filtered should be unchanged, got %v", filtered)
	}
}

// TestApplyReceiverTypeFilter_NoReceiverTypeUnknown proves that when
// ctx.ReceiverType is empty (the legacy / chain-resolution-failed
// case), no discrimination occurs and candidates pass through.
func TestApplyReceiverTypeFilter_EmptyReceiverPassesThrough(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Process", "svcA.Process", "Method")
	reg.Register("Process", "svcB.Process", "Method")

	candidates := []string{"svcA.Process", "svcB.Process"}

	ctx := CallContext{
		CalleeName:   "obj.Process",
		ReceiverType: "", // unknown — Phase 1/2 path
	}

	filtered, applied, dropAll := reg.applyReceiverTypeFilter(ctx, candidates)
	if applied != "" || dropAll || len(filtered) != 2 {
		t.Errorf("expected pass-through, got applied=%q dropAll=%v filtered=%v", applied, dropAll, filtered)
	}
}

// TestApplyReceiverTypeFilter_NonMethodCandidatesPassThrough proves
// that Function/Class/etc. candidates are not filtered when mixed
// with Method candidates — they have no parent-class semantics.
// Method-vs-Method discrimination still applies to the Method subset.
func TestApplyReceiverTypeFilter_NonMethodCandidatesPassThrough(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("execute", "proj.repo.AssetRepo.execute", "Method")
	reg.Register("execute", "proj.helpers.execute", "Function") // not a method

	candidates := []string{
		"proj.repo.AssetRepo.execute",
		"proj.helpers.execute",
	}

	ctx := CallContext{
		CalleeName:   "repo.execute",
		ReceiverType: "proj.repo.AssetRepo",
	}

	filtered, applied, dropAll := reg.applyReceiverTypeFilter(ctx, candidates)
	// Both candidates should survive: AssetRepo.execute matches by
	// receiver type; the Function candidate passes through because
	// receiver-type discrimination is undefined for non-Methods.
	// Since the candidate set is unchanged, no discrimination was
	// "applied" — the function returns "" so the caller's legacy
	// suffix-match / import-distance logic decides.
	if applied != "" {
		t.Errorf("applied = %q, want \"\" (unchanged candidate set)", applied)
	}
	if dropAll {
		t.Error("dropAll = true, want false")
	}
	if len(filtered) != 2 {
		t.Errorf("filtered = %v, want both candidates", filtered)
	}
}

// TestResolveCtx_ReceiverTypeDiscrimination_EndToEnd verifies the
// full ResolveCtx path with Tier 2 active: a method-call with a
// known receiver type binds to the correct internal Method even
// when bare-name suffix-match would otherwise produce conflation.
func TestResolveCtx_ReceiverTypeDiscrimination_EndToEnd(t *testing.T) {
	reg := NewFunctionRegistry()
	// Two Methods with the same bare name — classic conflation setup.
	reg.Register("get_result", "proj.repo.AssetRepo.get_result", "Method")
	reg.Register("get_result", "proj.svc.IntrospectService.get_result", "Method")

	// Without ReceiverType: legacy bare-name resolution picks one
	// (whichever the resolver's tiebreak prefers).
	legacyCtx := CallContext{
		CalleeName: "obj.get_result",
		ModuleQN:   "proj.main",
	}
	legacyRes := reg.ResolveCtx(legacyCtx)
	if legacyRes.QualifiedName == "" {
		t.Fatal("legacy resolution should produce some result")
	}

	// With ReceiverType pointing at IntrospectService: discrimination
	// must select that target specifically, regardless of which one
	// legacy picked.
	discrimCtx := CallContext{
		CalleeName:   "service.get_result",
		ModuleQN:     "proj.main",
		ReceiverType: "proj.svc.IntrospectService",
	}
	discrimRes := reg.ResolveCtx(discrimCtx)
	if discrimRes.QualifiedName != "proj.svc.IntrospectService.get_result" {
		t.Errorf("discriminated result = %q, want proj.svc.IntrospectService.get_result", discrimRes.QualifiedName)
	}
}

// TestResolveCtx_ExternalReceiverDropsBinding proves that when the
// receiver type is external (no internal candidate matches), the
// resolver returns empty instead of falling through to a bare-name
// suffix-match phantom. This is the specific case that eliminates
// `entry → AssetRepo.execute` for a Diesel `users.execute(conn)` call.
func TestResolveCtx_ExternalReceiverDropsBinding(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("execute", "proj.repo.AssetRepo.execute", "Method")

	ctx := CallContext{
		CalleeName:   "users.execute",
		ModuleQN:     "proj.main",
		ReceiverType: "diesel.query.Users", // external receiver
	}
	res := reg.ResolveCtx(ctx)
	if res.QualifiedName != "" {
		t.Errorf("expected empty result (external receiver should drop binding), got %q", res.QualifiedName)
	}
}

// TestSplitCalleeName pins the helper extracted in Phase 2 — every strategy
// that needs prefix/suffix relies on it. Behavior must match the inline
// code each strategy used pre-Phase-2.
func TestSplitCalleeName(t *testing.T) {
	cases := []struct {
		in         string
		wantPrefix string
		wantSuffix string
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

// TestApplyReceiverTypeFilter_DebugLogging proves that when tier2DebugEnabled
// is true, applyReceiverTypeFilter emits one slog record per call describing
// which short-circuit path was taken. Used by Phase B of the assetman Tier-2
// diagnostic plan (knowledge-base PR #491) to classify the 72 ambiguous
// fuzzy edges by short-circuit path.
func TestApplyReceiverTypeFilter_DebugLogging(t *testing.T) {
	// Save and restore the package-level flag + default slog handler.
	origFlag := tier2DebugEnabled
	origLogger := slog.Default()
	t.Cleanup(func() {
		tier2DebugEnabled = origFlag
		slog.SetDefault(origLogger)
	})

	cases := []struct {
		name           string
		setupReg       func(*FunctionRegistry)
		ctx            CallContext
		candidates     []string
		wantPath       string
		wantPathSubstr string
	}{
		{
			name:       "empty_receiver",
			setupReg:   func(r *FunctionRegistry) {},
			ctx:        CallContext{CalleeName: "foo.bar", ReceiverType: ""},
			candidates: []string{"a.b"},
			wantPath:   "empty_receiver_or_no_candidates",
		},
		{
			name:       "not_method_call_shape",
			setupReg:   func(r *FunctionRegistry) {},
			ctx:        CallContext{CalleeName: "foo", ReceiverType: "SomeType"},
			candidates: []string{"a.b"},
			wantPath:   "not_method_call_shape",
		},
		{
			name: "drop_all_no_internal_match",
			setupReg: func(r *FunctionRegistry) {
				r.Register("execute", "proj.repo.Foo.execute", "Method")
			},
			ctx:        CallContext{CalleeName: "diesel.execute", ReceiverType: "diesel.Query"},
			candidates: []string{"proj.repo.Foo.execute"},
			wantPath:   "drop_all_no_internal_match",
		},
		{
			name: "narrowed",
			setupReg: func(r *FunctionRegistry) {
				r.Register("get", "proj.repo.AssetRepo.get", "Method")
				r.Register("get", "proj.repo.UserRepo.get", "Method")
			},
			ctx:        CallContext{CalleeName: "asset_repo.get", ReceiverType: "proj.repo.AssetRepo"},
			candidates: []string{"proj.repo.AssetRepo.get", "proj.repo.UserRepo.get"},
			wantPath:   "narrowed",
		},
		{
			name: "pass_through_no_method",
			setupReg: func(r *FunctionRegistry) {
				r.Register("util", "proj.util", "Function")
			},
			ctx:        CallContext{CalleeName: "foo.util", ReceiverType: "SomeType"},
			candidates: []string{"proj.util"},
			wantPath:   "pass_through",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			tier2DebugEnabled = true

			reg := NewFunctionRegistry()
			tc.setupReg(reg)
			reg.applyReceiverTypeFilter(tc.ctx, tc.candidates)

			out := buf.String()
			if !strings.Contains(out, "tier2.short_circuit") {
				t.Fatalf("expected slog record with msg=tier2.short_circuit, got: %s", out)
			}
			if !strings.Contains(out, "path="+tc.wantPath) {
				t.Errorf("expected path=%q, got: %s", tc.wantPath, out)
			}
		})
	}

	// Also verify that with tier2DebugEnabled=false, NO records emit.
	t.Run("disabled_emits_nothing", func(t *testing.T) {
		var buf bytes.Buffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		tier2DebugEnabled = false

		reg := NewFunctionRegistry()
		reg.applyReceiverTypeFilter(CallContext{CalleeName: "foo.bar"}, []string{"a.b"})

		if buf.Len() > 0 {
			t.Errorf("expected no output when tier2DebugEnabled=false, got: %s", buf.String())
		}
	})
}

func TestParseEnvPolicy(t *testing.T) {
	t.Setenv("TEST_POLICY_UNSET", "")
	if got := parseEnvPolicy("TEST_POLICY_UNSET"); got != envPolicyUnset {
		t.Errorf("empty env: got %v want envPolicyUnset", got)
	}
	for _, v := range []string{"0", "f", "false", "F", "FALSE", "n", "no", "N", "NO"} {
		t.Setenv("TEST_POLICY_FALSY", v)
		if got := parseEnvPolicy("TEST_POLICY_FALSY"); got != envPolicyForceOff {
			t.Errorf("value %q: got %v want envPolicyForceOff", v, got)
		}
	}
	for _, v := range []string{"1", "true", "yes", "TRUE", "True", "on"} {
		t.Setenv("TEST_POLICY_TRUTHY", v)
		if got := parseEnvPolicy("TEST_POLICY_TRUTHY"); got != envPolicyForceOn {
			t.Errorf("value %q: got %v want envPolicyForceOn", v, got)
		}
	}
}

func TestShouldDropFuzzyJanusianChains_DefaultPythonOnly(t *testing.T) {
	// Phase E (2026-05-14): default policy is Python only. With env unset,
	// shouldDropFuzzyJanusianChains returns true for Python and false for
	// every other language. Save + restore the package-level policy so the
	// test doesn't leak state to other tests in this package.
	prev := fuzzyJanusianChainEnvPolicy
	fuzzyJanusianChainEnvPolicy = envPolicyUnset
	t.Cleanup(func() { fuzzyJanusianChainEnvPolicy = prev })

	if !shouldDropFuzzyJanusianChains(lang.Python) {
		t.Errorf("Python under default policy: expected gate ON")
	}
	for _, l := range []lang.Language{lang.Rust, lang.Go, lang.JavaScript, lang.TypeScript, lang.Java} {
		if shouldDropFuzzyJanusianChains(l) {
			t.Errorf("%v under default policy: expected gate OFF (Phase E scoped Python only)", l)
		}
	}
}

func TestShouldDropFuzzyJanusianChains_EnvForceOnOverridesLanguage(t *testing.T) {
	prev := fuzzyJanusianChainEnvPolicy
	fuzzyJanusianChainEnvPolicy = envPolicyForceOn
	t.Cleanup(func() { fuzzyJanusianChainEnvPolicy = prev })

	for _, l := range []lang.Language{lang.Python, lang.Rust, lang.Go, lang.JavaScript} {
		if !shouldDropFuzzyJanusianChains(l) {
			t.Errorf("%v under forceOn: expected gate ON", l)
		}
	}
}

func TestShouldDropFuzzyJanusianChains_EnvForceOffOverridesLanguage(t *testing.T) {
	prev := fuzzyJanusianChainEnvPolicy
	fuzzyJanusianChainEnvPolicy = envPolicyForceOff
	t.Cleanup(func() { fuzzyJanusianChainEnvPolicy = prev })

	for _, l := range []lang.Language{lang.Python, lang.Rust, lang.Go, lang.JavaScript} {
		if shouldDropFuzzyJanusianChains(l) {
			t.Errorf("%v under forceOff: expected gate OFF", l)
		}
	}
}
