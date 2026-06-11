package lang

func init() {
	Register(&LanguageSpec{
		Language:          PowerShell,
		FileExtensions:    []string{".ps1", ".psm1", ".psd1"},
		FunctionNodeTypes: []string{"function_statement", "class_method_definition"},
		ClassNodeTypes:    []string{"class_statement"},
		ModuleNodeTypes:   []string{"program"},
		// Cmdlet/function invocations are `command` nodes (like bash);
		// .NET method calls are `invokation_expression` (upstream grammar's
		// spelling).
		CallNodeTypes:   []string{"command", "invokation_expression"},
		ImportNodeTypes: []string{"command"}, // Import-Module / using module

		BranchingNodeTypes:  []string{"if_statement", "while_statement", "for_statement", "foreach_statement", "switch_statement", "do_statement", "trap_statement"},
		VariableNodeTypes:   []string{"assignment_expression"},
		AssignmentNodeTypes: []string{"assignment_expression"},
	})
}
