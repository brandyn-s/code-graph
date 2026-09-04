package lang

// Vendored from upstream codebase-memory-mcp's grammar manifest (0.9.1).
func init() {
	Register(&LanguageSpec{
		Language:          Svelte,
		FileExtensions:    []string{".svelte"},
		ModuleNodeTypes:   []string{"document"},
		FunctionNodeTypes: []string{},
		ClassNodeTypes:    []string{},
		FieldNodeTypes:    []string{},
		CallNodeTypes:     []string{},
		ImportNodeTypes:   []string{},
		VariableNodeTypes: []string{},
	})
}
