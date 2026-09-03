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
	releaseRepository = "brandyn-s/code-graph"
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

// TestPublicInstallerContract pins the dependency-free public install path:
// the README leads with the curl one-liner and `go install`, and install.sh
// verifies the archive against checksums.txt without requiring the GitHub CLI.
func TestPublicInstallerContract(t *testing.T) {
	readme := string(readRepositoryFile(t, "README.md"))
	for _, want := range []string{
		"curl -fsSL https://raw.githubusercontent.com/" + releaseRepository + "/main/install.sh | bash",
		"go install github.com/" + releaseRepository + "/cmd/code-graph@latest",
		"claude mcp add code-graph",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md must document the public install path %q", want)
		}
	}
	for _, unwanted := range []string{"private repository", "not published to the public MCP Registry", "codebase-memory-mcp-"} {
		if strings.Contains(readme, unwanted) {
			t.Errorf("README.md must not describe internal distribution: %q", unwanted)
		}
	}

	installer := string(readRepositoryFile(t, "install.sh"))
	for _, want := range []string{
		`REPO="${CODE_GRAPH_REPO:-` + releaseRepository + `}"`,
		"checksums.txt",
		"sha256sum",
		"shasum -a 256",
		"gh attestation verify",
		"Build provenance not verified",
	} {
		if !strings.Contains(installer, want) {
			t.Errorf("install.sh must contain %q", want)
		}
	}
	for _, unwanted := range []string{"gh release download", "gh auth login"} {
		if strings.Contains(installer, unwanted) {
			t.Errorf("install.sh must not require the GitHub CLI: %q", unwanted)
		}
	}
	checksumAt := strings.Index(installer, "Checksum mismatch")
	extractAt := strings.Index(installer, "tar -xzf")
	if checksumAt < 0 || extractAt < 0 || checksumAt > extractAt {
		t.Errorf("install.sh must verify the checksum before extracting (checksum=%d extract=%d)", checksumAt, extractAt)
	}
}

// TestVerifiedInstallersUseAuthenticatedGitHubCLI pins the fully verified
// install path (scripts/setup.sh, scripts/setup-windows.ps1): release
// membership and provenance are checked through an authenticated GitHub CLI
// and no anonymous download flow is used.
func TestVerifiedInstallersUseAuthenticatedGitHubCLI(t *testing.T) {
	manifestPath := filepath.Join(repositoryRoot(t), "server.json")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("server.json is not maintained in this repository; stat error = %v", err)
	}

	tests := []struct {
		path       string
		repository string
	}{
		{
			path:       "scripts/setup.sh",
			repository: `REPO="` + releaseRepository + `"`,
		},
		{
			path:       "scripts/setup-windows.ps1",
			repository: `$Repo = "` + releaseRepository + `"`,
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
				t.Errorf("%s must install from %s", tt.path, releaseRepository)
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

func TestUnixInstallerHelpDisclosesAuthentication(t *testing.T) {
	script := filepath.Join(repositoryRoot(t), "scripts", "setup.sh")
	output, err := exec.CommandContext(t.Context(), "bash", script, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("setup.sh --help: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "authenticated GitHub CLI") {
		t.Errorf("setup.sh --help must disclose that it needs an authenticated GitHub CLI:\n%s", output)
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
	}{
		{
			name:             "unix",
			path:             "scripts/setup.sh",
			download:         `gh release download "$tag"`,
			releaseVerify:    `gh release verify-asset "$tag" "$archive_path"`,
			provenanceVerify: `gh attestation verify "$archive_path"`,
			extract:          `tar -xzf "$archive_path"`,
		},
		{
			name:             "windows",
			path:             "scripts/setup-windows.ps1",
			download:         `gh release download $tag`,
			releaseVerify:    `gh release verify-asset $tag $tmpZip`,
			provenanceVerify: `gh attestation verify $tmpZip`,
			extract:          `Expand-Archive -Path $tmpZip`,
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

func TestInstallerLineEndingVariantsNormalizeExistingCRLF(t *testing.T) {
	variants := installerLineEndingVariants("first\r\nsecond\r\n")
	want := map[string]string{
		"LF":   "first\nsecond\n",
		"CRLF": "first\r\nsecond\r\n",
	}

	if len(variants) != len(want) {
		t.Fatalf("line-ending variant count = %d, want %d", len(variants), len(want))
	}
	for _, variant := range variants {
		if got, ok := want[variant.name]; !ok {
			t.Errorf("unexpected line-ending variant %q", variant.name)
		} else if variant.content != got {
			t.Errorf("%s content = %q, want %q", variant.name, variant.content, got)
		}
	}
}

type installerLineEndingVariant struct {
	name    string
	content string
}

func installerLineEndingVariants(raw string) []installerLineEndingVariant {
	lf := strings.ReplaceAll(raw, "\r\n", "\n")
	return []installerLineEndingVariant{
		{name: "LF", content: lf},
		{name: "CRLF", content: strings.ReplaceAll(lf, "\n", "\r\n")},
	}
}

func TestUnixInstallerMigratesLegacySourceRemote(t *testing.T) {
	tests := installerLineEndingVariants(string(readRepositoryFile(t, "scripts/setup.sh")))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := strings.ReplaceAll(tt.content, "\r\n", "\n")
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
				{"-C", repo, "remote", "add", "origin", "https://github.com/brandyn-s/code-graph.git"},
			} {
				output, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, output)
				}
			}

			command := function + `
REPO="brandyn-s/code-graph"
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
			if got, want := strings.TrimSpace(string(remote)), "https://github.com/brandyn-s/code-graph.git"; got != want {
				t.Errorf("migrated origin = %q, want %q", got, want)
			}
		})
	}
}
