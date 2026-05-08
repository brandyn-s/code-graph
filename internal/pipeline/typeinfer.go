package pipeline

// TypeMap maps variable names to their resolved class QN.
type TypeMap map[string]string

// PerFuncTypeMap maps a function/method QN to the local-variable
// TypeMap visible inside its body (including parameters, `self`, and
// `let` bindings). Keys are caller QNs; values are per-scope name->type
// lookups. Empty key "" is the module-scope TypeMap (used for free-fn
// callers and CALLS_PSEUDO sites that have no enclosing function).
type PerFuncTypeMap map[string]TypeMap

// FieldTypeMap maps "<structQN>.<fieldName>" -> field-type-class QN.
// Populated by walking struct/enum/union field declarations across
// every extracted file in the project. Used by the resolver to walk
// chains like `obj.field.method()` — once obj's type is known via
// the per-function TypeMap, FieldTypeMap supplies field's type so the
// resolver can look up `.method` on the field's type.
type FieldTypeMap map[string]string

// ReturnTypeMap maps function QN to the return type name.
type ReturnTypeMap map[string]string

// ResolveAsClassReason names the specific failure mode when
// resolveAsClassWithReason returns an empty QN. Used by Phase D
// instrumentation (2026-05-08) to split the previously-aggregate
// `traitQN-empty` skip reason in implements.go into its three
// downstream causes — without changing any caller behavior.
type ResolveAsClassReason string

const (
	// ResolveOK — the name resolved to a class-like QN. No failure.
	ResolveOK ResolveAsClassReason = ""
	// ResolveEmpty — registry.Resolve returned no QN at all. The
	// trait/struct name isn't reachable via any of the 9 resolver
	// strategies (import_map, same_module, type_dispatch, ...).
	// On PSM, this is the dominant failure mode for
	// `impl From<X> for Y` because `From` comes through Rust's
	// implicit prelude (no `use` statement) and the prelude isn't
	// in the imports map.
	ResolveEmpty ResolveAsClassReason = "resolve-empty"
	// ResolveLabelMissing — registry.Resolve returned a QN but
	// registry.exact has no label entry for that QN. Should be rare;
	// indicates a registry-population gap.
	ResolveLabelMissing ResolveAsClassReason = "label-missing"
	// ResolveLabelMismatch — registry.Resolve returned a QN with a
	// label that isn't class-like (e.g., Function, Module). Indicates
	// the resolver is binding the trait name to a non-trait symbol —
	// usually a same-named function or variable shadowing the trait.
	ResolveLabelMismatch ResolveAsClassReason = "label-mismatch"
)

// resolveAsClassWithReason mirrors resolveAsClass but additionally
// returns a structured reason when the QN is empty. Phase D-Instrument
// (2026-05-08) callers use this to split traitQN-empty into the three
// failure sub-buckets above.
//
// resolveAsClass remains as the legacy wrapper (forwards here, drops
// the reason) so existing callers don't change.
func resolveAsClassWithReason(name string, registry *FunctionRegistry, moduleQN string, importMap map[string]string) (string, ResolveAsClassReason) {
	result := registry.Resolve(name, moduleQN, importMap)
	if result.QualifiedName == "" {
		return "", ResolveEmpty
	}

	registry.mu.RLock()
	defer registry.mu.RUnlock()

	label, exists := registry.exact[result.QualifiedName]
	if !exists {
		return "", ResolveLabelMissing
	}

	switch label {
	case "Class", "Type", "Interface", "Enum", "Struct", "Trait":
		return result.QualifiedName, ResolveOK
	}
	return "", ResolveLabelMismatch
}

// resolveAsClass checks if a name refers to a class-like node in the
// registry (Class / Type / Interface / Enum / Struct / Trait).
//
// A2 (2026-05-07): Struct and Trait added to the accepted label set.
// The Rust extractor labels traits as "Trait" and structs as "Struct"
// (NOT "Class"); without these labels in the accepted set,
// `implementsRust` (in implements.go) and the IMPLEMENTS pass in
// pipeline.go silently dropped every Rust impl-block — both
// `resolveAsClass(traitName)` AND `resolveAsClass(structName)`
// returned empty, so the IMPLEMENTS edge was never emitted. PSM
// 2026-05-07 baseline: 365 IMPLEMENTS edges across 2,065 Rust files
// (CradlepointApiClientSync IMPLEMENTS CradlepointClientSync was
// missing) — explained entirely by this label-mismatch.
//
// Risk: anywhere `resolveAsClass` was used as a way to detect
// "is this name a CLASS specifically (not a Trait/Struct)" — but
// every existing call site uses `resolveAsClass` to find the QN of
// a type for IMPLEMENTS / EXTENDS / receiver-type binding. None of
// these care whether the type is technically a Class vs Struct vs
// Trait — they all behave identically.
func resolveAsClass(name string, registry *FunctionRegistry, moduleQN string, importMap map[string]string) string {
	qn, _ := resolveAsClassWithReason(name, registry, moduleQN, importMap)
	return qn
}
