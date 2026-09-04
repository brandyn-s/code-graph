package lang

// Vendored from upstream codebase-memory-mcp's grammar manifest (0.9.1).
func init() {
	Register(&LanguageSpec{
		Language:          GoMod,
		FileExtensions:    []string{"go.mod"},
		ModuleNodeTypes:   []string{"source_file"},
		FunctionNodeTypes: []string{},
		ClassNodeTypes:    []string{},
		FieldNodeTypes:    []string{},
		CallNodeTypes:     []string{},
		ImportNodeTypes:   []string{"require"},
		VariableNodeTypes: []string{"require_directive", "replace_directive"},
	})
}
