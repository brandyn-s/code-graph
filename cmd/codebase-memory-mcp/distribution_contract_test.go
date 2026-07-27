package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	redactedRepository = "redacted-org/code-graph"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func readRepositoryFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestInternalDistributionUsesAuthenticatedGitHubCLI(t *testing.T) {
	manifestPath := filepath.Join(repositoryRoot(t), "server.json")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("server.json must not exist for a private package; stat error = %v", err)
	}
	readme := string(readRepositoryFile(t, "README.md"))
	for _, want := range []string{"private repository", "not published to the public MCP Registry"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md must document internal distribution with %q", want)
		}
	}

	tests := []struct {
		path       string
		repository string
	}{
		{
			path:       "scripts/setup.sh",
			repository: `REPO="` + redactedRepository + `"`,
		},
		{
			path:       "scripts/setup-windows.ps1",
			repository: `$Repo = "` + redactedRepository + `"`,
		},
	}
	required := []string{
		"gh auth status",
		"gh release view",
		"gh release download",
		"gh repo clone",
		"GitHub CLI is not authenticated",
		"remote get-url origin",
		"remote set-url origin",
		"remote add origin",
	}
	forbidden := []string{
		"api.github.com/repos/",
		"Invoke-WebRequest",
		"git clone https://github.com/",
		"curl -fsSL",
		"wget -qO-",
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			content := string(readRepositoryFile(t, tt.path))
			if !strings.Contains(content, tt.repository) {
				t.Errorf("%s must install from %s", tt.path, redactedRepository)
			}
			for _, want := range required {
				if !strings.Contains(content, want) {
					t.Errorf("%s must contain %q", tt.path, want)
				}
			}
			for _, unwanted := range forbidden {
				if strings.Contains(content, unwanted) {
					t.Errorf("%s must not contain anonymous distribution flow %q", tt.path, unwanted)
				}
			}
		})
	}
}

func TestUnixInstallerHelpDisclosesPrivateAuthentication(t *testing.T) {
	script := filepath.Join(repositoryRoot(t), "scripts", "setup.sh")
	output, err := exec.CommandContext(t.Context(), "bash", script, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("setup.sh --help: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "authenticated GitHub CLI") {
		t.Errorf("setup.sh --help must disclose private-repository authentication:\n%s", output)
	}
}

func TestInstallersVerifyArchiveBeforeExtraction(t *testing.T) {
	tests := []struct {
		name             string
		path             string
		download         string
		releaseVerify    string
		provenanceVerify string
		extract          string
		historicalTag    string
		historicalNotice string
	}{
		{
			name:             "unix",
			path:             "scripts/setup.sh",
			download:         `gh release download "$tag"`,
			releaseVerify:    `gh release verify-asset "$tag" "$archive_path"`,
			provenanceVerify: `gh attestation verify "$archive_path"`,
			extract:          `tar -xzf "$archive_path"`,
			historicalTag:    `HISTORICAL_NO_PROVENANCE_TAG="v0.7.0-redacted.2"`,
			historicalNotice: "has no build provenance; immutable release membership verified",
		},
		{
			name:             "windows",
			path:             "scripts/setup-windows.ps1",
			download:         `gh release download $tag`,
			releaseVerify:    `gh release verify-asset $tag $tmpZip`,
			provenanceVerify: `gh attestation verify $tmpZip`,
			extract:          `Expand-Archive -Path $tmpZip`,
			historicalTag:    `$HistoricalNoProvenanceTag = "v0.7.0-redacted.2"`,
			historicalNotice: "has no build provenance; immutable release membership verified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := string(readRepositoryFile(t, tt.path))
			for _, required := range []string{
				tt.download,
				tt.releaseVerify,
				tt.provenanceVerify,
				tt.extract,
				tt.historicalTag,
				tt.historicalNotice,
				"https://slsa.dev/provenance/v1",
			} {
				if !strings.Contains(content, required) {
					t.Fatalf("%s must contain %q", tt.path, required)
				}
			}

			downloadAt := strings.Index(content, tt.download)
			releaseVerifyAt := strings.Index(content, tt.releaseVerify)
			provenanceVerifyAt := strings.Index(content, tt.provenanceVerify)
			extractAt := strings.Index(content, tt.extract)
			if downloadAt >= releaseVerifyAt ||
				releaseVerifyAt >= provenanceVerifyAt ||
				provenanceVerifyAt >= extractAt {
				t.Fatalf(
					"%s unsafe order: download=%d release_verify=%d "+
						"provenance_verify=%d extract=%d",
					tt.path,
					downloadAt,
					releaseVerifyAt,
					provenanceVerifyAt,
					extractAt,
				)
			}
		})
	}
}

func TestUnixInstallerMigratesLegacySourceRemote(t *testing.T) {
	content := string(readRepositoryFile(t, "scripts/setup.sh"))
	const functionStart = "ensure_private_source_remote() {\n"
	start := strings.Index(content, functionStart)
	if start == -1 {
		t.Fatal("setup.sh must define ensure_private_source_remote")
	}
	end := strings.Index(content[start:], "\n}\n")
	if end == -1 {
		t.Fatal("setup.sh ensure_private_source_remote function must have a closing brace")
	}
	function := content[start : start+end+len("\n}\n")]

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", repo},
		{"-C", repo, "remote", "add", "origin", "https://github.com/DeusData/codebase-memory-mcp.git"},
	} {
		output, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}

	command := function + `
REPO="redacted-org/code-graph"
SOURCE_DIR="$1"
ensure_private_source_remote
`
	output, err := exec.CommandContext(t.Context(), "bash", "-c", command, "test", repo).CombinedOutput()
	if err != nil {
		t.Fatalf("migrate legacy source remote: %v\n%s", err, output)
	}
	remote, err := exec.CommandContext(t.Context(), "git", "-C", repo, "remote", "get-url", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("read migrated origin: %v\n%s", err, remote)
	}
	if got, want := strings.TrimSpace(string(remote)), "https://github.com/redacted-org/code-graph.git"; got != want {
		t.Errorf("migrated origin = %q, want %q", got, want)
	}
}
