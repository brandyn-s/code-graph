package discover

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// FullModeMaxFileSize returns the per-file size cutoff for full-mode
// discovery. Source files above ~1MB are essentially always GENERATED
// (tree-sitter parser tables, bundled assets, generated bindings) and are
// deliberately skipped by the indexing pipeline. This helper is the single
// source of truth for that cutoff so every consumer that must agree with
// what the indexer indexed — the pipeline itself, index_health's disk-vs-
// index comparison, and the watcher's change snapshot — applies the SAME
// threshold. When they disagree (health/watcher discovering with no limit
// while the pipeline skips >1MB files), health reports deliberately-skipped
// giant files as "missing", and the watcher thrashes a reindex on every
// touch of a file the indexer will never index.
//
// Override with CBM_MAX_FILE_BYTES: a positive integer sets the cutoff in
// bytes; "0" disables the limit entirely; unset or unparsable uses the 1MB
// default. Discover treats a MaxFileSize of 0 as "no limit".
func FullModeMaxFileSize() int64 {
	const defaultMax = 1 << 20 // 1MB
	v := os.Getenv("CBM_MAX_FILE_BYTES")
	if v == "" {
		return defaultMax
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		slog.Warn("discover.max_file_bytes.invalid", "value", v, "using", defaultMax)
		return defaultMax
	}
	return n // 0 = unlimited
}

// IGNORE_PATTERNS are directory names to skip during discovery.
var IGNORE_PATTERNS = map[string]bool{
	// VCS / IDE
	".git": true, ".hg": true, ".svn": true, ".worktrees": true,
	".idea": true, ".vs": true, ".vscode": true, ".eclipse": true, ".claude": true,
	// Python
	".cache": true, ".eggs": true, ".env": true, ".mypy_cache": true, ".nox": true,
	".pytest_cache": true, ".ruff_cache": true, ".tox": true, ".venv": true,
	"__pycache__": true, "env": true, "htmlcov": true, "site-packages": true, "venv": true,
	// JS/TS
	".npm": true, ".nyc_output": true, ".pnpm-store": true, ".yarn": true,
	"bower_components": true, "coverage": true, "node_modules": true,
	// JS/TS framework caches
	".next": true, ".nuxt": true, ".svelte-kit": true, ".angular": true,
	".turbo": true, ".parcel-cache": true, ".docusaurus": true, ".expo": true,
	// Build artifacts
	"dist": true, "obj": true, "Pods": true, "target": true, "temp": true, "tmp": true,
	// Build system caches
	".terraform": true, ".serverless": true,
	"bazel-bin": true, "bazel-out": true, "bazel-testlogs": true,
	// Language-specific caches
	".cargo": true, ".stack-work": true, ".dart_tool": true,
	"zig-cache": true, "zig-out": true,
	".metals": true, ".bloop": true, ".bsp": true,
	".ccls-cache": true, ".clangd": true,
	"elm-stuff": true, "_opam": true, ".cpcache": true, ".shadow-cljs": true,
	// Deploy/hosting
	".vercel": true, ".netlify": true,
	// Misc
	".qdrant_code_embeddings": true, ".tmp": true, "vendor": true,
}

// IGNORE_SUFFIXES are file suffixes that are never source files.
var IGNORE_SUFFIXES = map[string]bool{
	// Temp/compiled
	".tmp": true, "~": true, ".pyc": true, ".pyo": true,
	".o": true, ".a": true, ".so": true, ".dll": true, ".class": true,
	// Images
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".ico": true, ".bmp": true, ".tiff": true, ".webp": true, ".svg": true,
	// Binaries
	".wasm": true, ".node": true, ".exe": true, ".bin": true, ".dat": true,
	// Databases
	".db": true, ".sqlite": true, ".sqlite3": true,
	// Fonts
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true, ".otf": true,
}

// IndexMode controls how aggressively files are filtered during discovery.
type IndexMode string

const (
	ModeFull IndexMode = "full" // default: parse everything supported
	ModeFast IndexMode = "fast" // aggressive filtering for speed
)

// FileInfo represents a discovered source file.
type FileInfo struct {
	Path     string        // absolute path
	RelPath  string        // relative to repo root
	Language lang.Language // detected language
	Size     int64         // file size in bytes
}

// Options configures file discovery.
type Options struct {
	IgnoreFile  string    // path to .cgrignore file (optional)
	Mode        IndexMode // indexing mode (full or fast)
	MaxFileSize int64     // max file size in bytes (0 = no limit)
	// UnsupportedTally, when non-nil, counts files that pass every ignore
	// filter but have no supported language — exactly the population that
	// would have been indexed if a grammar existed. Keyed by lowercased
	// extension (or lowercased bare filename when there is no extension).
	// Discover's walk is single-goroutine (filepath.Walk), so a plain map
	// is safe.
	UnsupportedTally map[string]int
}

// fastIgnoreDirs are additional directories skipped in fast mode.
var fastIgnoreDirs = map[string]bool{
	"generated": true, "gen": true, "auto-generated": true,
	"fixtures": true, "testdata": true, "test_data": true,
	"__tests__": true, "__mocks__": true, "__snapshots__": true,
	"__fixtures__": true, "__test__": true,
	"docs": true, "doc": true, "documentation": true,
	"examples": true, "example": true, "samples": true, "sample": true,
	"assets": true, "static": true, "public": true, "media": true,
	"third_party": true, "thirdparty": true, "3rdparty": true, "external": true,
	"migrations": true, "seeds": true,
	"e2e": true, "integration": true,
	"locale": true, "locales": true, "i18n": true, "l10n": true,
	"scripts": true, "tools": true, "hack": true,
	// Generic dirs moved from IGNORE_PATTERNS (cause false exclusions in Go, CMake, Maven)
	"bin": true, "build": true, "out": true,
}

// fastIgnoreSuffixes are additional file extensions skipped in fast mode.
var fastIgnoreSuffixes = map[string]bool{
	// Archives
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true,
	".rar": true, ".7z": true, ".jar": true, ".war": true, ".ear": true,
	// Media/audio/video
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wav": true,
	".flac": true, ".ogg": true, ".mkv": true, ".webm": true,
	// Documents
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".ppt": true, ".pptx": true, ".odt": true, ".ods": true,
	// Source maps
	".map": true,
	// Minified bundles
	".min.js": true, ".min.css": true,
	// Certificates/keys
	".pem": true, ".crt": true, ".key": true, ".cer": true, ".p12": true,
	// Serialized data
	".pb": true, ".proto": true, ".avro": true, ".parquet": true,
	// Compiled/intermediate
	".beam": true, ".elc": true, ".rlib": true,
	// Coverage/profiling
	".coverage": true, ".prof": true, ".out": true,
	// Patches
	".patch": true, ".diff": true,
}

// fastIgnoreFilenames are specific filenames skipped in fast mode.
var fastIgnoreFilenames = map[string]bool{
	"LICENSE": true, "LICENSE.txt": true, "LICENSE.md": true, "LICENSE-MIT": true, "LICENSE-APACHE": true,
	"LICENCE": true, "LICENCE.txt": true, "LICENCE.md": true,
	"CHANGELOG": true, "CHANGELOG.md": true, "CHANGES.md": true,
	"HISTORY": true, "HISTORY.md": true,
	"AUTHORS": true, "AUTHORS.md": true, "CONTRIBUTORS": true, "CONTRIBUTORS.md": true,
	"CODEOWNERS": true,
	"go.sum":     true, "yarn.lock": true, "pnpm-lock.yaml": true, "Pipfile.lock": true,
	"poetry.lock": true, "Gemfile.lock": true, "Cargo.lock": true, "mix.lock": true,
	"flake.lock": true, "pubspec.lock": true, "composer.lock": true,
	"configure": true, "Makefile.in": true, "config.guess": true, "config.sub": true,
	"package-lock.json": true,
}

// fastIgnorePatterns are suffix/contains patterns skipped in fast mode.
var fastIgnorePatterns = []string{
	".d.ts",          // TypeScript declaration files
	".bundle.",       // bundled files
	".chunk.",        // code-split chunks
	".generated.",    // generated code
	".pb.go",         // protobuf generated Go
	"_pb2.py",        // protobuf generated Python
	".pb2.py",        // protobuf generated Python (alt)
	"_grpc.pb.go",    // gRPC generated Go
	"_string.go",     // stringer generated Go
	"mock_",          // mock files prefix
	"_mock.",         // mock files suffix
	"_test_helpers.", // test helpers
	".stories.",      // Storybook stories
	".spec.",         // spec/test files
	".test.",         // test files
}

// shouldSkipDir returns true if the directory should be skipped during discovery.
func shouldSkipDir(name, rel string, extraIgnore []string, mode IndexMode) bool {
	// Never skip the root — the user explicitly chose this path. Without this
	// short-circuit, indexing a dot-prefixed root (e.g. ~/.claude) under
	// ModeFast aborts the walk on the first iteration via the dot-prefix
	// filter below, producing files_indexed=0.
	if rel == "." {
		return false
	}
	if IGNORE_PATTERNS[name] {
		return true
	}
	if mode == ModeFast {
		// Skip all dot-directories not already in IGNORE_PATTERNS
		if strings.HasPrefix(name, ".") {
			return true
		}
		if fastIgnoreDirs[name] {
			return true
		}
	}
	for _, pattern := range extraIgnore {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, rel); matched {
			return true
		}
	}
	return false
}

// shouldSkipFile returns true if the file should be skipped in fast mode.
func shouldSkipFile(name, path string, size int64, opts *Options) bool {
	// File size limit (both modes)
	if opts != nil && opts.MaxFileSize > 0 && size > opts.MaxFileSize {
		return true
	}
	if opts == nil || opts.Mode != ModeFast {
		return false
	}
	// Fast-mode filename filter
	if fastIgnoreFilenames[name] {
		return true
	}
	// Fast-mode suffix filter
	for suffix := range fastIgnoreSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	// Fast-mode pattern filter (contains/suffix patterns)
	for _, pattern := range fastIgnorePatterns {
		if strings.Contains(path, pattern) {
			return true
		}
	}
	return false
}

// hasIgnoredSuffix returns true if path ends with any suffix in IGNORE_SUFFIXES.
func hasIgnoredSuffix(path string) bool {
	for suffix := range IGNORE_SUFFIXES {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// classifyFile checks if a file is a supported source file and returns its FileInfo.
// Returns nil if the file should be skipped.
func classifyFile(path, rel string, info os.FileInfo, opts *Options) *FileInfo {
	if hasIgnoredSuffix(path) {
		return nil
	}
	if shouldSkipFile(info.Name(), path, info.Size(), opts) {
		return nil
	}

	ext := filepath.Ext(path)

	l, ok := lang.LanguageForExtension(ext)
	if !ok {
		l, ok = lang.LanguageForFilename(info.Name())
	}
	if !ok {
		if opts != nil && opts.UnsupportedTally != nil {
			key := strings.ToLower(ext)
			if key == "" {
				key = strings.ToLower(info.Name())
			}
			opts.UnsupportedTally[key]++
		}
		return nil
	}

	if l == lang.JSON && isIgnoredJSON(info.Name()) {
		return nil
	}
	if l == lang.JSON && info.Size() > 100*1024 {
		slog.Warn("discover.skip_large_json", "path", rel, "size", info.Size())
		return nil
	}

	return &FileInfo{
		Path:     path,
		RelPath:  filepath.ToSlash(rel),
		Language: l,
		Size:     info.Size(),
	}
}

// Discover walks a repository and returns all source files.
func Discover(ctx context.Context, repoPath string, opts *Options) ([]FileInfo, error) {
	repoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var extraIgnore []string
	if opts != nil && opts.IgnoreFile != "" {
		extraIgnore, _ = loadIgnoreFile(opts.IgnoreFile)
	} else {
		ignPath := filepath.Join(repoPath, ".cgrignore")
		extraIgnore, _ = loadIgnoreFile(ignPath)
	}

	var files []FileInfo

	mode := ModeFull
	if opts != nil && opts.Mode != "" {
		mode = opts.Mode
	}

	// Load gitignore + cbmignore matchers (nil-safe for non-git repos)
	matchers := loadIgnoreMatchers(repoPath)

	err = filepath.Walk(repoPath, func(path string, info os.FileInfo, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return filepath.SkipDir
		}

		// Skip symlinks — prevents duplicate indexing
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		rel, _ := filepath.Rel(repoPath, path)

		if info.IsDir() {
			// Hardcoded patterns first (fast O(1) map lookup)
			if shouldSkipDir(info.Name(), rel, extraIgnore, mode) {
				return filepath.SkipDir
			}
			// Then gitignore + cbmignore (skip root — library panics on base==path)
			if rel != "." && matchers.shouldIgnore(path, true) {
				slog.Debug("discover.gitignore_skip", "dir", rel)
				return filepath.SkipDir
			}
			return nil
		}

		// Gitignore + cbmignore check for files
		if matchers.shouldIgnore(path, false) {
			return nil
		}

		if fi := classifyFile(path, rel, info, opts); fi != nil {
			files = append(files, *fi)
		}
		return nil
	})

	return files, err
}

// ignoredJSONFiles are JSON filenames to skip (tool configs, lock files, specs).
var ignoredJSONFiles = map[string]bool{
	"package.json":       true,
	"package-lock.json":  true,
	"tsconfig.json":      true,
	"jsconfig.json":      true,
	"composer.json":      true,
	"composer.lock":      true,
	"yarn.lock":          true,
	"openapi.json":       true,
	"swagger.json":       true,
	"jest.config.json":   true,
	".eslintrc.json":     true,
	".prettierrc.json":   true,
	".babelrc.json":      true,
	"tslint.json":        true,
	"angular.json":       true,
	"firebase.json":      true,
	"renovate.json":      true,
	"lerna.json":         true,
	"turbo.json":         true,
	".stylelintrc.json":  true,
	"pnpm-lock.json":     true,
	"deno.json":          true,
	"biome.json":         true,
	"devcontainer.json":  true,
	".devcontainer.json": true,
	"launch.json":        true,
	"settings.json":      true,
	"extensions.json":    true,
	"tasks.json":         true,
}

func isIgnoredJSON(name string) bool {
	return ignoredJSONFiles[name]
}

func loadIgnoreFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, line)
		}
	}
	return patterns, scanner.Err()
}
