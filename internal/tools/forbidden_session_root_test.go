package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Catches the 2026-05-13 regression where os.Getenv("HOME") returned empty
// on Windows and the home-dir guard silently failed, causing an unbounded
// auto-index walk of ~/.cache, AppData, etc.
func TestIsForbiddenSessionRoot_HomeDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("user home dir not resolvable on this platform")
	}
	if !isForbiddenSessionRoot(home) {
		t.Errorf("home dir %q must classify as forbidden but did not", home)
	}
}

func TestIsForbiddenSessionRoot_ParentOfHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("user home dir not resolvable on this platform")
	}
	parent := filepath.Dir(home)
	if !isForbiddenSessionRoot(parent) {
		t.Errorf("parent of home %q must classify as forbidden but did not", parent)
	}
}

func TestIsForbiddenSessionRoot_PosixRoot(t *testing.T) {
	if isForbiddenSessionRoot("/") != true {
		t.Errorf(`expected "/" to be forbidden`)
	}
}

func TestIsForbiddenSessionRoot_DriveRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive-root check is Windows-specific")
	}
	for _, p := range []string{`C:\`, `C:`, `D:\`} {
		if !isForbiddenSessionRoot(p) {
			t.Errorf("drive root %q must classify as forbidden but did not", p)
		}
	}
}

func TestIsForbiddenSessionRoot_EmptyPath(t *testing.T) {
	if !isForbiddenSessionRoot("") {
		t.Error("empty path must classify as forbidden")
	}
}

// SAFETY: legitimate project paths must NOT be flagged forbidden, or the
// guard becomes a denial-of-service.
func TestIsForbiddenSessionRoot_LegitimateRepoPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("user home dir not resolvable on this platform")
	}
	for _, sub := range []string{"Documents/GitHub/foo", "code/bar", "projects/baz"} {
		p := filepath.Join(home, sub)
		if isForbiddenSessionRoot(p) {
			t.Errorf("legitimate repo path %q must NOT be flagged forbidden", p)
		}
	}
}

func TestIsForbiddenSessionRoot_CacheSubdir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("user home dir not resolvable on this platform")
	}
	for _, sub := range []string{".cache", ".cache/some-project", "AppData", "AppData/Local/Temp"} {
		p := filepath.Join(home, sub)
		if !isForbiddenSessionRoot(p) {
			t.Errorf("scope-forbidden ancestor path %q must classify as forbidden", p)
		}
	}
}

func TestIsForbiddenSessionRoot_SystemDirs(t *testing.T) {
	cases := []string{
		"/etc", "/etc/passwd", "/var/log", "/usr/local",
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, `C:\Windows`, `C:\Windows\System32`, `C:\Program Files`, `C:\Program Files (x86)\foo`, `C:\ProgramData\bar`)
	}
	for _, p := range cases {
		if !isForbiddenSessionRoot(p) {
			t.Errorf("system dir %q must classify as forbidden", p)
		}
	}
}
