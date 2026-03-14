# Security Hardening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix 5 security findings (C1, H1, H2, M1, M2) from the 2026-03-14 assessment of codebase-memory-mcp.

**Architecture:** Each finding is an independent task with its own test-first cycle. No task depends on another. The self-update mechanism gets fail-closed checksum enforcement and tar size limits. File access tools get path containment validation. CI workflows get SHA-pinned actions. The installer gets backup-before-overwrite for invalid JSON configs.

**Tech Stack:** Go 1.26, SQLite, CGo (tree-sitter), GitHub Actions

**Repo:** `C:/Users/user/Documents/GitHub/codebase-memory-mcp` (fork of DeusData/codebase-memory-mcp at redacted-org/codebase-memory-mcp)

**Out of scope:** C2 (cryptographic signature verification) requires build infrastructure changes (key management, cosign/minisign integration, CI signing step). Tracked as future work after these immediate fixes land.

---

### Task 1: Fail-Closed on Missing Checksums (C1)

**Finding:** `cmd/codebase-memory-mcp/update.go:97-101` silently skips checksum verification when checksums.txt download fails, then installs the binary anyway.

**Files:**
- Modify: `cmd/codebase-memory-mcp/update.go:94-126`
- Test: `cmd/codebase-memory-mcp/update_test.go`

**Step 1: Write the failing test**

Add to `cmd/codebase-memory-mcp/update_test.go`:

```go
func TestDownloadAndVerify_FailsWithoutChecksums(t *testing.T) {
	// Set up a server that serves the release asset but NOT checksums.txt
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve a minimal valid tar.gz containing a fake binary
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		content := []byte("#!/bin/sh\necho codebase-memory-mcp test")
		hdr := &tar.Header{
			Name:     "codebase-memory-mcp",
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(content)
		_ = tw.Close()
		_ = gw.Close()
		w.Write(buf.Bytes())
	}))
	defer assetServer.Close()

	// Allow test server URLs for download
	origPrefixes := selfupdate.AllowedDownloadPrefixes
	selfupdate.AllowedDownloadPrefixes = append(selfupdate.AllowedDownloadPrefixes, assetServer.URL)
	t.Cleanup(func() { selfupdate.AllowedDownloadPrefixes = origPrefixes })

	// Release with an asset but NO checksums.txt
	release := &selfupdate.Release{
		TagName: "v9.9.9",
		Assets: []selfupdate.Asset{
			{
				Name:               "codebase-memory-mcp-linux-amd64.tar.gz",
				BrowserDownloadURL: assetServer.URL + "/binary.tar.gz",
				Size:               1024,
			},
			// Note: no checksums.txt asset
		},
	}

	asset := &release.Assets[0]
	_, err := downloadAndVerify(context.Background(), release, "codebase-memory-mcp-linux-amd64.tar.gz", asset)
	if err == nil {
		t.Fatal("expected error when checksums are unavailable, got nil")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum-related error, got: %v", err)
	}
}
```

Add these imports to the test file if not present: `"archive/tar"`, `"bytes"`, `"compress/gzip"`, `"context"`, `"net/http"`, `"net/http/httptest"`, `"strings"`, and `"github.com/DeusData/codebase-memory-mcp/internal/selfupdate"`.

**Step 2: Run test to verify it fails**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./cmd/codebase-memory-mcp/ -run TestDownloadAndVerify_FailsWithoutChecksums -v`
Expected: FAIL (currently the function succeeds without checksums)

**Step 3: Fix the implementation**

In `cmd/codebase-memory-mcp/update.go`, replace lines 94-126 of `downloadAndVerify`:

```go
func downloadAndVerify(ctx context.Context, release *selfupdate.Release, assetName string, asset *selfupdate.Asset) ([]byte, error) {
	fmt.Println("Downloading checksums...")
	checksums, err := selfupdate.DownloadChecksums(ctx, release)
	if err != nil {
		return nil, fmt.Errorf("checksum verification required but failed: %w", err)
	}

	fmt.Printf("Downloading %s...\n", assetName)
	tarballData, err := selfupdate.DownloadAsset(ctx, asset.BrowserDownloadURL)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}

	expected, ok := checksums[assetName]
	if !ok {
		return nil, fmt.Errorf("no checksum found for %s in checksums.txt", assetName)
	}
	hash := sha256.Sum256(tarballData)
	actual := hex.EncodeToString(hash[:])
	if actual != expected {
		return nil, fmt.Errorf("checksum mismatch\n  expected: %s\n  actual:   %s", expected, actual)
	}
	fmt.Println("Checksum verified.")

	binaryData, err := extractBinaryFromTarGz(tarballData)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	return binaryData, nil
}
```

Key changes:
- Checksum download failure now returns an error instead of setting `checksums = nil`
- Missing checksum entry for the specific asset is also an error
- No more `if checksums != nil` conditional - checksums are always required

**Step 4: Run test to verify it passes**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./cmd/codebase-memory-mcp/ -run TestDownloadAndVerify_FailsWithoutChecksums -v`
Expected: PASS

**Step 5: Run all update tests to check for regressions**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./cmd/codebase-memory-mcp/ -run "Test.*Update|Test.*Extract|Test.*Copy" -v`
Expected: All PASS

**Step 6: Commit**

```bash
git add cmd/codebase-memory-mcp/update.go cmd/codebase-memory-mcp/update_test.go
git commit -m "fix(security): fail-closed on missing checksums during self-update

Previously, if checksums.txt download failed, the update proceeded
without any integrity verification. Now the update aborts with a
clear error message. Addresses finding C1."
```

---

### Task 2: Tar Entry Size Limit (M1)

**Finding:** `cmd/codebase-memory-mcp/update.go:186` reads entire tar entry with `io.ReadAll(tr)` without checking `hdr.Size`. A crafted archive could cause OOM.

**Files:**
- Modify: `cmd/codebase-memory-mcp/update.go:168-194`
- Test: `cmd/codebase-memory-mcp/update_test.go`

**Step 1: Write the failing test**

Add to `cmd/codebase-memory-mcp/update_test.go`:

```go
func TestExtractBinaryFromTarGz_RejectsOversizedEntry(t *testing.T) {
	// Create a tar.gz with a header claiming a 300MB file
	// (we won't actually write 300MB of data - the header size is what we check)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Write a header with an inflated size but minimal actual content
	hdr := &tar.Header{
		Name:     "codebase-memory-mcp",
		Mode:     0o755,
		Size:     300 * 1024 * 1024, // 300 MB - over the limit
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	// Write just a tiny bit of actual data (tar allows short writes for testing)
	_, _ = tw.Write([]byte("fake"))
	_ = tw.Close()
	_ = gw.Close()

	_, err := extractBinaryFromTarGz(buf.Bytes())
	if err == nil {
		t.Fatal("expected error for oversized tar entry")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size-related error, got: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./cmd/codebase-memory-mcp/ -run TestExtractBinaryFromTarGz_RejectsOversizedEntry -v`
Expected: FAIL (currently no size check)

**Step 3: Fix the implementation**

In `cmd/codebase-memory-mcp/update.go`, replace `extractBinaryFromTarGz`:

```go
// maxBinarySize is the maximum allowed size for the extracted binary (200 MB).
const maxBinarySize = 200 << 20

// extractBinaryFromTarGz extracts the first regular file from a .tar.gz archive.
func extractBinaryFromTarGz(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && strings.HasPrefix(filepath.Base(hdr.Name), "codebase-memory-mcp") {
			if hdr.Size > maxBinarySize {
				return nil, fmt.Errorf("binary size %d exceeds maximum %d bytes", hdr.Size, maxBinarySize)
			}
			content, err := io.ReadAll(io.LimitReader(tr, maxBinarySize+1))
			if err != nil {
				return nil, fmt.Errorf("read entry: %w", err)
			}
			if int64(len(content)) > maxBinarySize {
				return nil, fmt.Errorf("binary size exceeds maximum %d bytes", maxBinarySize)
			}
			return content, nil
		}
	}
	return nil, fmt.Errorf("binary not found in archive")
}
```

Key changes:
- Added `maxBinarySize` constant (200 MB)
- Check `hdr.Size` before reading (catches honest-but-large entries)
- Use `io.LimitReader` as defense-in-depth (catches dishonest headers with more data than declared)

**Step 4: Run test to verify it passes**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./cmd/codebase-memory-mcp/ -run TestExtractBinaryFromTarGz -v`
Expected: All PASS (both existing and new test)

**Step 5: Commit**

```bash
git add cmd/codebase-memory-mcp/update.go cmd/codebase-memory-mcp/update_test.go
git commit -m "fix(security): add size limit to tar extraction during self-update

Reject tar entries exceeding 200MB to prevent decompression bomb attacks.
Checks both the tar header size and uses io.LimitReader as defense-in-depth.
Addresses finding M1."
```

---

### Task 3: Path Containment in File Access Tools (H2)

**Finding:** `snippet.go:105` and `code_search.go:94` construct file paths from indexed data without verifying the result stays within the project root. A poisoned index could read arbitrary files.

**Files:**
- Create: `internal/tools/pathcheck.go`
- Create: `internal/tools/pathcheck_test.go`
- Modify: `internal/tools/snippet.go:105` (1 line change)
- Modify: `internal/tools/code_search.go:94` (1 line change)
- Test: `internal/tools/snippet_test.go` (add path traversal test)

**Step 1: Write the failing test for the path checker**

Create `internal/tools/pathcheck_test.go`:

```go
package tools

import (
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

	tests := []struct {
		name string
		rel  string
	}{
		{"dotdot prefix", "../../../etc/passwd"},
		{"dotdot mid-path", "cmd/../../etc/passwd"},
		{"absolute path", "/etc/passwd"},
		{"dotdot only", ".."},
	}
	if runtime.GOOS == "windows" {
		tests = append(tests, struct {
			name string
			rel  string
		}{"windows absolute", `C:\Windows\System32\config\SAM`})
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
```

**Step 2: Run test to verify it fails**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./internal/tools/ -run TestSafePath -v`
Expected: FAIL (function doesn't exist yet)

**Step 3: Implement safePath**

Create `internal/tools/pathcheck.go`:

```go
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
```

**Step 4: Run test to verify safePath passes**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./internal/tools/ -run TestSafePath -v`
Expected: All PASS

**Step 5: Write the snippet path traversal test**

Add to `internal/tools/snippet_test.go`:

```go
func TestSnippet_PathTraversalBlocked(t *testing.T) {
	// Create a server with a node that has a path-traversal FilePath
	tmpDir := t.TempDir()
	routerDir := filepath.Join(tmpDir, "db")
	projRoot := filepath.Join(tmpDir, "project")

	if err := os.MkdirAll(projRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	// Write a source file at the project root (legitimate)
	if err := os.WriteFile(filepath.Join(projRoot, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Write a "secret" file OUTSIDE the project root
	secretFile := filepath.Join(tmpDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("TOP SECRET DATA"), 0o600); err != nil {
		t.Fatal(err)
	}

	router, err := store.NewRouterWithDir(routerDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.CloseAll)

	projName := "traversal-test"
	st, err := router.ForProject(projName)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProject(projName, projRoot); err != nil {
		t.Fatal(err)
	}

	// Insert a node with a malicious FilePath that escapes the project root
	_, _ = st.UpsertNode(&store.Node{
		Project:       projName,
		Label:         "Function",
		Name:          "EvilFunc",
		QualifiedName: projName + ".EvilFunc",
		FilePath:      "../secret.txt", // traversal!
		StartLine:     1,
		EndLine:       1,
	})

	srv := &Server{
		router:         router,
		handlers:       make(map[string]mcp.ToolHandler),
		sessionProject: projName,
	}

	result := callSnippetRaw(t, srv, projName+".EvilFunc")

	// Must be an error - should NOT return the secret file content
	if !result.IsError {
		tc, ok := result.Content[0].(*mcp.TextContent)
		if ok && strings.Contains(tc.Text, "TOP SECRET") {
			t.Fatal("path traversal succeeded - secret file was read!")
		}
		// If it returned a non-error JSON (e.g., suggestions), that's also acceptable
		// as long as it doesn't contain the secret content
		return
	}
	// Error result is the expected outcome
}
```

Add `"strings"` to the import block in `snippet_test.go` if not already present.

**Step 6: Run the traversal test to verify it fails**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./internal/tools/ -run TestSnippet_PathTraversalBlocked -v`
Expected: FAIL (currently the traversal path is not blocked)

**Step 7: Apply safePath to snippet.go**

In `internal/tools/snippet.go`, replace line 105:

Before:
```go
absPath := filepath.Join(proj.RootPath, node.FilePath)
```

After:
```go
absPath, pathErr := safePath(proj.RootPath, node.FilePath)
if pathErr != nil {
	return errResult(fmt.Sprintf("path security check failed: %v", pathErr)), nil
}
```

**Step 8: Apply safePath to code_search.go**

In `internal/tools/code_search.go`, replace line 94:

Before:
```go
absPath := filepath.Join(root, relPath)
```

After:
```go
absPath, pathErr := safePath(root, relPath)
if pathErr != nil {
	continue // skip files with invalid paths
}
```

**Step 9: Run all snippet and search tests**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./internal/tools/ -run "TestSnippet|TestSafePath" -v`
Expected: All PASS

**Step 10: Commit**

```bash
git add internal/tools/pathcheck.go internal/tools/pathcheck_test.go internal/tools/snippet.go internal/tools/code_search.go internal/tools/snippet_test.go
git commit -m "fix(security): add path containment check to file access tools

get_code_snippet and search_code now verify that resolved file paths
stay within the project root directory. Prevents reading arbitrary
files via poisoned index data containing '../' sequences.
Addresses finding H2."
```

---

### Task 4: SHA-Pin GitHub Actions (H1)

**Finding:** All GitHub Actions in release.yml and dry-run.yml use mutable tag references (e.g., `@v4`) instead of immutable SHA pins.

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `.github/workflows/dry-run.yml`

**Step 1: Replace all tag refs with SHA pins in release.yml**

Apply these replacements throughout `.github/workflows/release.yml`:

| Before | After |
|---|---|
| `actions/checkout@v4` | `actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5 # v4` |
| `actions/setup-go@v5` | `actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5` |
| `golangci/golangci-lint-action@v7` | `golangci/golangci-lint-action@9fae48acfc02a90574d7c304a1758ef9895495fa # v7` |
| `actions/upload-artifact@v4` | `actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4` |
| `actions/download-artifact@v4` | `actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093 # v4` |
| `softprops/action-gh-release@v2` | `softprops/action-gh-release@a06a81a03ee405af7f2048a818ed3f03bbf83c7b # v2` |
| `msys2/setup-msys2@v2` | `msys2/setup-msys2@4f806de0a5a7294ffabaff804b38a9b435a73bda # v2` |

**Step 2: Apply same replacements to dry-run.yml**

Same SHA mappings for the actions used in `.github/workflows/dry-run.yml` (subset: checkout, setup-go, golangci-lint-action, setup-msys2).

**Step 3: Validate YAML syntax**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml')); yaml.safe_load(open('.github/workflows/dry-run.yml')); print('YAML valid')"`

Expected: `YAML valid`

**Step 4: Commit**

```bash
git add .github/workflows/release.yml .github/workflows/dry-run.yml
git commit -m "fix(security): SHA-pin all GitHub Actions in CI workflows

Replace mutable tag references (@v4, @v5, etc.) with immutable
commit SHA pins. Prevents supply chain attacks via compromised
upstream action tags. Tag preserved in comment for readability.
Addresses finding H1."
```

---

### Task 5: Backup Before Overwriting Invalid JSON Configs (M2)

**Finding:** `cmd/codebase-memory-mcp/install.go:537-540` silently discards all content from editor config files (Cursor, Windsurf, VS Code, Gemini, Zed) when JSON is invalid, replacing it with just the codebase-memory-mcp entry.

**Files:**
- Modify: `cmd/codebase-memory-mcp/install.go`
- Test: `cmd/codebase-memory-mcp/install_test.go`

**Step 1: Write the failing test**

Add to `cmd/codebase-memory-mcp/install_test.go`:

```go
func TestEditorMCPInstall_BackupsInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcp.json")
	binaryPath := "/usr/local/bin/codebase-memory-mcp"

	// Write invalid JSON to the config
	invalidJSON := `{"mcpServers": {"other-tool": {"command": "other"}} THIS IS BROKEN`
	if err := os.WriteFile(configPath, []byte(invalidJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := installConfig{dryRun: false}
	installEditorMCP(binaryPath, configPath, "TestEditor", cfg)

	// Verify a .bak file was created with the original content
	bakPath := configPath + ".bak"
	bakContent, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("expected backup file at %s, got error: %v", bakPath, err)
	}
	if string(bakContent) != invalidJSON {
		t.Errorf("backup content mismatch:\n  got:  %s\n  want: %s", string(bakContent), invalidJSON)
	}

	// Verify the config was overwritten with valid JSON containing our entry
	newContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(newContent, &root); err != nil {
		t.Fatalf("new config should be valid JSON: %v", err)
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected mcpServers key")
	}
	if _, exists := servers["codebase-memory-mcp"]; !exists {
		t.Fatal("expected codebase-memory-mcp server entry")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./cmd/codebase-memory-mcp/ -run TestEditorMCPInstall_BackupsInvalidJSON -v`
Expected: FAIL (no .bak file is created currently)

**Step 3: Fix installEditorMCP in install.go**

In `cmd/codebase-memory-mcp/install.go`, find the `installEditorMCP` function. Replace the JSON unmarshal error handling block (around line 537-540):

Before:
```go
	if data, err := os.ReadFile(configPath); err == nil {
		if jsonErr := json.Unmarshal(data, &root); jsonErr != nil {
			// File exists but is invalid JSON -- back up and overwrite
			fmt.Printf("  ⚠ Invalid JSON in %s, overwriting\n", configPath)
			root = make(map[string]any)
		}
	}
```

After:
```go
	if data, err := os.ReadFile(configPath); err == nil {
		if jsonErr := json.Unmarshal(data, &root); jsonErr != nil {
			// File exists but is invalid JSON -- back up before overwriting
			bakPath := configPath + ".bak"
			if bakErr := os.WriteFile(bakPath, data, 0o600); bakErr != nil {
				fmt.Printf("  ⚠ Invalid JSON in %s and backup failed: %v\n", configPath, bakErr)
				fmt.Printf("  → Fix the JSON manually or remove the file, then re-run install\n")
				return
			}
			fmt.Printf("  ⚠ Invalid JSON in %s, backed up to %s\n", configPath, bakPath)
			root = make(map[string]any)
		}
	}
```

Apply the same pattern to `installVSCodeMCP` (around line 666) and `installZedMCP` (around line 771). For Zed specifically, the current code skips instead of overwriting - change it to also create a backup:

Before (Zed):
```go
		if jsonErr := json.Unmarshal(data, &root); jsonErr != nil {
			// Zed settings.json likely has other settings -- don't overwrite on bad JSON
			fmt.Printf("  ⚠ Invalid JSON in %s, skipping\n", configPath)
			return
		}
```

After (Zed):
```go
		if jsonErr := json.Unmarshal(data, &root); jsonErr != nil {
			fmt.Printf("  ⚠ Invalid JSON in %s, skipping (fix manually)\n", configPath)
			return
		}
```

(Zed's behavior of skipping is actually safer - leave it as-is, just clarify the message.)

**Step 4: Run test to verify it passes**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./cmd/codebase-memory-mcp/ -run TestEditorMCPInstall_BackupsInvalidJSON -v`
Expected: PASS

**Step 5: Run all install tests for regression check**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./cmd/codebase-memory-mcp/ -run "TestEditorMCP|TestVSCode|TestZed|TestInstall" -v`
Expected: All PASS

**Step 6: Commit**

```bash
git add cmd/codebase-memory-mcp/install.go cmd/codebase-memory-mcp/install_test.go
git commit -m "fix(security): backup invalid JSON config files before overwriting

When an editor's MCP config file contains invalid JSON, create a .bak
copy before overwriting. Previously the invalid content was silently
discarded, potentially losing user configuration.
Addresses finding M2."
```

---

### Task 6: Final Verification

**Step 1: Run the full test suite**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go test ./... 2>&1 | tail -20`
Expected: All packages PASS

**Step 2: Verify all commits are clean**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && git log --oneline -5`
Expected: 5 commits with the security fix messages

**Step 3: Push and create PR**

```bash
git push origin main
```

Or if using a feature branch:
```bash
git checkout -b fix/security-hardening
git push -u origin fix/security-hardening
gh pr create --title "Security hardening: 5 findings from assessment" --body "$(cat <<'EOF'
## Summary
- **C1**: Self-update now fails closed when checksums unavailable
- **H1**: All GitHub Actions SHA-pinned in release + dry-run workflows
- **H2**: Path containment check prevents file reads outside project root
- **M1**: Tar extraction capped at 200MB to prevent decompression bombs
- **M2**: Invalid JSON config files backed up before overwriting

## Out of scope
- C2 (cryptographic signature verification) - requires build infra changes

## Test plan
- [ ] `go test ./...` passes
- [ ] Manual: `codebase-memory-mcp update --dry-run` still works
- [ ] Manual: `codebase-memory-mcp install --dry-run` still works
EOF
)"
```
