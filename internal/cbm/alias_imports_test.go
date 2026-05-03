package cbm

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/lang"
)

// TestRustAliasImports verifies that `use path::Item as Alias` produces
// an import with local_name=Alias and module_path=path::Item (ACC-004).
func TestRustAliasImports(t *testing.T) {
	Init()
	defer Shutdown()

	code := `use std::io::Result as IoResult;
use foo::bar::Baz as MyBaz;
use foo::Bar;

fn main() {
    let _r: IoResult<()> = Ok(());
}
`
	result, err := ExtractFile([]byte(code), lang.Rust, "test", "test.rs")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	imports := map[string]string{}
	for _, imp := range result.Imports {
		imports[imp.LocalName] = imp.ModulePath
	}

	checks := []struct {
		local, target string
	}{
		{"IoResult", "std::io::Result"},
		{"MyBaz", "foo::bar::Baz"},
		{"Bar", "foo::Bar"}, // unaliased: local = path_last of full path
	}
	for _, c := range checks {
		got, ok := imports[c.local]
		if !ok {
			t.Errorf("missing import for local=%q (have: %v)", c.local, imports)
			continue
		}
		if got != c.target {
			t.Errorf("import %q: got module_path=%q, want %q", c.local, got, c.target)
		}
	}
}
