package discover

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverBasic(t *testing.T) {
	dir := t.TempDir()

	// Create a Go file and a Python file
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("def main(): pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	files, err := Discover(ctx, dir, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	// Verify file info is populated
	for _, f := range files {
		if f.Path == "" {
			t.Error("expected non-empty Path")
		}
		if f.RelPath == "" {
			t.Error("expected non-empty RelPath")
		}
		if f.Language == "" {
			t.Error("expected non-empty Language")
		}
	}
}

func TestDiscoverSkipsWorktrees(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// .worktrees/ contains duplicated source trees from git worktrees
	wtDir := filepath.Join(dir, ".worktrees", "feature-branch", "src")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := Discover(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file (skipping .worktrees), got %d", len(files))
	}
	if filepath.Base(files[0].Path) != "main.go" {
		t.Errorf("expected main.go, got %s", files[0].Path)
	}
}

func TestDiscoverDotPrefixedRootInFastMode(t *testing.T) {
	// Regression: indexing a dot-prefixed root (e.g. ~/.claude) under
	// ModeFast used to abort the walk on the first iteration, producing
	// files_indexed=0. shouldSkipDir now short-circuits when rel == ".".
	parent := t.TempDir()
	dotRoot := filepath.Join(parent, ".myroot")
	if err := os.MkdirAll(dotRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotRoot, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotRoot, "app.py"), []byte("def main(): pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := Discover(context.Background(), dotRoot, &Options{Mode: ModeFast})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files in dot-prefixed root under ModeFast, got %d", len(files))
	}
}

func TestDiscoverCancellation(t *testing.T) {
	dir := t.TempDir()

	// Create a file so the directory isn't empty
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	_, err := Discover(ctx, dir, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
