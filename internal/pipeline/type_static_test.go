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
