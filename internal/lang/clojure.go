package lang

// Vendored from upstream codebase-memory-mcp's grammar manifest (0.9.1).
func init() {
	Register(&LanguageSpec{
		Language:          Clojure,
		FileExtensions:    []string{".clj", ".cljs", ".cljc", ".edn"},
		ModuleNodeTypes:   []string{"source"},
		FunctionNodeTypes: []string{"list_lit"},
		ClassNodeTypes:    []string{},
		FieldNodeTypes:    []string{},
		CallNodeTypes:     []string{"list_lit"},
		ImportNodeTypes:   []string{},
		VariableNodeTypes: []string{},
	})
}
