package tools

// Grammar-telemetry provenance tests.
//
// The defect these pin: findBaselinesPath resolved baselines.json from
// runtime.Caller(0) — the BUILD MACHINE's source path, baked in at compile
// time — so whether index_health reported grammar_versions{,_age_days} depended
// on who compiled the binary:
//
//	locally built  -> embedded path exists here      -> fields present
//	CI built       -> /Users/runner/work/...         -> fields silently OMITTED
//
// Confirmed on one host by comparing three installed binaries built from
// identical source: both local builds emitted the fields; the CI-built
// pre-public release emitted neither. The field was therefore missing from every
// official release while present in dev builds, and its absence read as "no
// drift to report" rather than "cannot tell".
//
// Fixes pinned here:
//  1. CBM_GRAMMAR_BASELINES_PATH override, so a release binary can be pointed
//     at the file (and it WINS over the source-tree walk, which can succeed by
//     accident on a machine that happens to have a checkout at the build path).
//  2. grammar_versions_source is always reported, so "unavailable" is visible
//     instead of silent.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleBaselines = `{
  "go":     {"lang": "go",     "content_sha256_first_4k": "aaaa1111"},
  "python": {"lang": "python", "content_sha256_first_4k": "bbbb2222"}
}`

func writeBaselines(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "baselines.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write baselines: %v", err)
	}
	return path
}

func TestGrammarVersions_EnvOverrideIsUsed(t *testing.T) {
	path := writeBaselines(t, sampleBaselines)
	t.Setenv(grammarBaselinesPathEnv, path)

	versions, age, source, err := loadGrammarVersionsAgeSource()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != grammarSourceEnv {
		t.Errorf("source = %q, want %q", source, grammarSourceEnv)
	}
	if versions["go"] != "aaaa1111" || versions["python"] != "bbbb2222" {
		t.Errorf("versions not parsed from the override file: %v", versions)
	}
	if age < 0 {
		t.Errorf("age = %d, want >= 0 for a file that exists", age)
	}
}

// TestGrammarVersions_EnvOverrideWinsOverSourceTree is the load-bearing
// ordering property. Running under `go test` the source-tree walk SUCCEEDS
// (the test binary's compile-time path is this checkout), so if the override
// did not take precedence this test would silently read the repo's real
// baselines.json instead of the operator's file.
func TestGrammarVersions_EnvOverrideWinsOverSourceTree(t *testing.T) {
	if _, ok := findBaselinesPath(); !ok {
		t.Skip("source-tree baselines not reachable; ordering is untestable here")
	}
	path := writeBaselines(t, `{"only": {"lang": "only", "content_sha256_first_4k": "override-wins"}}`)
	t.Setenv(grammarBaselinesPathEnv, path)

	versions, _, source, err := loadGrammarVersionsAgeSource()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != grammarSourceEnv {
		t.Fatalf("source = %q, want the env override to win over the source tree", source)
	}
	if versions["only"] != "override-wins" {
		t.Errorf("read the source tree instead of the override: %v", versions)
	}
}

// TestGrammarVersions_MissingOverrideReportsUnavailable: a stale override must
// NOT silently fall back to a source tree the operator did not ask for.
func TestGrammarVersions_MissingOverrideReportsUnavailable(t *testing.T) {
	t.Setenv(grammarBaselinesPathEnv, filepath.Join(t.TempDir(), "does-not-exist.json"))

	versions, age, source, err := loadGrammarVersionsAgeSource()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != grammarSourceUnavailable {
		t.Errorf("source = %q, want %q", source, grammarSourceUnavailable)
	}
	if len(versions) != 0 || age != -1 {
		t.Errorf("expected empty result for a missing override, got versions=%v age=%d", versions, age)
	}
}

// TestGrammarVersions_SourceIsAlwaysNonEmpty is the anti-silence property: no
// matter which branch runs, index_health has something to report. That is the
// whole point — the previous behavior distinguished "no drift" from "cannot
// tell" not at all.
func TestGrammarVersions_SourceIsAlwaysNonEmpty(t *testing.T) {
	cases := map[string]func(*testing.T){
		"with-valid-override": func(t *testing.T) {
			t.Setenv(grammarBaselinesPathEnv, writeBaselines(t, sampleBaselines))
		},
		"with-missing-override": func(t *testing.T) {
			t.Setenv(grammarBaselinesPathEnv, filepath.Join(t.TempDir(), "nope.json"))
		},
		"no-override": func(t *testing.T) {
			t.Setenv(grammarBaselinesPathEnv, "")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t)
			_, _, source, err := loadGrammarVersionsAgeSource()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.TrimSpace(source) == "" {
				t.Error("grammar_versions_source is empty — absence would be silent again")
			}
		})
	}
}

// TestIndexHealth_AlwaysReportsGrammarSource pins the wiring: the handler must
// emit grammar_versions_source even when no baseline data is available.
func TestIndexHealth_AlwaysReportsGrammarSource(t *testing.T) {
	t.Setenv(grammarBaselinesPathEnv, filepath.Join(t.TempDir(), "absent.json"))
	s := newServerWithSeededProject(t)

	resp := metadataResponseFromHandler(t, s.handleIndexHealth, "index_health",
		map[string]any{"project": "test"})

	src, ok := resp["grammar_versions_source"].(string)
	if !ok || src == "" {
		t.Fatalf("index_health omitted grammar_versions_source; keys=%v", mapKeys(resp))
	}
	if src != grammarSourceUnavailable {
		t.Errorf("grammar_versions_source = %q, want %q", src, grammarSourceUnavailable)
	}
}
