package tools

// Loads grammar canary baselines for surfacing in index_health (Phase B4).
// The baselines file lives at bench/research/grammar_canaries/baselines.json,
// produced by bench/research/grammar_drift_check.py --update-baseline.
//
// We read the file at runtime rather than embedding so operators can
// re-baseline without rebuilding the binary. If the file is absent,
// index_health degrades gracefully (returns empty grammar versions map).
//
// PATH RESOLUTION (fixed 2026-07-27): resolution used ONLY runtime.Caller(0),
// which yields the BUILD MACHINE's source path baked in at compile time. That
// made the telemetry's presence depend on WHO COMPILED the binary, invisibly:
//
//	locally-built  -> embedded path /Users/<me>/.../code-graph/internal/tools/...
//	               -> exists on this host -> walk finds bench/ -> fields present
//	CI-built       -> embedded path /Users/runner/work/code-graph/...
//	               -> absent on this host -> findBaselinesPath false -> fields
//	                  silently OMITTED from every index_health response
//
// Confirmed by comparing three installed binaries on one host: the two locally
// built ones emitted grammar_versions{,_age_days}; the CI-built v0.7.0-redacted.2
// emitted neither, with identical source for both. So the field was missing from
// every official RELEASE while appearing in dev builds — the worst shape for a
// staleness signal, since its absence looked like "no drift to report".
//
// Two changes:
//  1. CBM_GRAMMAR_BASELINES_PATH lets an operator point at the file explicitly.
//     This is the supported path for release binaries, and it keeps the
//     re-baseline-without-rebuild property the runtime read exists for.
//  2. index_health now always reports grammar_versions_source, so "no grammar
//     data" is distinguishable from "grammar data is fresh". A telemetry field
//     that vanishes silently cannot be acted on.
//
// NOTE ON THE AGE SEMANTICS: ageDays is the baselines FILE's mtime age, not the
// age of the vendored grammars. On the host that surfaced this, the reported
// "46 days" was simply how long ago baselines.json had last been written
// locally. It answers "how stale is my baseline snapshot?", NOT "how old are
// the grammars?" — the field name invites the second reading, so the source
// field below names what is actually being measured.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/brandyn-s/code-graph/internal/config"
)

// grammarBaselinesPathEnv lets an operator point at baselines.json explicitly.
// Required for release binaries, whose compile-time source path does not exist
// on the deployment host (see the path-resolution note above).
const grammarBaselinesPathEnv = "CBM_GRAMMAR_BASELINES_PATH"

// Grammar-telemetry provenance values for index_health's
// grammar_versions_source field. Always emitted, so an operator can tell
// "baseline absent" from "baseline fresh" instead of seeing nothing.
const (
	grammarSourceEnv           = "env:" + grammarBaselinesPathEnv
	grammarSourceSourceTree    = "source-tree"
	grammarSourceUnavailable   = "unavailable"
	grammarSourceUnavailableCI = "unavailable (release binary: set " + grammarBaselinesPathEnv + " to enable)"
)

// loadGrammarVersionsAge returns:
//   - grammarVersions: map of language -> fingerprint (currently the
//     placeholder_v0 content-hash; will be the AST-shape SHA when the
//     real fingerprint lands).
//   - ageDays: days since the baselines file was last modified (≥ 0)
//     or -1 if the file isn't found.
//   - err: non-nil only on unexpected I/O errors; "file missing" is
//     treated as a clean (empty, age=-1) result.
//
// The function locates the baselines file relative to the running binary's
// repo (when invoked from the test/dev tree) by walking up from the
// current package directory. In production deployments where the binary
// is shipped without the bench/ tree, the file won't exist and this
// returns ({}, -1, nil) — index_health responses simply omit the grammar
// version block.
func loadGrammarVersionsAge() (versions map[string]string, ageDays int, err error) {
	versions, ageDays, _, err = loadGrammarVersionsAgeSource()
	return versions, ageDays, err
}

// loadGrammarVersionsAgeSource is loadGrammarVersionsAge plus a provenance
// string describing WHERE the baselines came from (or why they are missing).
// The provenance is what makes the absence case actionable.
func loadGrammarVersionsAgeSource() (versions map[string]string, ageDays int, source string, err error) {
	path, source, ok := findBaselinesPathSource()
	if !ok {
		return nil, -1, source, nil
	}

	versions, ageDays, err = readBaselines(path)
	if err != nil {
		return nil, -1, source, err
	}
	if versions == nil {
		// File named but absent (e.g. a stale env override).
		return nil, -1, grammarSourceUnavailable, nil
	}
	return versions, ageDays, source, nil
}

// readBaselines reads and parses the baselines file at path.
//
// #nosec G304 -- path comes from either the compile-time source-tree walk or
// CBM_GRAMMAR_BASELINES_PATH, an operator-set environment variable on the
// machine running the server. Both are trusted-operator inputs, not
// request-derived; an operator who can set the server's environment can already
// read any file the process can. The file is parsed as JSON and only
// fingerprint strings are surfaced, so contents are never executed.
func readBaselines(path string) (versions map[string]string, ageDays int, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, -1, nil
		}
		return nil, -1, err
	}

	data, err := os.ReadFile(path) // #nosec G304 -- see doc comment
	if err != nil {
		return nil, -1, err
	}

	// baselines.json is { lang: { lang, size_bytes, content_sha256_first_4k, ... }, ... }
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, -1, err
	}

	versions = make(map[string]string, len(raw))
	for lang, fields := range raw {
		// Use content_sha256_first_4k as the version fingerprint. When the
		// real AST-shape fingerprint lands, this key changes; the
		// fallback to size_bytes catches the transition.
		if v, ok := fields["content_sha256_first_4k"].(string); ok && v != "" {
			versions[lang] = v
			continue
		}
		if v, ok := fields["fingerprint"].(string); ok && v != "" {
			versions[lang] = v
		}
	}

	age := int(time.Since(info.ModTime()).Hours() / 24)
	if age < 0 {
		age = 0
	}
	return versions, age, nil
}

// findBaselinesPathSource resolves baselines.json, preferring an explicit
// operator override over the compile-time source path.
//
// Order matters: the env override MUST win, because the source-tree walk
// succeeds by accident on a machine that happens to have a checkout at the
// build path — which is exactly how this became build-machine-dependent.
func findBaselinesPathSource() (path, source string, ok bool) {
	if override := config.Get(config.GrammarBaselinesPath); override != "" {
		// #nosec G703 -- the path is operator-supplied via the server's own
		// environment, not request-derived. Anyone able to set it can already
		// read whatever the process can; only JSON fingerprint strings are
		// surfaced from the file.
		if _, err := os.Stat(override); err == nil {
			return override, grammarSourceEnv, true
		}
		// Named but unreadable: report unavailable rather than silently
		// falling back to a source tree the operator did not ask for.
		return "", grammarSourceUnavailable, false
	}
	if path, ok := findBaselinesPath(); ok {
		return path, grammarSourceSourceTree, true
	}
	// No override and no reachable source tree — the release-binary case.
	// Name the remedy in the provenance string itself.
	return "", grammarSourceUnavailableCI, false
}

// findBaselinesPath locates bench/research/grammar_canaries/baselines.json
// by walking up from the current Go file's directory until it finds the
// repo root (a directory containing both `internal` and `bench`).
//
// Returns ("", false) if the file or repo root can't be found. This is the
// NORMAL case for a release binary: runtime.Caller(0) yields the build
// machine's path, which does not exist on the deployment host. Use
// CBM_GRAMMAR_BASELINES_PATH there — see the package note above.
func findBaselinesPath() (path string, ok bool) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	dir := filepath.Dir(thisFile)
	// Walk up until we see both internal/ and bench/.
	for i := 0; i < 10; i++ {
		intDir := filepath.Join(dir, "internal")
		benchDir := filepath.Join(dir, "bench")
		if statIsDir(intDir) && statIsDir(benchDir) {
			return filepath.Join(benchDir, "research", "grammar_canaries", "baselines.json"), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	return "", false
}

func statIsDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
