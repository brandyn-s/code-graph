package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestEdgeTypeTableIsUniqueAndDocumented(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range EdgeTypes {
		if seen[e.Type] {
			t.Errorf("duplicate edge type %s", e.Type)
		}
		seen[e.Type] = true
		if e.Family == "" || e.Doc == "" || e.Source == "" || e.Target == "" {
			t.Errorf("%s is missing family, roles, or doc", e.Type)
		}
	}
	root := repoRootForEdgeTypes(t)
	doc, err := os.ReadFile(filepath.Join(root, "docs", "edge-types.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range EdgeTypes {
		if !strings.Contains(string(doc), "`"+e.Type+"`") {
			t.Errorf("%s is not described in docs/edge-types.md", e.Type)
		}
	}
}

// TestNoUndeclaredEdgeTypeLiterals fails when production code constructs an
// edge with a string literal that is not in the EdgeTypes table. Adding an
// edge kind means adding it here first, with its semantics.
func TestNoUndeclaredEdgeTypeLiterals(t *testing.T) {
	root := repoRootForEdgeTypes(t)
	literal := regexp.MustCompile(`\bType:\s*"([A-Z][A-Z_]+)"`)
	var offenders []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendored" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "edge_types.go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range literal.FindAllStringSubmatch(string(b), -1) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel+": "+m[0])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("edge types must use the store.Edge* constants:\n  %s", strings.Join(offenders, "\n  "))
	}
}

func repoRootForEdgeTypes(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
