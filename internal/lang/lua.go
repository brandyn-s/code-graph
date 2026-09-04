package lang

// Vendored from upstream codebase-memory-mcp's grammar manifest (0.9.1).
func init() {
	Register(&LanguageSpec{
		Language:          Lua,
		FileExtensions:    []string{".lua"},
		ModuleNodeTypes:   []string{"chunk"},
		FunctionNodeTypes: []string{"function_declaration", "function_definition"},
		ClassNodeTypes:    []string{},
		FieldNodeTypes:    []string{},
		CallNodeTypes:     []string{"function_call"},
		ImportNodeTypes:   []string{"function_call"},
		VariableNodeTypes: []string{"variable_declaration"},
	})
}
