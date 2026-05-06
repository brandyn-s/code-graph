package tools

// Loads grammar canary baselines for surfacing in index_health (Phase B4).
// The baselines file lives at bench/research/grammar_canaries/baselines.json,
// produced by bench/research/grammar_drift_check.py --update-baseline.
//
// We read the file at runtime rather than embedding so operators can
// re-baseline without rebuilding the binary. If the file is absent,
// index_health degrades gracefully (returns empty grammar versions map).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"time"
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
func loadGrammarVersionsAge() (map[string]string, int, error) {
	path, ok := findBaselinesPath()
	if !ok {
		return nil, -1, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, -1, nil
		}
		return nil, -1, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, -1, err
	}

	// baselines.json is { lang: { lang, size_bytes, content_sha256_first_4k, ... }, ... }
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, -1, err
	}

	versions := make(map[string]string, len(raw))
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

// findBaselinesPath locates bench/research/grammar_canaries/baselines.json
// by walking up from the current Go file's directory until it finds the
// repo root (a directory containing both `internal` and `bench`).
//
// Returns ("", false) if the file or repo root can't be found — common
// in production deployments where the binary ships without the bench/
// tree. Callers handle this gracefully.
func findBaselinesPath() (string, bool) {
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
