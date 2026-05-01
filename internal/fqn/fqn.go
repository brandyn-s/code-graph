package fqn

import (
	"path/filepath"
	"strings"
)

// jsTsIndexExts lists the file extensions for which a trailing
// `index` path component represents the canonical "module index" file
// (JS/TS module resolution). For these languages, the `index` segment
// is stripped from the QN so that `pkg/index.ts:foo` and `pkg/foo` in
// some other file land at the same module-level QN. Stripping for
// non-JS/TS files (e.g. Go's `internal/tools/index.go`) is a bug —
// see PR resolving the CBM Method QN inconsistency where
// `internal-tools/index.go`'s single Method dropped its file segment.
var jsTsIndexExts = map[string]struct{}{
	".js":  {},
	".jsx": {},
	".ts":  {},
	".tsx": {},
	".mjs": {},
	".cjs": {},
	".mts": {},
	".cts": {},
}

// Compute returns the canonical qualified name for a node.
// Format: <project>.<rel_path_parts_dotted>.<name>
// Examples:
//   - myproject.cmd.server.main.HandleRequest
//   - myproject.pkg.service.ProcessOrder
func Compute(project, relPath, name string) string {
	// Capture the original extension BEFORE stripping it. The
	// `index`-strip below is only safe for JS/TS-family files.
	ext := strings.ToLower(filepath.Ext(relPath))
	// Remove file extension
	relPath = strings.TrimSuffix(relPath, filepath.Ext(relPath))
	// Convert path separators to dots
	parts := strings.Split(filepath.ToSlash(relPath), "/")

	// For Python __init__.py, drop the __init__ part
	if ext == ".py" && len(parts) > 0 && parts[len(parts)-1] == "__init__" {
		parts = parts[:len(parts)-1]
	}
	// For JS/TS index files. Scoped by ext so Go's `index.go`,
	// Rust's `index.rs`, etc. are not mis-stripped.
	if _, ok := jsTsIndexExts[ext]; ok && len(parts) > 0 && parts[len(parts)-1] == "index" {
		parts = parts[:len(parts)-1]
	}

	all := append([]string{project}, parts...)
	if name != "" {
		all = append(all, name)
	}
	return strings.Join(all, ".")
}

// ModuleQN returns the qualified name for a module (file without function name).
func ModuleQN(project, relPath string) string {
	return Compute(project, relPath, "")
}

// FolderQN returns the qualified name for a folder.
func FolderQN(project, relDir string) string {
	parts := strings.Split(filepath.ToSlash(relDir), "/")
	all := append([]string{project}, parts...)
	return strings.Join(all, ".")
}
