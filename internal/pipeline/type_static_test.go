package pipeline

import "testing"

func TestTypeStaticDispatch_DieselShape(t *testing.T) {
	r := NewFunctionRegistry()
	r.Register("AssetRepo", "proj.src.repo.AssetRepo", "Struct")
	r.Register("new", "proj.src.repo.AssetRepo.new", "Method")

	ctx := CallContext{
		CalleeName:     "AssetRepo::new",
		CallerQN:       "proj.src.main.controls",
		ModuleQN:       "proj.src.main",
		ImportBindings: map[string]string{"AssetRepo": "rust_diesel_negative::repo::AssetRepo"},
	}
	result := r.ResolveCtx(ctx)
	if result.QualifiedName != "proj.src.repo.AssetRepo.new" {
		t.Fatalf("expected proj.src.repo.AssetRepo.new, got %q (strategy=%q)", result.QualifiedName, result.Strategy)
	}
	if result.Strategy != "type_static_dispatch" {
		t.Errorf("expected strategy=type_static_dispatch, got %q", result.Strategy)
	}
}

func TestTypeStaticDispatch_ExternalDropped(t *testing.T) {
	// Vec is std — no internal class. Strategy must drop, not phantom-match.
	r := NewFunctionRegistry()
	r.Register("Foo", "proj.src.foo.Foo", "Struct")
	r.Register("new", "proj.src.foo.Foo.new", "Method")  // internal Foo.new

	ctx := CallContext{
		CalleeName:     "Vec::new",
		CallerQN:       "proj.src.main.run",
		ModuleQN:       "proj.src.main",
		ImportBindings: map[string]string{},
	}
	result := r.ResolveCtx(ctx)
	if result.QualifiedName != "" {
		t.Fatalf("expected empty (Vec is external), got %q", result.QualifiedName)
	}
}

func TestTypeStaticDispatch_NotImported(t *testing.T) {
	// Internal Foo class exists in another file but not imported by caller.
	// Without import OR same-module, should drop.
	r := NewFunctionRegistry()
	r.Register("Foo", "proj.src.foo.Foo", "Struct")
	r.Register("new", "proj.src.foo.Foo.new", "Method")

	ctx := CallContext{
		CalleeName:     "Foo::new",
		CallerQN:       "proj.src.main.run",
		ModuleQN:       "proj.src.main",
		ImportBindings: map[string]string{}, // no import
	}
	result := r.ResolveCtx(ctx)
	if result.QualifiedName != "" {
		t.Fatalf("expected empty (Foo not imported), got %q", result.QualifiedName)
	}
}

// ACC-003: module-dispatch — `diagnostics::router(...)` from a sibling
// module in the same package, no `use` declaration. The Module label
// gets a wider reachability gate (sibling-module).
func TestTypeStaticDispatch_ModuleSiblingDispatch(t *testing.T) {
	r := NewFunctionRegistry()
	// Module node for src/v2/telem/health/diagnostics.rs
	r.Register("diagnostics", "proj.src.v2.telem.health.diagnostics", "Module")
	r.Register("router", "proj.src.v2.telem.health.diagnostics.router", "Function")

	// Caller in src/v2/telem/health/mod.rs — sibling module (shared parent).
	ctx := CallContext{
		CalleeName:     "diagnostics::router",
		CallerQN:       "proj.src.v2.telem.health.mod.router",
		ModuleQN:       "proj.src.v2.telem.health.mod",
		ImportBindings: map[string]string{}, // no `use diagnostics;` needed
	}
	result := r.ResolveCtx(ctx)
	if result.QualifiedName != "proj.src.v2.telem.health.diagnostics.router" {
		t.Fatalf("expected proj.src.v2.telem.health.diagnostics.router, got %q", result.QualifiedName)
	}
	if result.Strategy != "type_static_dispatch" {
		t.Errorf("expected strategy=type_static_dispatch, got %q", result.Strategy)
	}
}

func TestTypeStaticDispatch_ModuleExternalDropped(t *testing.T) {
	// `tracing::info(...)` — no internal `tracing` module. Drop.
	r := NewFunctionRegistry()
	r.Register("info", "proj.src.helpers.info", "Function")

	ctx := CallContext{
		CalleeName:     "tracing::info",
		CallerQN:       "proj.src.main.run",
		ModuleQN:       "proj.src.main",
		ImportBindings: map[string]string{},
	}
	result := r.ResolveCtx(ctx)
	if result.QualifiedName != "" {
		t.Fatalf("expected empty (tracing is external module), got %q", result.QualifiedName)
	}
}

// ACC-008: Tier 2 must accept candidates that match the chain-resolved
// receiver type, not just the root receiver. The chain walker in
// resolveCallWithTypes computes the final-segment receiver type and
// passes it as ctx.ReceiverType. This test verifies Tier 2 accepts a
// Method whose parent equals that chain-resolved type.
func TestApplyReceiverTypeFilter_ChainResolvedReceiver(t *testing.T) {
	r := NewFunctionRegistry()
	r.Register("UpdaterClient", "proj.src.updater.UpdaterClient", "Struct")
	r.Register("enqueue_asset_update", "proj.src.updater.UpdaterClient.enqueue_asset_update", "Method")

	// resolveCallWithTypes sets ReceiverType to the CHAIN-RESOLVED type
	// when chain analysis lands on a known intermediate type. Tier 2 then
	// matches candidates whose parent equals that chain target.
	candidates := []string{"proj.src.updater.UpdaterClient.enqueue_asset_update"}
	ctx := CallContext{
		CalleeName:   "data.updater_client.enqueue_asset_update",
		ReceiverType: "proj.src.updater.UpdaterClient", // chain-resolved, not root
	}
	filtered, _, dropAll := r.applyReceiverTypeFilter(ctx, candidates)
	if dropAll {
		t.Fatal("expected accept (receiver matches chain-resolved type), got dropAll=true")
	}
	if len(filtered) != 1 || filtered[0] != "proj.src.updater.UpdaterClient.enqueue_asset_update" {
		t.Fatalf("expected matching candidate accepted, got %v", filtered)
	}
}

// applyReceiverTypeFilter must accept candidates whose parent is a Trait
// that the receiver Struct implements. Without this, Tier 2 drops
// legitimate Trait-method calls when the method is defined on the Trait.
func TestApplyReceiverTypeFilter_TraitImpl(t *testing.T) {
	r := NewFunctionRegistry()
	// Struct + impl of trait Display
	r.Register("Foo", "proj.src.foo.Foo", "Struct")
	r.Register("Display", "proj.src.display.Display", "Trait")
	// fmt is a Method on the Trait, NOT directly on Foo
	r.Register("fmt", "proj.src.display.Display.fmt", "Method")
	// Register the impl relationship
	r.RegisterTraitImpl("proj.src.foo.Foo", "proj.src.display.Display")

	candidates := []string{"proj.src.display.Display.fmt"}
	ctx := CallContext{
		CalleeName:   "obj.fmt",
		ReceiverType: "proj.src.foo.Foo",
	}
	filtered, applied, dropAll := r.applyReceiverTypeFilter(ctx, candidates)
	if dropAll {
		t.Fatal("expected candidates accepted (Trait impl), got dropAll=true")
	}
	if applied != "receiver-type-match" && applied != "" {
		// Either applied is "receiver-type-match" (filter narrowed) or "" (passthrough)
		// Both are acceptable; the candidates list must include the Trait method.
	}
	found := false
	for _, qn := range filtered {
		if qn == "proj.src.display.Display.fmt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Display.fmt accepted via Trait impl, got %v (applied=%q)", filtered, applied)
	}
}

// ACC-006: crate-prefixed scoped paths (`crate_name::module::fn`) should
// resolve through the existing module-dispatch path after the crate prefix
// is stripped. The strip happens in pipeline.go:resolveCallWithTypes, but
// we can test the post-strip behavior by simulating the rewritten input.
func TestTypeStaticDispatch_PostCratePrefixStrip(t *testing.T) {
	r := NewFunctionRegistry()
	r.Register("state", "proj.src.state", "Module")
	r.Register("ready", "proj.src.state.ready", "Function")

	// After ACC-006 strip in resolveCallWithTypes, a callee like
	// `myproj::state::ready` becomes `state::ready` and reaches ResolveCtx.
	ctx := CallContext{
		CalleeName:     "state::ready",
		CallerQN:       "proj.src.main.controls",
		ModuleQN:       "proj.src.main",
		ImportBindings: map[string]string{},
	}
	result := r.ResolveCtx(ctx)
	if result.QualifiedName != "proj.src.state.ready" {
		t.Fatalf("expected proj.src.state.ready, got %q (strategy=%q)", result.QualifiedName, result.Strategy)
	}
}

func TestTypeStaticDispatch_ModuleSiblingClassDistinction(t *testing.T) {
	// Sibling-module reachability fires ONLY for Module label, not Class.
	// A Struct in a sibling module without `use` should still drop —
	// otherwise common type names like Error / Result would phantom-match.
	r := NewFunctionRegistry()
	r.Register("Result", "proj.src.foo.Result", "Struct") // sibling Struct
	r.Register("new", "proj.src.foo.Result.new", "Method")

	ctx := CallContext{
		CalleeName:     "Result::new",
		CallerQN:       "proj.src.bar.run", // bar is sibling of foo
		ModuleQN:       "proj.src.bar",
		ImportBindings: map[string]string{},
	}
	result := r.ResolveCtx(ctx)
	if result.QualifiedName != "" {
		t.Fatalf("expected empty (Class needs import even from sibling module), got %q", result.QualifiedName)
	}
}
