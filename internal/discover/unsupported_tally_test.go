package discover

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

func writeTallyFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedTally_CountsOnlyUnsupported(t *testing.T) {
	dir := t.TempDir()
	writeTallyFile(t, dir, "a.go", "package a\n")
	writeTallyFile(t, dir, "b.kt", "fun main() {}\n")
	writeTallyFile(t, dir, "c.kt", "fun other() {}\n")
	writeTallyFile(t, dir, "Makefile", "all:\n")
	writeTallyFile(t, dir, "noext", "plain text\n")

	tally := make(map[string]int)
	files, err := Discover(context.Background(), dir, &Options{UnsupportedTally: tally})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// a.go and Makefile are supported (extension / filename match) and must
	// NOT be tallied; b.kt, c.kt, and noext have no grammar.
	want := map[string]int{".kt": 2, "noext": 1}
	if !reflect.DeepEqual(tally, want) {
		t.Errorf("tally = %v, want %v", tally, want)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 supported files (a.go, Makefile), got %d", len(files))
	}
}

func TestUnsupportedTally_LowercasesKeys(t *testing.T) {
	dir := t.TempDir()
	writeTallyFile(t, dir, "X.KT", "fun main() {}\n")
	writeTallyFile(t, dir, "NOEXT", "plain\n")

	tally := make(map[string]int)
	if _, err := Discover(context.Background(), dir, &Options{UnsupportedTally: tally}); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := map[string]int{".kt": 1, "noext": 1}
	if !reflect.DeepEqual(tally, want) {
		t.Errorf("tally = %v, want %v", tally, want)
	}
}

func TestUnsupportedTally_IgnoreFiltersExcluded(t *testing.T) {
	dir := t.TempDir()
	// Under an ignored directory — must not be tallied.
	writeTallyFile(t, dir, filepath.Join("node_modules", "x.kt"), "fun a() {}\n")
	// Above MaxFileSize — must not be tallied (shouldSkipFile runs before
	// the language lookup in classifyFile).
	writeTallyFile(t, dir, "big.kt", "fun b() {} // padding to exceed the cap\n")
	// Survives every filter — the only tallied file.
	writeTallyFile(t, dir, "ok.kt", "fun c()\n")

	tally := make(map[string]int)
	_, err := Discover(context.Background(), dir, &Options{MaxFileSize: 10, UnsupportedTally: tally})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := map[string]int{".kt": 1}
	if !reflect.DeepEqual(tally, want) {
		t.Errorf("tally = %v, want %v", tally, want)
	}
}

func TestDiscoverOptionsZeroValue_UnsupportedTally_Inactive(t *testing.T) {
	// Zero-value activation rule (CLAUDE.md "Test Conventions"): with
	// Options{} (nil tally) and with opts == nil, discovery must behave
	// exactly as before — no tallying, no panic.
	dir := t.TempDir()
	writeTallyFile(t, dir, "a.go", "package a\n")
	writeTallyFile(t, dir, "b.kt", "fun main() {}\n")

	files, err := Discover(context.Background(), dir, &Options{})
	if err != nil {
		t.Fatalf("Discover with Options{}: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("Options{}: expected 1 supported file, got %d", len(files))
	}

	files, err = Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Discover with nil opts: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("nil opts: expected 1 supported file, got %d", len(files))
	}
}

func TestCutLanguageHints_NoneSupported(t *testing.T) {
	// If a cut language is restored, its grammar resolves again and the
	// hint entry must be removed — otherwise index_health would report a
	// supported language as cut.
	for ext := range CutLanguageHints {
		if _, ok := lang.LanguageForExtension(ext); ok {
			t.Errorf("CutLanguageHints lists %q but LanguageForExtension resolves it — remove the stale hint", ext)
		}
	}
}
