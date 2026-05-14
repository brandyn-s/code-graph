package cbm

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/lang"
)

// PSM's defvar! macro declares env-var-backed typed constants. Before the
// extractor change, these were invisible to code-graph (only the
// macro_invocation node was emitted as a CALLS edge, never a Definition).
// The 2026-05-13 PSM tool-comparison battery surfaced this gap; the
// extractor now emits a Variable definition per defvar! site.
//
// Five test cases pin the contract:
//   1. simplest defvar! emits one Variable
//   2. multiple defvars in the same source emit one each, preserving names
//   3. a non-defvar! macro_invocation does NOT emit a Variable
//   4. decorators=["defvar"] is set so callers can filter via CONTAINS
//   5. start_line points at the macro_invocation line (not the line where
//      the name token sits within the token_tree)
func TestDefvarMacro_SimplestEmitsOneVariable(t *testing.T) {
	source := []byte(`
defvar!(MY_FLAG: bool = false, or try t => t.parse(); );
`)
	result, err := ExtractFile(source, lang.Rust, "test", "src/main.rs")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	vars := varsByName(result.Definitions)
	if _, ok := vars["MY_FLAG"]; !ok {
		t.Fatalf("expected Variable 'MY_FLAG' in definitions, got %v", definitionNames(result.Definitions))
	}
}

func TestDefvarMacro_MultipleSites(t *testing.T) {
	source := []byte(`
defvar!(API_BASE: String = "https://x".to_string(), or try t => t.parse(); );
defvar!(MAX_RANGE: f64 = 15000.0, or try t => t.parse(); );
defvar!(TIMEOUT_SECS: u64 = 10, or try t => t.parse(); );
`)
	result, err := ExtractFile(source, lang.Rust, "test", "src/main.rs")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	vars := varsByName(result.Definitions)
	for _, want := range []string{"API_BASE", "MAX_RANGE", "TIMEOUT_SECS"} {
		if _, ok := vars[want]; !ok {
			t.Errorf("expected Variable %q, got %v", want, definitionNames(result.Definitions))
		}
	}
}

func TestDefvarMacro_OtherMacroNotExtracted(t *testing.T) {
	// `println!` is an unambiguous non-defvar macro_invocation. It must
	// NOT emit a Variable definition.
	source := []byte(`
fn f() {
    println!("hello {}", "world");
}
`)
	result, err := ExtractFile(source, lang.Rust, "test", "src/main.rs")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	for _, d := range result.Definitions {
		if d.Label == "Variable" {
			t.Errorf("unexpected Variable from non-defvar macro: %q", d.Name)
		}
	}
}

func TestDefvarMacro_DecoratorTagged(t *testing.T) {
	source := []byte(`defvar!(TAG_ME: u32 = 0, or try t => t.parse(); );
`)
	result, err := ExtractFile(source, lang.Rust, "test", "src/main.rs")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	for _, d := range result.Definitions {
		if d.Name == "TAG_ME" && d.Label == "Variable" {
			found := false
			for _, dec := range d.Decorators {
				if dec == "defvar" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected decorators=[\"defvar\"], got %v", d.Decorators)
			}
			return
		}
	}
	t.Errorf("TAG_ME Variable not found in %v", definitionNames(result.Definitions))
}

func TestDefvarMacro_StartLinePointsAtMacro(t *testing.T) {
	// First non-newline content is on line 2 (a blank line precedes).
	source := []byte(`
defvar!(LINE_TWO: u32 = 0, or try t => t.parse(); );
`)
	result, err := ExtractFile(source, lang.Rust, "test", "src/main.rs")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	for _, d := range result.Definitions {
		if d.Name == "LINE_TWO" {
			if d.StartLine != 2 {
				t.Errorf("expected StartLine=2 for LINE_TWO, got %d", d.StartLine)
			}
			return
		}
	}
	t.Errorf("LINE_TWO not found in %v", definitionNames(result.Definitions))
}

// --- helpers ---

func varsByName(defs []Definition) map[string]Definition {
	m := make(map[string]Definition, len(defs))
	for _, d := range defs {
		if d.Label == "Variable" {
			m[d.Name] = d
		}
	}
	return m
}

func definitionNames(defs []Definition) []string {
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name+":"+d.Label)
	}
	return names
}
