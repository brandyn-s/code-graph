package lang

// Vendored from upstream codebase-memory-mcp's grammar manifest (0.9.1).
func init() {
	Register(&LanguageSpec{
		Language:          Erlang,
		FileExtensions:    []string{".erl", ".hrl"},
		ModuleNodeTypes:   []string{"source_file"},
		FunctionNodeTypes: []string{"function_clause"},
		ClassNodeTypes:    []string{"type_alias"},
		FieldNodeTypes:    []string{},
		CallNodeTypes:     []string{"call"},
		ImportNodeTypes:   []string{"module_attribute", "import", "include"},
		VariableNodeTypes: []string{"pp_define", "record_decl"},
	})
}
