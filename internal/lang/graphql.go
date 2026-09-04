package lang

// Vendored from upstream codebase-memory-mcp's grammar manifest (0.9.1).
func init() {
	Register(&LanguageSpec{
		Language:          GraphQL,
		FileExtensions:    []string{".graphql", ".gql", ".graphqls"},
		ModuleNodeTypes:   []string{"document"},
		FunctionNodeTypes: []string{},
		ClassNodeTypes:    []string{"object_type_definition", "input_object_type_definition", "enum_type_definition", "interface_type_definition", "union_type_definition", "scalar_type_definition", "type_definition"},
		FieldNodeTypes:    []string{"field_definition", "input_value_definition"},
		CallNodeTypes:     []string{},
		ImportNodeTypes:   []string{},
		VariableNodeTypes: []string{},
	})
}
