package tools

import (
	"fmt"
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
