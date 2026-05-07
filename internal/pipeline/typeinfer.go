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
	result := registry.Resolve(name, moduleQN, importMap)
	if result.QualifiedName == "" {
		return ""
	}

	registry.mu.RLock()
	defer registry.mu.RUnlock()

	label, exists := registry.exact[result.QualifiedName]
	if !exists {
		return ""
	}

	// Only return if it's a class-like node.
	switch label {
	case "Class", "Type", "Interface", "Enum", "Struct", "Trait":
		return result.QualifiedName
	}
	return ""
}
