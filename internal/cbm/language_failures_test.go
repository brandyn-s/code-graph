package cbm

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// =====================================================================
// CONFIRMED RED: Makefile — rule not in function_node_types
// =====================================================================

func TestMakefileRuleAsFunction(t *testing.T) {
	source := []byte("all:\n\t@echo hello\n")
	result, err := ExtractFile(source, lang.Makefile, "test", "Makefile")
	if err != nil {
		t.Fatal(err)
	}
	fns := defsWithLabel(result, "Function")
	if len(fns) == 0 {
		t.Errorf("expected >=1 Function (target 'all'), got 0 (rule not in func_types)")
	}
	assertHasName(t, fns, "all")
}

func TestMakefileMultipleTargets(t *testing.T) {
	source := []byte("all: main.o\n\tgcc -o all main.o\n\nbuild:\n\tgo build ./...\n")
	result, err := ExtractFile(source, lang.Makefile, "test", "Makefile")
	if err != nil {
		t.Fatal(err)
	}
	fns := defsWithLabel(result, "Function")
	if len(fns) < 2 {
		t.Errorf("expected >=2 Functions (all, build), got %d: %v", len(fns), names(fns))
	}
	assertHasName(t, fns, "all")
	assertHasName(t, fns, "build")
}

func TestMakefileVariableExtraction(t *testing.T) {
	source := []byte("CC := gcc\nCFLAGS := -Wall\n")
	result, err := ExtractFile(source, lang.Makefile, "test", "Makefile")
	if err != nil {
		t.Fatal(err)
	}
	vars := defsWithLabel(result, "Variable")
	// variable_assignment is in VariableNodeTypes; this probe reveals if name extraction works
	if len(vars) == 0 {
		t.Logf("INFO: Makefile variable extraction returns 0 vars — name field may need a Makefile case in extract_var_names")
	} else {
		assertHasName(t, vars, "CC")
		assertHasName(t, vars, "CFLAGS")
	}
}
