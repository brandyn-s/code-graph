package pipeline

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/cbm"
	"github.com/brandyn-s/code-graph/internal/lang"
)

// CG-2 (2026-05-06) — internal-class-membership gate on the chain
// walker's type_dispatch emission.
//
// Background: 2026-05-06 PSM baseline shows interface-dispatch precision
// 0.81 (22 FPs in 118 emissions, 38% of all Rust FPs). Per-edge incident
// data was not available to attribute exact share, but a structural gap
// was identified: the chain walker at `pipeline.go::resolveCallWithTypes`
// emitted type_dispatch when `currentType.method` existed in the
// registry, WITHOUT verifying `currentType` itself was a registered
// Class/Struct/Enum/Trait/Interface. This bypassed the
// `applyReceiverTypeFilter` safety net used by `resolveViaNameLookup`.
//
// The gate added by CG-2: chain-walker emits type_dispatch only when
// `currentType` is registered as a class-like type. Cases where
// `currentType` is a Module name (or other non-class entity that
// happens to share a QN prefix with a method) now fall through to the
// regular resolver pipeline, which can apply Tier 2/3 discrimination.
//
// These tests pin the gate at the registry level. Full empirical
// verification of the precision lift requires per-fixture re-baselining
// (deferred to a measurement session).

// TestIsClassLike_AcceptedLabels — registry recognizes Class, Struct,
// Enum, Trait, Interface as class-like. Methods, Functions, Modules
// are NOT class-like.
func TestIsClassLike_AcceptedLabels(t *testing.T) {
	r := NewFunctionRegistry()
	cases := map[string]struct {
		label string
		want  bool
	}{
		"qn.Class":     {"Class", true},
		"qn.Struct":    {"Struct", true},
		"qn.Enum":      {"Enum", true},
		"qn.Trait":     {"Trait", true},
		"qn.Interface": {"Interface", true},
		"qn.Method":    {"Method", false},
		"qn.Function":  {"Function", false},
		"qn.Module":    {"Module", false},
		"qn.Field":     {"Field", false},
	}
	for qn, c := range cases {
		r.Register(qn, qn, c.label)
		got := r.IsClassLike(qn)
		if got != c.want {
			t.Errorf("IsClassLike(%q) [label=%s]: got %v, want %v",
				qn, c.label, got, c.want)
		}
	}
	// Unregistered QN returns false.
	if r.IsClassLike("qn.Unregistered") {
		t.Errorf("IsClassLike(unregistered) must be false")
	}
}

// TestChainWalker_GateAcceptsRegisteredClass — type_dispatch fires
// when currentType is a registered class-like type. Pins the
// expected post-gate behavior on the canonical case.
func TestChainWalker_GateAcceptsRegisteredClass(t *testing.T) {
	p := &Pipeline{
		ProjectName: "proj",
		registry:    NewFunctionRegistry(),
	}
	// MyStruct registered as Struct — the gate accepts it.
	p.registry.Register("MyStruct", "proj.foo.MyStruct", "Struct")
	p.registry.Register("DoThing", "proj.foo.MyStruct.DoThing", "Method")
	p.registry.Register("Caller", "proj.foo.Caller", "Function")

	moduleQN := "proj.foo"
	typeMap := TypeMap{"obj": "proj.foo.MyStruct"}
	call := cbm.Call{CalleeName: "obj.DoThing", EnclosingFuncQN: "proj.foo.Caller"}
	edge, ok := p.resolveCallEdge(call, moduleQN, nil, typeMap, nil, lang.Language(""))
	if !ok {
		t.Fatalf("expected emit on registered-class type_dispatch")
	}
	if got := edge.Properties["resolver_rule"]; got != ResolverRuleInterfaceDispatch {
		t.Errorf("expected interface-dispatch rule, got %v", got)
	}
}

// TestChainWalker_GateRejectsNonClassReceiver — when currentType
// resolves to a registered NON-class entity (e.g. a Module that
// happens to have a child with the matching method name), the chain
// walker no longer short-circuits with type_dispatch. The call
// falls through to the regular resolver pipeline, which can pick
// up alternate candidates or drop entirely.
//
// In production this catches edge cases where the chain walker's
// type inference resolved to something like a Module name that
// isn't a real receiver type — emitting type_dispatch in those
// cases bypasses the Tier 2 receiver-type discriminator that
// resolveViaNameLookup uses.
func TestChainWalker_GateRejectsNonClassReceiver(t *testing.T) {
	p := &Pipeline{
		ProjectName: "proj",
		registry:    NewFunctionRegistry(),
	}
	// Register `proj.foo.utils` as a Module (NOT class-like). Also
	// register `proj.foo.utils.compute` as a free Function — so
	// `utils.compute` exists as a QN but utils itself is a Module.
	p.registry.Register("utils", "proj.foo.utils", "Module")
	p.registry.Register("compute", "proj.foo.utils.compute", "Function")
	p.registry.Register("Caller", "proj.foo.Caller", "Function")

	moduleQN := "proj.foo"
	// TypeMap binds `x` to the Module's QN — pre-CG-2, the chain
	// walker would emit type_dispatch on x.compute even though
	// `utils` is not a real receiver type. Post-CG-2, the gate
	// rejects this.
	typeMap := TypeMap{"x": "proj.foo.utils"}
	call := cbm.Call{CalleeName: "x.compute", EnclosingFuncQN: "proj.foo.Caller"}
	edge, ok := p.resolveCallEdge(call, moduleQN, nil, typeMap, nil, lang.Language(""))

	// Whether emission happens at all depends on downstream resolver
	// strategies (unique_name fallback can still fire). What we pin
	// here: if it DOES emit, it does NOT carry the interface-dispatch
	// rule (because the chain walker's type_dispatch path was gated).
	if ok {
		if got := edge.Properties["resolver_rule"]; got == ResolverRuleInterfaceDispatch {
			t.Errorf("chain-walker bypass detected: emitted interface-dispatch "+
				"on non-class receiver type (utils=Module). "+
				"Got rule=%v target=%v", got, edge.TargetQN)
		}
	}
}

// TestChainWalker_GateRejectsUnregisteredReceiver — when currentType
// resolves to a QN not in the registry at all, the chain walker
// should NOT emit type_dispatch. Defensive case: TypeMap pointing
// to an external/uninternal type.
func TestChainWalker_GateRejectsUnregisteredReceiver(t *testing.T) {
	p := &Pipeline{
		ProjectName: "proj",
		registry:    NewFunctionRegistry(),
	}
	// Register `Vec.new` as a Method to simulate the case where
	// `currentType.method` exists but `currentType` (Vec) doesn't —
	// e.g. someone registered the method but not the parent type.
	// In real Rust, Vec wouldn't be in our registry at all; this
	// test verifies the gate handles partial registrations safely.
	p.registry.Register("new", "Vec.new", "Method")
	p.registry.Register("Caller", "proj.foo.Caller", "Function")

	moduleQN := "proj.foo"
	typeMap := TypeMap{"v": "Vec"}
	call := cbm.Call{CalleeName: "v.new", EnclosingFuncQN: "proj.foo.Caller"}
	edge, ok := p.resolveCallEdge(call, moduleQN, nil, typeMap, nil, lang.Language(""))

	// Same pattern: regardless of downstream resolution, the chain-
	// walker bypass must not fire.
	if ok {
		if got := edge.Properties["resolver_rule"]; got == ResolverRuleInterfaceDispatch {
			t.Errorf("chain-walker bypass: emitted interface-dispatch on "+
				"unregistered receiver type (Vec). Got rule=%v target=%v",
				got, edge.TargetQN)
		}
	}
}
