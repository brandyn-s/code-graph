package pipeline

import (
	"fmt"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
	"github.com/DeusData/codebase-memory-mcp/internal/lang"
)

func TestPropertyBaseline(t *testing.T) {
	cbm.Init()
	defer cbm.Shutdown()

	samples := []struct {
		name     string
		filename string
		lang     lang.Language
		code     string
	}{
		{"Go", "main.go", lang.Go, `package main

import "fmt"

func authenticate(username string, password string) (bool, error) {
	if username == "" {
		return false, fmt.Errorf("empty")
	}
	return true, nil
}

func processData(items []string, limit int) []string {
	result := make([]string, 0, limit)
	for i, item := range items {
		if i >= limit {
			break
		}
		result = append(result, item)
	}
	return result
}
`},
		{"Python", "main.py", lang.Python, `def authenticate(username: str, password: str) -> bool:
    if not username:
        return False
    return check_credentials(username, password)

def process_data(items: list, limit: int = 10) -> list:
    result = []
    for i, item in enumerate(items):
        if i >= limit:
            break
        result.append(item)
    return result
`},
		{"JavaScript", "main.js", lang.JavaScript, `function authenticate(username, password) {
    if (!username) {
        return false;
    }
    return checkCredentials(username, password);
}

function processData(items, limit = 10) {
    const result = [];
    for (let i = 0; i < items.length && i < limit; i++) {
        result.push(items[i]);
    }
    return result;
}
`},
		{"TypeScript", "main.ts", lang.TypeScript, `function authenticate(username: string, password: string): boolean {
    if (!username) {
        return false;
    }
    return checkCredentials(username, password);
}

function processData(items: string[], limit: number = 10): string[] {
    const result: string[] = [];
    for (let i = 0; i < items.length && i < limit; i++) {
        result.push(items[i]);
    }
    return result;
}
`},
		{"Rust", "main.rs", lang.Rust, `fn authenticate(username: &str, password: &str) -> bool {
    if username.is_empty() {
        return false;
    }
    check_credentials(username, password)
}

fn process_data(items: Vec<String>, limit: usize) -> Vec<String> {
    let mut result = Vec::new();
    for (i, item) in items.iter().enumerate() {
        if i >= limit {
            break;
        }
        result.push(item.clone());
    }
    result
}
`},
	}

	for _, s := range samples {
		t.Run(s.name, func(t *testing.T) {
			result, err := cbm.ExtractFile([]byte(s.code), s.lang, "test", s.filename)
			if err != nil {
				t.Fatalf("extraction failed: %v", err)
			}

			fmt.Printf("\n=== %s (%d definitions) ===\n", s.name, len(result.Definitions))
			for _, d := range result.Definitions {
				fmt.Printf("  %s %s:\n", d.Label, d.Name)
				fmt.Printf("    Signature:    %q\n", d.Signature)
				fmt.Printf("    ParamNames:   %v\n", d.ParamNames)
				fmt.Printf("    ParamTypes:   %v\n", d.ParamTypes)
				fmt.Printf("    ReturnType:   %q\n", d.ReturnType)
				fmt.Printf("    ReturnTypes:  %v\n", d.ReturnTypes)
				fmt.Printf("    Complexity:   %d\n", d.Complexity)
				fmt.Printf("    Lines:        %d\n", d.Lines)
			}
		})
	}
}
