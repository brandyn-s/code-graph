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

// resolveAsClass checks if a name refers to a Class/Type node in the registry.
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

	// Only return if it's a class-like node
	switch label {
	case "Class", "Type", "Interface", "Enum":
		return result.QualifiedName
	}
	return ""
}
