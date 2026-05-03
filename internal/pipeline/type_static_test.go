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
