package cbm

import (
	"fmt"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/lang"
)

// TestParamDebug dumps the raw extraction for Python to debug param_names.
func TestParamDebug(t *testing.T) {
	Init()
	defer Shutdown()

	code := `def authenticate(username: str, password: str) -> bool:
    pass

def greet(name):
    pass

def process(items: list, limit: int = 10) -> list:
    pass
`
	result, err := ExtractFile([]byte(code), lang.Python, "test", "test.py")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	for _, d := range result.Definitions {
		if d.Label == "Module" {
			continue
		}
		fmt.Printf("\n%s %s:\n", d.Label, d.Name)
		fmt.Printf("  Signature:  %q\n", d.Signature)
		fmt.Printf("  ParamNames: %v (len=%d)\n", d.ParamNames, len(d.ParamNames))
		fmt.Printf("  ParamTypes: %v (len=%d)\n", d.ParamTypes, len(d.ParamTypes))
		fmt.Printf("  ReturnType: %q\n", d.ReturnType)
	}

	// Also test JS
	jsCode := `function hello(name) { return name; }
function add(a, b) { return a + b; }
`
	result2, err := ExtractFile([]byte(jsCode), lang.JavaScript, "test", "test.js")
	if err != nil {
		t.Fatalf("JS extraction failed: %v", err)
	}
	fmt.Printf("\n\n=== JavaScript ===\n")
	for _, d := range result2.Definitions {
		if d.Label == "Module" {
			continue
		}
		fmt.Printf("\n%s %s:\n", d.Label, d.Name)
		fmt.Printf("  Signature:  %q\n", d.Signature)
		fmt.Printf("  ParamNames: %v (len=%d)\n", d.ParamNames, len(d.ParamNames))
	}
}
