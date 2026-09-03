package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func withGitHubCLIRunner(
	t *testing.T,
	runner func(context.Context, ...string) ([]byte, error),
) {
	t.Helper()
	original := runGitHubCLI
	runGitHubCLI = runner
	t.Cleanup(func() { runGitHubCLI = original })
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int // >0, <0, or 0
	}{
		{"0.2.1", "0.2.0", 1},
		{"0.2.0", "0.2.0", 0},
		{"0.1.9", "0.2.0", -1},
		{"0.10.0", "0.2.0", 1},
		{"1.0.0", "0.99.99", 1},
		{"0.0.1", "0.0.2", -1},
		{"v0.2.1", "0.2.1", 0},
		{"0.2.1", "v0.2.1", 0},
		{"0.2.1-dev", "0.2.1", -1},
		{"0.2.1", "0.2.1-dev", 1},
		{"0.2.1-dev", "0.2.1-dev", 0},
		{"0.3.0", "0.2.1-dev", 1},
		{"0.2.0", "0.2.1-dev", -1},
		{"0.7.0-redacted.2", "0.7.0-redacted.1", 1},
		{"0.7.0-redacted.10", "0.7.0-redacted.2", 1},
		{"0.7.0-redacted.1", "0.7.0-redacted.1", 0},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.a, tt.b), func(t *testing.T) {
			got := CompareVersions(tt.a, tt.b)
			switch {
			case tt.want > 0 && got <= 0:
				t.Fatalf("CompareVersions(%q, %q) = %d, want > 0", tt.a, tt.b, got)
			case tt.want < 0 && got >= 0:
				t.Fatalf("CompareVersions(%q, %q) = %d, want < 0", tt.a, tt.b, got)
			case tt.want == 0 && got != 0:
				t.Fatalf("CompareVersions(%q, %q) = %d, want 0", tt.a, tt.b, got)
			}
		})
	}
}

func TestAssetName(t *testing.T) {
	name := AssetName()
	expected := fmt.Sprintf("codebase-memory-mcp-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		expected += ".zip"
	} else {
		expected += ".tar.gz"
	}
	if name != expected {
		t.Fatalf("AssetName() = %q, want %q", name, expected)
	}
}

func TestFetchLatestRelease(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"tag_name": "v1.0.0",
			"assets": [
				{"name": "codebase-memory-mcp-linux-amd64.tar.gz", "browser_download_url": "https://example.com/linux.tar.gz", "size": 1024},
				{"name": "checksums.txt", "browser_download_url": "https://example.com/checksums.txt", "size": 256}
			]
		}`)
	}))
	defer ts.Close()

	old := ReleaseURL
	ReleaseURL = ts.URL
	defer func() { ReleaseURL = old }()

	release, err := FetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("FetchLatestRelease() error: %v", err)
	}

	if release.LatestVersion() != "1.0.0" {
		t.Fatalf("LatestVersion() = %q, want %q", release.LatestVersion(), "1.0.0")
	}

	if len(release.Assets) != 2 {
		t.Fatalf("expected 2 assets, got %d", len(release.Assets))
	}
}

func TestFetchLatestRelease_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	old := ReleaseURL
	ReleaseURL = ts.URL
	defer func() { ReleaseURL = old }()

	_, err := FetchLatestRelease(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestFetchRelease_PrivateMetadataUsesAuthenticatedGitHubCLI(t *testing.T) {
	var gotArgs []string
	withGitHubCLIRunner(t, func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(`{"tag_name":"v1.2.3","assets":[]}`), nil
	})

	release, err := FetchRelease(context.Background(), ReleaseURL)
	if err != nil {
		t.Fatalf("FetchRelease() error: %v", err)
	}
	if release.TagName != "v1.2.3" {
		t.Fatalf("TagName = %q, want v1.2.3", release.TagName)
	}
	wantArgs := []string{
		"api",
		"repos/brandyn-s/code-graph/releases/latest",
		"--header",
		"Accept: application/vnd.github+json",
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("gh args = %q, want %q", gotArgs, wantArgs)
	}
}

func TestFetchRelease_PrivateMetadataFailureHasNoAnonymousFallback(t *testing.T) {
	withGitHubCLIRunner(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("gh authentication required")
	})
	var anonymousCalls atomic.Int32
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(
		func(*http.Request) (*http.Response, error) {
			anonymousCalls.Add(1)
			return nil, errors.New("anonymous HTTP must not run")
		},
	)}
	t.Cleanup(func() { http.DefaultClient = originalClient })

	_, err := FetchRelease(context.Background(), ReleaseURL)

	if err == nil || !strings.Contains(err.Error(), "authenticated GitHub CLI") {
		t.Fatalf("FetchRelease() error = %v, want authenticated gh error", err)
	}
	if got := anonymousCalls.Load(); got != 0 {
		t.Fatalf("anonymous HTTP calls = %d, want 0", got)
	}
}

func TestDownloadAsset_PrivateAssetUsesAuthenticatedGitHubCLI(t *testing.T) {
	var gotArgs []string
	withGitHubCLIRunner(t, func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("private release bytes"), nil
	})
	rawURL := "https://github.com/brandyn-s/code-graph/releases/" +
		"download/v1.2.3/codebase-memory-mcp-linux-amd64.tar.gz"

	data, err := DownloadAsset(context.Background(), rawURL)

	if err != nil {
		t.Fatalf("DownloadAsset() error: %v", err)
	}
	if string(data) != "private release bytes" {
		t.Fatalf("DownloadAsset() = %q", data)
	}
	wantArgs := []string{
		"release",
		"download",
		"v1.2.3",
		"--repo",
		"brandyn-s/code-graph",
		"--pattern",
		"codebase-memory-mcp-linux-amd64.tar.gz",
		"--output",
		"-",
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("gh args = %q, want %q", gotArgs, wantArgs)
	}
}

func TestDownloadAsset_PrivateAssetFailureHasNoAnonymousFallback(t *testing.T) {
	withGitHubCLIRunner(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("gh release download failed")
	})
	var anonymousCalls atomic.Int32
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(
		func(*http.Request) (*http.Response, error) {
			anonymousCalls.Add(1)
			return nil, errors.New("anonymous HTTP must not run")
		},
	)}
	t.Cleanup(func() { http.DefaultClient = originalClient })
	rawURL := "https://github.com/brandyn-s/code-graph/releases/" +
		"download/v1.2.3/checksums.txt"

	_, err := DownloadAsset(context.Background(), rawURL)

	if err == nil || !strings.Contains(err.Error(), "authenticated GitHub CLI") {
		t.Fatalf("DownloadAsset() error = %v, want authenticated gh error", err)
	}
	if got := anonymousCalls.Load(); got != 0 {
		t.Fatalf("anonymous HTTP calls = %d, want 0", got)
	}
}

func TestVerifyReleaseAsset_VerifiesMembershipBeforeSLSA(t *testing.T) {
	archive := []byte("authenticated release archive")
	const assetName = "codebase-memory-mcp-linux-amd64.tar.gz"

	var calls [][]string
	var verificationPath string
	withGitHubCLIRunner(t, func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))

		var path string
		switch args[0] {
		case "release":
			path = args[3]
		case "attestation":
			path = args[2]
		default:
			t.Fatalf("unexpected gh command: %q", args)
		}
		if verificationPath == "" {
			verificationPath = path
		} else if path != verificationPath {
			t.Fatalf("verification path changed: %q then %q", verificationPath, path)
		}
		if filepath.Base(path) != assetName {
			t.Fatalf("verification filename = %q, want %q", filepath.Base(path), assetName)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read verification archive: %v", err)
		}
		if !slices.Equal(got, archive) {
			t.Fatalf("verification archive = %q, want %q", got, archive)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat verification archive: %v", err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("verification archive mode = %#o, want 0600", got)
			}
			dirInfo, err := os.Stat(filepath.Dir(path))
			if err != nil {
				t.Fatalf("stat verification directory: %v", err)
			}
			if got := dirInfo.Mode().Perm(); got != 0o700 {
				t.Fatalf("verification directory mode = %#o, want 0700", got)
			}
		}
		return nil, nil
	})

	err := VerifyReleaseAsset(context.Background(), "v1.2.3", assetName, archive)
	if err != nil {
		t.Fatalf("VerifyReleaseAsset() error: %v", err)
	}

	wantCalls := [][]string{
		{
			"release",
			"verify-asset",
			"v1.2.3",
			verificationPath,
			"--repo",
			"brandyn-s/code-graph",
		},
		{
			"attestation",
			"verify",
			verificationPath,
			"--repo",
			"brandyn-s/code-graph",
			"--predicate-type",
			"https://slsa.dev/provenance/v1",
		},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("gh calls = %q, want %q", calls, wantCalls)
	}
	for i := range wantCalls {
		if !slices.Equal(calls[i], wantCalls[i]) {
			t.Fatalf("gh call %d = %q, want %q", i, calls[i], wantCalls[i])
		}
	}
	if _, err := os.Stat(verificationPath); !os.IsNotExist(err) {
		t.Fatalf("verification archive was not removed: %v", err)
	}
}

func TestVerifyReleaseAsset_HistoricalTagStillRequiresMembership(t *testing.T) {
	const historicalTag = "v0.7.0-redacted.2"
	const assetName = "codebase-memory-mcp-linux-amd64.tar.gz"

	t.Run("membership succeeds without SLSA attestation", func(t *testing.T) {
		var calls [][]string
		withGitHubCLIRunner(t, func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			return nil, nil
		})

		err := VerifyReleaseAsset(
			context.Background(),
			historicalTag,
			assetName,
			[]byte("historical archive"),
		)
		if err != nil {
			t.Fatalf("VerifyReleaseAsset() error: %v", err)
		}
		if len(calls) != 1 {
			t.Fatalf("gh calls = %q, want one membership verification", calls)
		}
		if got := calls[0][:3]; !slices.Equal(
			got,
			[]string{"release", "verify-asset", historicalTag},
		) {
			t.Fatalf("gh call = %q, want release membership verification", calls[0])
		}
	})

	t.Run("membership failure aborts historical update", func(t *testing.T) {
		var calls int
		withGitHubCLIRunner(t, func(_ context.Context, _ ...string) ([]byte, error) {
			calls++
			return nil, errors.New("asset is not in the release")
		})

		err := VerifyReleaseAsset(
			context.Background(),
			historicalTag,
			assetName,
			[]byte("untrusted archive"),
		)
		if err == nil || !strings.Contains(err.Error(), "membership") {
			t.Fatalf("VerifyReleaseAsset() error = %v, want membership failure", err)
		}
		if calls != 1 {
			t.Fatalf("gh calls = %d, want exactly one membership attempt", calls)
		}
	})
}

func TestVerifyReleaseAsset_RejectsInvalidTagOrAssetBasename(t *testing.T) {
	tests := []struct {
		name      string
		tag       string
		assetName string
	}{
		{name: "empty tag", tag: "", assetName: "asset.tar.gz"},
		{name: "option-like tag", tag: "--repo", assetName: "asset.tar.gz"},
		{name: "tag path", tag: "v1/evil", assetName: "asset.tar.gz"},
		{name: "tag whitespace", tag: "v1 evil", assetName: "asset.tar.gz"},
		{name: "empty asset", tag: "v1.2.3", assetName: ""},
		{name: "parent asset", tag: "v1.2.3", assetName: "../asset.tar.gz"},
		{name: "slash asset", tag: "v1.2.3", assetName: "dir/asset.tar.gz"},
		{name: "backslash asset", tag: "v1.2.3", assetName: `dir\asset.tar.gz`},
		{name: "dot asset", tag: "v1.2.3", assetName: "."},
		{name: "dot-dot asset", tag: "v1.2.3", assetName: ".."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			withGitHubCLIRunner(t, func(_ context.Context, _ ...string) ([]byte, error) {
				calls++
				return nil, nil
			})

			err := VerifyReleaseAsset(
				context.Background(),
				tt.tag,
				tt.assetName,
				[]byte("archive"),
			)
			if err == nil {
				t.Fatal("VerifyReleaseAsset() error = nil, want validation failure")
			}
			if calls != 0 {
				t.Fatalf("gh calls = %d, want 0 for invalid input", calls)
			}
		})
	}
}

func TestRelease_FindAsset(t *testing.T) {
	release := &Release{
		Assets: []Asset{
			{Name: "file-a.tar.gz"},
			{Name: "file-b.tar.gz"},
			{Name: "checksums.txt"},
		},
	}

	if a := release.FindAsset("file-b.tar.gz"); a == nil {
		t.Fatal("expected to find file-b.tar.gz")
	}
	if a := release.FindAsset("nonexistent"); a != nil {
		t.Fatal("expected nil for nonexistent asset")
	}
}

func TestDownloadChecksums(t *testing.T) {
	checksumServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "abc123  file-a.tar.gz\ndef456  file-b.tar.gz\n")
	}))
	defer checksumServer.Close()

	// Allow local test server URLs
	orig := AllowedDownloadPrefixes
	AllowedDownloadPrefixes = append(AllowedDownloadPrefixes, checksumServer.URL)
	t.Cleanup(func() { AllowedDownloadPrefixes = orig })

	release := &Release{
		Assets: []Asset{
			{Name: "checksums.txt", BrowserDownloadURL: checksumServer.URL},
		},
	}

	checksums, err := DownloadChecksums(context.Background(), release)
	if err != nil {
		t.Fatalf("DownloadChecksums() error: %v", err)
	}

	if checksums["file-a.tar.gz"] != "abc123" {
		t.Fatalf("expected abc123 for file-a.tar.gz, got %q", checksums["file-a.tar.gz"])
	}
	if checksums["file-b.tar.gz"] != "def456" {
		t.Fatalf("expected def456 for file-b.tar.gz, got %q", checksums["file-b.tar.gz"])
	}
}

func TestDownloadChecksums_NoChecksumsFile(t *testing.T) {
	release := &Release{
		Assets: []Asset{
			{Name: "file-a.tar.gz"},
		},
	}

	_, err := DownloadChecksums(context.Background(), release)
	if err == nil {
		t.Fatal("expected error when checksums.txt is missing")
	}
}
