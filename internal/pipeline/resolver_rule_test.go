package pipeline

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
)

// TestResolverRuleFromLSPStrategy_TypeDispatch — Go LSP type-dispatch
// (LSP resolved obj's static type then looked up method on that type)
// must classify as receiver-qualified. Mirrors lsp_test.go's pinned
// "lsp_type_dispatch" strategy string.
func TestResolverRuleFromLSPStrategy_TypeDispatch(t *testing.T) {
	got := resolverRuleFromLSPStrategy("lsp_type_dispatch")
	if got != ResolverRuleReceiverQualified {
		t.Errorf("lsp_type_dispatch must be receiver-qualified, got %q", got)
	}
}

// TestResolverRuleFromLSPStrategy_EmbedDispatch — embedded-type dispatch
// (Go's struct embedding) classifies as receiver-qualified — same
// semantic as direct type dispatch with one indirection layer.
func TestResolverRuleFromLSPStrategy_EmbedDispatch(t *testing.T) {
	got := resolverRuleFromLSPStrategy("lsp_embed_dispatch")
	if got != ResolverRuleReceiverQualified {
		t.Errorf("lsp_embed_dispatch must be receiver-qualified, got %q", got)
	}
}

// TestResolverRuleFromLSPStrategy_InterfaceDispatch — Go interface
// satisfaction must classify as interface-dispatch.
func TestResolverRuleFromLSPStrategy_InterfaceDispatch(t *testing.T) {
	got := resolverRuleFromLSPStrategy("lsp_interface_dispatch")
	if got != ResolverRuleInterfaceDispatch {
		t.Errorf("lsp_interface_dispatch must be interface-dispatch, got %q", got)
	}
}

// TestResolverRuleFromLSPStrategy_InterfaceResolve — alternate interface
// resolution path also routes to interface-dispatch.
func TestResolverRuleFromLSPStrategy_InterfaceResolve(t *testing.T) {
	got := resolverRuleFromLSPStrategy("lsp_interface_resolve")
	if got != ResolverRuleInterfaceDispatch {
		t.Errorf("lsp_interface_resolve must be interface-dispatch, got %q", got)
	}
}

// TestResolverRuleFromLSPStrategy_DefaultExactQN — any unrecognized LSP
// strategy (including empty) routes to exact-qn-match. The LSP only
// emits a ResolvedCall after type-aware resolution succeeded, so a
// non-dispatch strategy still represents an exact-resolved QN.
func TestResolverRuleFromLSPStrategy_DefaultExactQN(t *testing.T) {
	cases := []string{"", "lsp_resolved", "novel_strategy_xyz"}
	for _, s := range cases {
		got := resolverRuleFromLSPStrategy(s)
		if got != ResolverRuleExactQN {
			t.Errorf("LSP strategy %q must default to exact-qn-match, got %q", s, got)
		}
	}
}

// TestResolverRuleFromRegistryStrategy_TypeDispatch — Go-side TypeMap
// classQN+method resolution (resolveCallWithTypes) maps to
// interface-dispatch.
func TestResolverRuleFromRegistryStrategy_TypeDispatch(t *testing.T) {
	got := resolverRuleFromRegistryStrategy("type_dispatch")
	if got != ResolverRuleInterfaceDispatch {
		t.Errorf("type_dispatch must be interface-dispatch, got %q", got)
	}
}

// TestResolverRuleFromRegistryStrategy_SameModule — same-module
// resolution maps to same-package-shadow.
func TestResolverRuleFromRegistryStrategy_SameModule(t *testing.T) {
	got := resolverRuleFromRegistryStrategy("same_module")
	if got != ResolverRuleSamePackageShadow {
		t.Errorf("same_module must be same-package-shadow, got %q", got)
	}
}

// TestResolverRuleFromRegistryStrategy_CrossPackageHeuristics — the
// four cross-package heuristic strategies (import_map,
// import_map_suffix, unique_name, suffix_match) all bucket as
// cross-package-heuristic.
func TestResolverRuleFromRegistryStrategy_CrossPackageHeuristics(t *testing.T) {
	cases := []string{"import_map", "import_map_suffix", "unique_name", "suffix_match"}
	for _, s := range cases {
		got := resolverRuleFromRegistryStrategy(s)
		if got != ResolverRuleCrossPackageHeuristic {
			t.Errorf("%q must be cross-package-heuristic, got %q", s, got)
		}
	}
}

// TestResolverRuleFromRegistryStrategy_Fuzzy — last-resort
// FuzzyResolve path classifies as fuzzy-resolve.
func TestResolverRuleFromRegistryStrategy_Fuzzy(t *testing.T) {
	got := resolverRuleFromRegistryStrategy("fuzzy")
	if got != ResolverRuleFuzzyResolve {
		t.Errorf("fuzzy must be fuzzy-resolve, got %q", got)
	}
}

// TestResolverRuleFromRegistryStrategy_DefaultUnknown — any unrecognized
// registry strategy routes to unknown. Tripwire for new resolver
// strategies that haven't been classified yet.
func TestResolverRuleFromRegistryStrategy_DefaultUnknown(t *testing.T) {
	cases := []string{"", "novel_strategy_abc", "type_dispatch_v2"}
	for _, s := range cases {
		got := resolverRuleFromRegistryStrategy(s)
		if got != ResolverRuleUnknown {
			t.Errorf("registry strategy %q must default to unknown, got %q", s, got)
		}
	}
}

// TestResolveCallEdge_PseudoEdgeIsModalPseudo — when EnclosingFuncQN
// is empty, resolveCallEdge substitutes moduleQN and tags the edge
// CALLS_PSEUDO. resolver_rule must be modal-pseudo (overrides whatever
// underlying registry rule would have chosen).
func TestResolveCallEdge_PseudoEdgeIsModalPseudo(t *testing.T) {
	p := &Pipeline{
		ProjectName: "proj",
		registry:    NewFunctionRegistry(),
	}
	p.registry.Register("Target", "proj.main.Target", "Function")

	moduleQN := "proj.main"
	importMap := map[string]string{}
	typeMap := TypeMap{}
	lspCallerMethods := map[string]bool{}

	// EnclosingFuncQN empty → CALLS_PSEUDO. Underlying same-module
	// resolution produces same-package-shadow rule which modal-pseudo
	// must override.
	pseudoCall := cbm.Call{CalleeName: "Target", EnclosingFuncQN: ""}
	edge, ok := p.resolveCallEdge(pseudoCall, moduleQN, importMap, typeMap, lspCallerMethods)
	if !ok {
		t.Fatalf("expected resolveCallEdge to emit on same-module callee")
	}
	if edge.Type != "CALLS_PSEUDO" {
		t.Fatalf("expected CALLS_PSEUDO, got %q", edge.Type)
	}
	if got := edge.Properties["resolver_rule"]; got != ResolverRuleModalPseudo {
		t.Errorf("CALLS_PSEUDO must carry modal-pseudo resolver_rule, got %v", got)
	}
}

// TestResolveCallEdge_SamePackageFreeFunction — same-module strategy
// from the registry produces same-package-shadow rule when the
// caller is a real function.
func TestResolveCallEdge_SamePackageFreeFunction(t *testing.T) {
	p := &Pipeline{
		ProjectName: "proj",
		registry:    NewFunctionRegistry(),
	}
	p.registry.Register("Target", "proj.main.Target", "Function")
	p.registry.Register("Caller", "proj.main.Caller", "Function")

	moduleQN := "proj.main"
	importMap := map[string]string{}
	typeMap := TypeMap{}
	lspCallerMethods := map[string]bool{}

	call := cbm.Call{CalleeName: "Target", EnclosingFuncQN: "proj.main.Caller"}
	edge, ok := p.resolveCallEdge(call, moduleQN, importMap, typeMap, lspCallerMethods)
	if !ok {
		t.Fatalf("expected resolveCallEdge to emit on same-module callee")
	}
	if edge.Type != "CALLS" {
		t.Fatalf("expected CALLS, got %q", edge.Type)
	}
	if got := edge.Properties["resolver_rule"]; got != ResolverRuleSamePackageShadow {
		t.Errorf("same-module resolution must be same-package-shadow, got %v", got)
	}
}

// TestResolveCallEdge_CrossPackageImportMap — import_map strategy
// (callee qualified by imported package alias) routes to
// cross-package-heuristic.
func TestResolveCallEdge_CrossPackageImportMap(t *testing.T) {
	p := &Pipeline{
		ProjectName: "proj",
		registry:    NewFunctionRegistry(),
	}
	p.registry.Register("Target", "proj.bar.Target", "Function")
	p.registry.Register("Caller", "proj.foo.Caller", "Function")

	moduleQN := "proj.foo"
	importMap := map[string]string{
		"bar": "proj.bar",
	}
	typeMap := TypeMap{}
	lspCallerMethods := map[string]bool{}

	// Call: bar.Target() inside Caller. bar is in importMap → resolves
	// to proj.bar.Target via import_map strategy.
	call := cbm.Call{CalleeName: "bar.Target", EnclosingFuncQN: "proj.foo.Caller"}
	edge, ok := p.resolveCallEdge(call, moduleQN, importMap, typeMap, lspCallerMethods)
	if !ok {
		t.Fatalf("expected resolveCallEdge to emit on import_map resolution")
	}
	if got := edge.Properties["resolver_rule"]; got != ResolverRuleCrossPackageHeuristic {
		t.Errorf("import_map resolution must be cross-package-heuristic, got %v", got)
	}
}

// TestResolveCallEdge_TypeDispatchInterfaceDispatch — TypeMap-driven
// resolveCallWithTypes "type_dispatch" strategy maps to
// interface-dispatch.
func TestResolveCallEdge_TypeDispatchInterfaceDispatch(t *testing.T) {
	p := &Pipeline{
		ProjectName: "proj",
		registry:    NewFunctionRegistry(),
	}
	p.registry.Register("DoThing", "proj.foo.MyStruct.DoThing", "Method")
	p.registry.Register("Caller", "proj.foo.Caller", "Function")

	moduleQN := "proj.foo"
	importMap := map[string]string{}
	typeMap := TypeMap{
		"obj": "proj.foo.MyStruct",
	}
	lspCallerMethods := map[string]bool{}

	// Call: obj.DoThing() — TypeMap says obj has type MyStruct;
	// MyStruct.DoThing exists in registry → type_dispatch strategy.
	call := cbm.Call{CalleeName: "obj.DoThing", EnclosingFuncQN: "proj.foo.Caller"}
	edge, ok := p.resolveCallEdge(call, moduleQN, importMap, typeMap, lspCallerMethods)
	if !ok {
		t.Fatalf("expected resolveCallEdge to emit on type_dispatch")
	}
	if got := edge.Properties["resolver_rule"]; got != ResolverRuleInterfaceDispatch {
		t.Errorf("type_dispatch must be interface-dispatch, got %v", got)
	}
}

// TestResolveCallEdge_SelfMethod — Python-style self.method() with a
// matching enclosing class produces self-method rule.
func TestResolveCallEdge_SelfMethod(t *testing.T) {
	p := &Pipeline{
		ProjectName: "proj",
		registry:    NewFunctionRegistry(),
	}
	p.registry.Register("MyClass", "app.module.MyClass", "Class")
	p.registry.Register("helper", "app.module.MyClass.helper", "Method")
	p.registry.Register("entry", "app.module.MyClass.entry", "Method")

	moduleQN := "app.module"
	importMap := map[string]string{}
	typeMap := TypeMap{}
	lspCallerMethods := map[string]bool{}

	call := cbm.Call{CalleeName: "self.helper", EnclosingFuncQN: "app.module.MyClass.entry"}
	edge, ok := p.resolveCallEdge(call, moduleQN, importMap, typeMap, lspCallerMethods)
	if !ok {
		t.Fatalf("expected resolveCallEdge to emit on self.helper")
	}
	if got := edge.Properties["resolver_rule"]; got != ResolverRuleSelfMethod {
		t.Errorf("self.method must be self-method, got %v", got)
	}
}

// TestCollectLSPResolvedEdges_AssignsInterfaceDispatch — LSP path
// emits edges with resolver_rule sourced from the strategy.
// Interface-dispatch case must produce interface-dispatch rule.
func TestCollectLSPResolvedEdges_AssignsInterfaceDispatch(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Caller", "proj.foo.Caller", "Function")
	reg.Register("Run", "proj.bar.Run", "Method")

	rcs := []cbm.ResolvedCall{
		{
			CallerQN:   "proj.foo.Caller",
			CalleeQN:   "proj.bar.Run",
			Strategy:   "lsp_interface_dispatch",
			Confidence: 0.9,
		},
	}
	edges, _ := collectLSPResolvedEdges(rcs, reg)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if got := edges[0].Properties["resolver_rule"]; got != ResolverRuleInterfaceDispatch {
		t.Errorf("LSP interface-dispatch must produce interface-dispatch rule, got %v", got)
	}
}

// TestCollectLSPResolvedEdges_AssignsReceiverQualified — LSP type-dispatch
// strategy produces receiver-qualified rule.
func TestCollectLSPResolvedEdges_AssignsReceiverQualified(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Caller", "proj.foo.Caller", "Function")
	reg.Register("Method", "proj.bar.Type.Method", "Method")

	rcs := []cbm.ResolvedCall{
		{
			CallerQN:   "proj.foo.Caller",
			CalleeQN:   "proj.bar.Type.Method",
			Strategy:   "lsp_type_dispatch",
			Confidence: 0.9,
		},
	}
	edges, _ := collectLSPResolvedEdges(rcs, reg)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if got := edges[0].Properties["resolver_rule"]; got != ResolverRuleReceiverQualified {
		t.Errorf("LSP type-dispatch must produce receiver-qualified rule, got %v", got)
	}
}

// TestCollectLSPResolvedEdges_DefaultExactQN — non-dispatch LSP
// strategy strings produce exact-qn-match.
func TestCollectLSPResolvedEdges_DefaultExactQN(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("Caller", "proj.foo.Caller", "Function")
	reg.Register("Helper", "proj.foo.Helper", "Function")

	rcs := []cbm.ResolvedCall{
		{
			CallerQN:   "proj.foo.Caller",
			CalleeQN:   "proj.foo.Helper",
			Strategy:   "lsp_resolved",
			Confidence: 0.9,
		},
	}
	edges, _ := collectLSPResolvedEdges(rcs, reg)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if got := edges[0].Properties["resolver_rule"]; got != ResolverRuleExactQN {
		t.Errorf("default LSP strategy must produce exact-qn-match, got %v", got)
	}
}

// TestBuildEdgesFromResults_ModalExternalUpgrade — buildEdgesFromResults
// upgrades CALLS edges with stub targets to CALLS_EXTERNAL and
// overrides the resolver_rule property to modal-external. Verifies the
// modal-external override fires correctly and overrides the original
// rule (here cross-package-heuristic).
func TestBuildEdgesFromResults_ModalExternalUpgrade(t *testing.T) {
	results := [][]resolvedEdge{
		{
			{
				CallerQN: "proj.foo.Caller",
				TargetQN: "external.stub.Target",
				Type:     "CALLS",
				Properties: map[string]any{
					"resolver_rule":    ResolverRuleCrossPackageHeuristic,
					"caller_node_kind": CallerKindFunction,
				},
			},
		},
	}
	qnToID := map[string]int64{
		"proj.foo.Caller":      1,
		"external.stub.Target": 2,
	}
	labels := map[string]string{
		"proj.foo.Caller":      "Function",
		"external.stub.Target": "Function",
	}
	stubQNs := map[string]bool{
		"external.stub.Target": true,
	}

	edges := buildEdgesFromResults(results, qnToID, labels, "proj", 1, stubQNs)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].Type != "CALLS_EXTERNAL" {
		t.Errorf("expected CALLS_EXTERNAL on stub target, got %q", edges[0].Type)
	}
	if got := edges[0].Properties["resolver_rule"]; got != ResolverRuleModalExternal {
		t.Errorf("CALLS_EXTERNAL must carry modal-external resolver_rule, got %v", got)
	}
}
