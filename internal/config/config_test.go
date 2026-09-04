package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
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
			t.Fatal("go.mod not found above test directory")
		}
		dir = parent
	}
}

// productKeyPattern matches variable names this project owns. Platform
// variables (PATH, HOME, ...) are deliberately excluded.
var productKeyPattern = regexp.MustCompile(`^(CODE_GRAPH_|CBM_|RESOLVER_|LOCAGENT_|VOYAGE_|ANTHROPIC_|EMBEDDING_SEED_|ENABLE_SIMILARITY_)`)

func TestEveryKeyIsDocumented(t *testing.T) {
	root := repoRoot(t)
	var docs strings.Builder
	for _, name := range []string{"README.md", "CLAUDE.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		docs.Write(b)
	}
	text := docs.String()
	for _, k := range All() {
		if !strings.Contains(text, "`"+k.Name+"`") {
			t.Errorf("%s is read by the binary but not documented in README.md or CLAUDE.md", k.Name)
		}
	}
}

func TestEveryDocumentedProductKeyIsDeclared(t *testing.T) {
	root := repoRoot(t)
	declared := map[string]bool{}
	for _, k := range All() {
		declared[k.Name] = true
	}
	row := regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]+)`")
	for _, name := range []string{"README.md", "CLAUDE.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range row.FindAllStringSubmatch(string(b), -1) {
			key := m[1]
			if productKeyPattern.MatchString(key) && !declared[key] {
				t.Errorf("%s documents %s but internal/config does not declare it", name, key)
			}
		}
	}
}

func TestNoDirectProductEnvReadsOutsideConfig(t *testing.T) {
	root := repoRoot(t)
	literal := regexp.MustCompile(`os\.(Getenv|LookupEnv)\("([A-Z][A-Z0-9_]*)"\)`)
	var offenders []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				base := d.Name()
				if base == "vendored" || base == "config" || base == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range literal.FindAllStringSubmatch(string(b), -1) {
				if productKeyPattern.MatchString(m[2]) {
					rel, _ := filepath.Rel(root, path)
					offenders = append(offenders, rel+": "+m[0])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("product environment variables must be read through internal/config:\n  %s", strings.Join(offenders, "\n  "))
	}
}

func TestKeyNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range All() {
		if seen[k.Name] {
			t.Errorf("duplicate key %s", k.Name)
		}
		seen[k.Name] = true
		if k.Doc == "" || k.Default == "" {
			t.Errorf("%s needs Doc and Default", k.Name)
		}
	}
}

func TestTruthyAndSnapshotRedaction(t *testing.T) {
	t.Setenv(SimilarityEdges.Name, " Yes ")
	if !Truthy(SimilarityEdges) {
		t.Fatal("expected truthy")
	}
	t.Setenv(VoyageAPIKey.Name, "sk-secret")
	for _, r := range Snapshot() {
		if r.Name == VoyageAPIKey.Name && r.Value != "<set>" {
			t.Fatalf("secret leaked in snapshot: %q", r.Value)
		}
	}
}
