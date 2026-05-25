package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSafePath_Valid(t *testing.T) {
	root := "/project/root"
	if runtime.GOOS == "windows" {
		root = `C:\project\root`
	}

	tests := []string{
		"main.go",
		"cmd/server/main.go",
		"internal/pkg/handler.go",
	}
	for _, rel := range tests {
		t.Run(rel, func(t *testing.T) {
			got, err := safePath(root, rel)
			if err != nil {
				t.Fatalf("safePath(%q, %q) error: %v", root, rel, err)
			}
			expected := filepath.Join(root, rel)
			if got != expected {
				t.Errorf("safePath(%q, %q) = %q, want %q", root, rel, got, expected)
			}
		})
	}
}

func TestSafePath_Traversal(t *testing.T) {
	root := "/project/root"
	if runtime.GOOS == "windows" {
		root = `C:\project\root`
	}

	// These paths escape the root after filepath.Join + filepath.Clean resolve ".."
	tests := []struct {
		name string
		rel  string
	}{
		{"dotdot prefix", "../../../etc/passwd"},
		{"dotdot mid-path", "cmd/../../etc/passwd"},
		{"dotdot only", ".."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := safePath(root, tt.rel)
			if err == nil {
				t.Fatalf("safePath(%q, %q) should have returned error for path traversal", root, tt.rel)
			}
		})
	}
}

func TestSafePath_AbsolutePathNeutralized(t *testing.T) {
	// filepath.Join neutralizes absolute second arguments by treating them as relative.
	// e.g. filepath.Join("/project/root", "/etc/passwd") = "/project/root/etc/passwd"
	// So absolute paths in relPath are NOT a traversal vector - they stay under root.
	root := "/project/root"
	if runtime.GOOS == "windows" {
		root = `C:\project\root`
	}

	absRel := "/etc/passwd" //nolint:gocritic // intentionally testing path-separator behavior
	got, err := safePath(root, absRel)
	if err != nil {
		t.Fatalf("absolute relPath should be neutralized by filepath.Join, got error: %v", err)
	}
	expected := filepath.Join(root, absRel) //nolint:gocritic // intentional
	if got != expected {
		t.Errorf("safePath result = %q, want %q", got, expected)
	}
}

func TestIsForbiddenIndexPath_SensitiveDotDirs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("user home dir not resolvable on this platform")
	}
	for _, dotdir := range []string{".ssh", ".aws", ".gnupg", ".gpg", ".config", ".kube", ".docker", ".password-store", ".local"} {
		p := filepath.Join(home, dotdir)
		if !isForbiddenIndexPath(p) {
			t.Errorf("sensitive dotdir %q must be forbidden for indexing", p)
		}
		sub := filepath.Join(home, dotdir, "subdir")
		if !isForbiddenIndexPath(sub) {
			t.Errorf("child of sensitive dotdir %q must be forbidden for indexing", sub)
		}
	}
}

func TestIsForbiddenIndexPath_InheritsSessionRootChecks(t *testing.T) {
	if !isForbiddenIndexPath("/") {
		t.Error("root dir must be forbidden for indexing")
	}
	if !isForbiddenIndexPath("/etc") {
		t.Error("/etc must be forbidden for indexing")
	}
	if !isForbiddenIndexPath("/var/log") {
		t.Error("/var/log must be forbidden for indexing")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("user home dir not resolvable on this platform")
	}
	if !isForbiddenIndexPath(home) {
		t.Errorf("home dir %q must be forbidden for indexing", home)
	}
}

func TestIsForbiddenIndexPath_LegitimateRepoPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("user home dir not resolvable on this platform")
	}
	for _, sub := range []string{
		"Documents/GitHub/my-project",
		"code/my-app",
		"projects/service-foo",
		"repos/internal-tool",
	} {
		p := filepath.Join(home, sub)
		if isForbiddenIndexPath(p) {
			t.Errorf("legitimate repo path %q must NOT be forbidden for indexing", p)
		}
	}
}
