package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// safePath joins root and relPath, then verifies the result is still under root.
// Returns an error if the resolved path escapes the root directory.
func safePath(root, relPath string) (string, error) {
	absPath := filepath.Join(root, relPath)
	// Clean both paths to resolve any ".." components
	cleanRoot := filepath.Clean(root)
	cleanAbs := filepath.Clean(absPath)

	// The resolved path must start with the root path + separator (or equal root exactly)
	if cleanAbs != cleanRoot && !strings.HasPrefix(cleanAbs, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes project root %q", relPath, root)
	}

	return absPath, nil
}

// isForbiddenIndexPath returns true if path must not be indexed by
// index_repository. Reuses the isForbiddenSessionRoot checks (home dir,
// system dirs, cache dirs) and adds sensitive credential/config directories
// that could leak secrets if indexed and later queried.
func isForbiddenIndexPath(path string) bool {
	if isForbiddenSessionRoot(path) {
		return true
	}

	clean := filepath.Clean(path)
	cleanLower := strings.ToLower(clean)

	home, _ := os.UserHomeDir()
	if home == "" {
		return false
	}
	cleanHome := strings.ToLower(filepath.Clean(home))
	sepStr := string(filepath.Separator)

	for _, dotdir := range []string{
		".ssh",
		".aws",
		".gnupg",
		".gpg",
		".config",
		".kube",
		".docker",
		".password-store",
		".local",
	} {
		bad := cleanHome + sepStr + dotdir
		if cleanLower == bad || strings.HasPrefix(cleanLower, bad+sepStr) {
			return true
		}
	}

	return false
}
