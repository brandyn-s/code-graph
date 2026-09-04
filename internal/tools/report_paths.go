package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// reportsDir is where generated documents (orientation reports, graph
// visualizations) live by default: <cache>/reports/<project>/. Writing there
// instead of into the indexed checkout keeps the checkout identity that the
// evidence contract is bound to untouched; a default index_repository run
// leaves `git status` exactly as it found it.
func (s *Server) reportsDir(project string) string {
	return filepath.Join(s.router.Dir(), "reports", project)
}

// resolveGeneratedOutputPath decides where a generated document is written.
//
//   - requested == "" → defaultPath (under reportsDir; never inside the checkout)
//   - relative path   → resolved under rootPath and containment-checked, an
//     explicit opt-in to writing into the checkout
//   - absolute path   → must be inside rootPath (explicit opt-in) or inside
//     the cache reports directory; anything else is rejected so a tool call
//     cannot be turned into an arbitrary file write.
//
// The returned path always has ext (e.g. ".md", ".html").
func (s *Server) resolveGeneratedOutputPath(rootPath, project, requested, defaultPath, ext string) (string, error) {
	var out string
	switch {
	case requested == "":
		out = defaultPath
	case filepath.IsAbs(requested):
		clean := filepath.Clean(requested)
		insideRoot := rootPath != "" && pathWithin(rootPath, clean)
		insideCache := pathWithin(s.reportsDir(project), clean)
		if !insideRoot && !insideCache {
			return "", fmt.Errorf("output path %q escapes project root %q and is not under the report directory %q", requested, rootPath, s.reportsDir(project))
		}
		out = clean
	default:
		if rootPath == "" {
			return "", fmt.Errorf("relative output path %q requires a project with a root path", requested)
		}
		safe, err := safePath(rootPath, requested)
		if err != nil {
			return "", fmt.Errorf("output path rejected: %w", err)
		}
		out = safe
	}
	if !strings.EqualFold(filepath.Ext(out), ext) {
		return "", fmt.Errorf("output path must have a %s extension", ext)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	return out, nil
}

// pathWithin reports whether target is root or lies beneath it (lexically,
// after cleaning both). Symlinks are not resolved; safePath handles the
// checkout case where that matters.
func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// writesIntoCheckout reports whether out lies inside the project root.
func writesIntoCheckout(rootPath, out string) bool {
	return rootPath != "" && pathWithin(rootPath, out)
}
