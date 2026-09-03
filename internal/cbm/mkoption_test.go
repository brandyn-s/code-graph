package cbm

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// PSM's NixOS modules declare options via `name = mkOption { ... };`
// bindings. Before this extractor, these were invisible to code-graph
// (the Nix binding extractor's default path doesn't emit them as
// Definitions). With this pass, each mkOption-shaped binding emits an
// Option-labeled Definition with:
//   - name = leaf identifier of the binding's attrpath
//   - decorators = ["mkOption"]  (filterable via CONTAINS)
//   - start_line = the binding's source line
//
// PSM end-to-end validation (sibling worktree, 2026-05-14):
//   grep total lines of `= *mkOption` in nix/    = 717
//   `MATCH (o:Option) RETURN count(o)` after idx = 727
//   1.4% over-count — accounted for by multi-line `=\nmkOption` shape
//   that line-grep misses but the AST walker catches.

func TestMkOption_SimplestEmitsOneOption(t *testing.T) {
	source := []byte(`
{
  options = {
    enable = mkOption {
      type = types.bool;
      default = false;
    };
  };
}
`)
	result, err := ExtractFile(source, lang.Nix, "test", "module.nix")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	opts := optionsByName(result.Definitions)
	if _, ok := opts["enable"]; !ok {
		t.Fatalf("expected Option 'enable', got %v", definitionSummary(result.Definitions))
	}
}

func TestMkOption_MultipleSites(t *testing.T) {
	source := []byte(`
{
  options = {
    enable = mkOption { type = types.bool; default = false; };
    port = mkOption { type = types.int; default = 8080; };
    host = mkOption { type = types.str; default = "localhost"; };
  };
}
`)
	result, err := ExtractFile(source, lang.Nix, "test", "module.nix")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	opts := optionsByName(result.Definitions)
	for _, want := range []string{"enable", "port", "host"} {
		if _, ok := opts[want]; !ok {
			t.Errorf("expected Option %q, got %v", want, definitionSummary(result.Definitions))
		}
	}
}

func TestMkOption_NonMkOptionBindingNotExtracted(t *testing.T) {
	// `enable = false;` is a plain binding, not an mkOption invocation.
	// Must NOT emit an Option-labeled Definition.
	source := []byte(`
{
  config = {
    enable = false;
    services.foo = "bar";
  };
}
`)
	result, err := ExtractFile(source, lang.Nix, "test", "config.nix")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	for _, d := range result.Definitions {
		if d.Label == "Option" {
			t.Errorf("unexpected Option from non-mkOption binding: %q", d.Name)
		}
	}
}

func TestMkOption_DecoratorTagged(t *testing.T) {
	source := []byte(`
{
  options.foo = mkOption { type = types.bool; default = false; };
}
`)
	result, err := ExtractFile(source, lang.Nix, "test", "module.nix")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	for _, d := range result.Definitions {
		if d.Name == "foo" && d.Label == "Option" {
			found := false
			for _, dec := range d.Decorators {
				if dec == "mkOption" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected decorators=[\"mkOption\"], got %v", d.Decorators)
			}
			return
		}
	}
	t.Errorf("Option 'foo' not found in %v", definitionSummary(result.Definitions))
}

func TestMkOption_QualifiedFunctionName(t *testing.T) {
	// `lib.mkOption` (select_expression in tree-sitter) must also be
	// recognized — many Nix modules import `lib` and use the qualified
	// form rather than the unqualified `mkOption`.
	source := []byte(`
{
  options.bar = lib.mkOption { type = types.int; default = 0; };
}
`)
	result, err := ExtractFile(source, lang.Nix, "test", "module.nix")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	opts := optionsByName(result.Definitions)
	if _, ok := opts["bar"]; !ok {
		t.Errorf("expected Option 'bar' from lib.mkOption, got %v", definitionSummary(result.Definitions))
	}
}

func TestMkOption_DottedAttrpathUsesLeafName(t *testing.T) {
	// `services.foo.enable = mkOption {...};` — the option name is
	// `enable` (the leaf), not the full dotted path.
	source := []byte(`
{
  options.services.foo.enable = mkOption { type = types.bool; default = false; };
}
`)
	result, err := ExtractFile(source, lang.Nix, "test", "module.nix")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	opts := optionsByName(result.Definitions)
	if _, ok := opts["enable"]; !ok {
		t.Errorf("expected leaf Option 'enable' from dotted attrpath, got %v", definitionSummary(result.Definitions))
	}
}

// --- helpers ---

func optionsByName(defs []Definition) map[string]Definition {
	m := make(map[string]Definition, len(defs))
	for _, d := range defs {
		if d.Label == "Option" {
			m[d.Name] = d
		}
	}
	return m
}

func definitionSummary(defs []Definition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name+":"+d.Label)
	}
	return out
}
