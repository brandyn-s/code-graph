package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ReleaseURL is the GitHub API endpoint for latest release. Exported for test injection.
//
// Points at this fork, NOT upstream DeusData — `code-graph update` replaces
// the running binary with whatever this URL serves, and an upstream binary
// would silently drop every fork addition (security tools, resolver gates,
// SCIP ingest). See tools.releaseURL for the matching update-notice endpoint.
var ReleaseURL = "https://api.github.com/repos/brandyn-s/code-graph/releases/latest"

const (
	privateRepository       = "brandyn-s/code-graph"
	slsaProvenancePredicate = "https://slsa.dev/provenance/v1"
)

var safeReleaseComponent = regexp.MustCompile(
	`^[A-Za-z0-9][A-Za-z0-9._+-]{0,255}$`,
)

var runGitHubCLI = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	if exitError, ok := err.(*exec.ExitError); ok {
		detail := strings.TrimSpace(string(exitError.Stderr))
		if detail != "" {
			return nil, fmt.Errorf("%w: %s", err, detail)
		}
	}
	return nil, err
}

// Release holds parsed GitHub release metadata.
type Release struct {
	TagName    string  `json:"tag_name"`
	Assets     []Asset `json:"assets"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
}

// Asset holds a single release artifact.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// FetchLatestRelease fetches release metadata from GitHub.
func FetchLatestRelease(ctx context.Context) (*Release, error) {
	return FetchRelease(ctx, ReleaseURL)
}

// ReleaseListURL lists recent releases including prereleases. Exported for
// test injection. Used only on the "rc" update channel.
var ReleaseListURL = "https://api.github.com/repos/brandyn-s/code-graph/releases?per_page=20"

// Update channels. GitHub's /releases/latest already excludes prereleases, so
// the stable channel needs no filtering; the rc channel scans the release list
// and accepts release candidates.
const (
	ChannelStable = "stable"
	ChannelRC     = "rc"
)

// NormalizeChannel maps a raw CODE_GRAPH_UPDATE_CHANNEL value to a channel.
// Unknown values fall back to stable so a typo never opts into prereleases.
func NormalizeChannel(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), ChannelRC) {
		return ChannelRC
	}
	return ChannelStable
}

// FetchNewestRelease returns the newest release visible on the channel:
// GitHub's latest non-prerelease on stable, or the highest version among
// published (non-draft) releases including release candidates on rc.
func FetchNewestRelease(ctx context.Context, channel string) (*Release, error) {
	if NormalizeChannel(channel) != ChannelRC {
		return FetchLatestRelease(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, err := fetchReleaseHTTP(ctx, ReleaseListURL)
	if err != nil {
		return nil, err
	}
	var releases []Release
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	var newest *Release
	for i := range releases {
		r := &releases[i]
		if r.Draft || r.TagName == "" {
			continue
		}
		if newest == nil || CompareVersions(r.TagName, newest.TagName) > 0 {
			newest = r
		}
	}
	if newest == nil {
		return nil, fmt.Errorf("no published releases found on the %s channel", ChannelRC)
	}
	return newest, nil
}

// FetchRelease fetches release metadata, authenticating access
// through GitHub CLI while retaining HTTP injection for generic/public URLs.
func FetchRelease(ctx context.Context, rawURL string) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var body []byte
	if isPrivateReleaseURL(rawURL) {
		var err error
		body, err = runGitHubCLI(
			ctx,
			"api",
			"repos/"+privateRepository+"/releases/latest",
			"--header",
			"Accept: application/vnd.github+json",
		)
		if err != nil {
			return nil, fmt.Errorf(
				"authenticated GitHub CLI release metadata failed "+
					"(run `gh auth login --hostname github.com`): %w",
				err,
			)
		}
		if len(body) > 1<<20 {
			return nil, fmt.Errorf("release metadata exceeds 1 MiB")
		}
	} else {
		var err error
		body, err = fetchReleaseHTTP(ctx, rawURL)
		if err != nil {
			return nil, err
		}
	}

	var release Release
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	return &release, nil
}

func fetchReleaseHTTP(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github api status=%d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

// LatestVersion returns the version string from the latest release (without "v" prefix).
func (r *Release) LatestVersion() string {
	return strings.TrimPrefix(r.TagName, "v")
}

// FindAsset finds a release asset by name.
func (r *Release) FindAsset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// AssetName returns the expected release asset name for the current platform.
func AssetName() string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("code-graph-%s-%s.zip", runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("code-graph-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

// CompareVersions compares two semver strings (e.g. "0.2.1" vs "0.2.0").
// Returns >0 if a > b, <0 if a < b, 0 if equal.
func CompareVersions(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")

	aVersion := strings.SplitN(a, "-", 2)
	bVersion := strings.SplitN(b, "-", 2)
	aBase := aVersion[0]
	bBase := bVersion[0]

	aParts := strings.Split(aBase, ".")
	bParts := strings.Split(bBase, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		ai, _ := strconv.Atoi(aParts[i])
		bi, _ := strconv.Atoi(bParts[i])
		if ai != bi {
			return ai - bi
		}
	}
	if len(aParts) != len(bParts) {
		return len(aParts) - len(bParts)
	}

	// Same base version — non-dev beats dev (e.g. "0.2.1" > "0.2.1-dev")
	aHasPre := len(aVersion) == 2
	bHasPre := len(bVersion) == 2
	if aHasPre && !bHasPre {
		return -1
	}
	if !aHasPre && bHasPre {
		return 1
	}
	if aHasPre && bHasPre {
		return comparePrerelease(aVersion[1], bVersion[1])
	}
	return 0
}

// comparePrerelease orders dot-separated pre-release identifiers per semver:
// numeric identifiers compare numerically, others lexically, and a longer
// identifier list wins when all shared identifiers are equal. This also orders
// the legacy pre-public scheme (vX.Y.Z-<word>.N) by N; any plain vX.Y.Z release
// already compares newer than every pre-release of the same base.
func comparePrerelease(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		ai, aErr := strconv.Atoi(aParts[i])
		bi, bErr := strconv.Atoi(bParts[i])
		switch {
		case aErr == nil && bErr == nil:
			if ai != bi {
				return ai - bi
			}
		case aErr == nil:
			return -1 // numeric identifiers sort before alphanumeric ones
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(aParts[i], bParts[i]); c != 0 {
				return c
			}
		}
	}
	return len(aParts) - len(bParts)
}

// AllowedDownloadPrefixes controls which URL prefixes are accepted by DownloadAsset.
// Exported for test injection only.
var AllowedDownloadPrefixes = []string{
	"https://github.com/",
	"https://api.github.com/",
}

// DownloadAsset downloads a release asset and returns the full body as bytes.
// The response body is fully read before returning to avoid premature context cancellation.
func DownloadAsset(ctx context.Context, rawURL string) ([]byte, error) {
	allowed := false
	for _, prefix := range AllowedDownloadPrefixes {
		if strings.HasPrefix(rawURL, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("refusing to download from non-GitHub URL: %s", rawURL)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if isPrivateRepositoryURL(rawURL) {
		tag, assetName, err := privateReleaseAsset(rawURL)
		if err != nil {
			return nil, err
		}
		data, err := runGitHubCLI(
			ctx,
			"release",
			"download",
			tag,
			"--repo",
			privateRepository,
			"--pattern",
			assetName,
			"--output",
			"-",
		)
		if err != nil {
			return nil, fmt.Errorf(
				"authenticated GitHub CLI asset download failed "+
					"(run `gh auth login --hostname github.com`): %w",
				err,
			)
		}
		if len(data) > 500<<20 {
			return nil, fmt.Errorf("download exceeds 500 MiB")
		}
		return data, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download status=%d", resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, 500<<20)) // 500 MB safety limit
}

// VerifyReleaseAsset proves that archive is the named immutable release asset
// and, for releases that publish provenance, that it has a valid SLSA
// attestation. Verification is performed against a private temporary copy so
// the exact downloaded bytes are checked before extraction or replacement.
func VerifyReleaseAsset(
	ctx context.Context,
	releaseTag string,
	assetName string,
	archive []byte,
) error {
	if !safeReleaseComponent.MatchString(releaseTag) {
		return fmt.Errorf("invalid release tag %q", releaseTag)
	}
	if !safeReleaseComponent.MatchString(assetName) ||
		filepath.Base(assetName) != assetName ||
		strings.ContainsAny(assetName, `/\`) {
		return fmt.Errorf("invalid release asset basename %q", assetName)
	}

	tempDir, err := os.MkdirTemp("", "code-graph-update-*")
	if err != nil {
		return fmt.Errorf("create private verification directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return fmt.Errorf("secure verification directory: %w", err)
	}

	archivePath := filepath.Join(tempDir, assetName)
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		return fmt.Errorf("write verification archive: %w", err)
	}
	if err := os.Chmod(archivePath, 0o600); err != nil {
		return fmt.Errorf("secure verification archive: %w", err)
	}

	verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := runGitHubCLI(
		verifyCtx,
		"release",
		"verify-asset",
		releaseTag,
		archivePath,
		"--repo",
		privateRepository,
	); err != nil {
		return fmt.Errorf(
			"authenticated release asset membership verification failed: %w",
			err,
		)
	}

	if _, err := runGitHubCLI(
		verifyCtx,
		"attestation",
		"verify",
		archivePath,
		"--repo",
		privateRepository,
		"--predicate-type",
		slsaProvenancePredicate,
	); err != nil {
		return fmt.Errorf(
			"authenticated release asset provenance verification failed: %w",
			err,
		)
	}
	return nil
}

func isPrivateReleaseURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "api.github.com") {
		return false
	}
	return strings.Trim(parsed.Path, "/") ==
		"repos/"+privateRepository+"/releases/latest"
}

func isPrivateRepositoryURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	path := strings.Trim(parsed.Path, "/")
	switch {
	case strings.EqualFold(parsed.Hostname(), "github.com"):
		return path == privateRepository ||
			strings.HasPrefix(path, privateRepository+"/")
	case strings.EqualFold(parsed.Hostname(), "api.github.com"):
		return path == "repos/"+privateRepository ||
			strings.HasPrefix(path, "repos/"+privateRepository+"/")
	default:
		return false
	}
}

func privateReleaseAsset(rawURL string) (tag, assetName string, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("parse private release asset URL: %w", err)
	}
	escapedParts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if !strings.EqualFold(parsed.Hostname(), "github.com") ||
		len(escapedParts) != 6 ||
		escapedParts[0] != "brandyn-s" ||
		escapedParts[1] != "code-graph" ||
		escapedParts[2] != "releases" ||
		escapedParts[3] != "download" {
		return "", "", fmt.Errorf(
			"refusing unsupported private release asset URL: %s",
			rawURL,
		)
	}
	tag, err = url.PathUnescape(escapedParts[4])
	if err != nil || tag == "" {
		return "", "", fmt.Errorf("invalid private release tag in URL")
	}
	assetName, err = url.PathUnescape(escapedParts[5])
	if err != nil || assetName == "" {
		return "", "", fmt.Errorf("invalid private release asset name in URL")
	}
	return tag, assetName, nil
}

// DownloadChecksums downloads and parses the checksums.txt file from a release.
// Returns a map of filename → hex-encoded SHA-256 hash.
func DownloadChecksums(ctx context.Context, release *Release) (map[string]string, error) {
	asset := release.FindAsset("checksums.txt")
	if asset == nil {
		return nil, fmt.Errorf("checksums.txt not found in release")
	}

	data, err := DownloadAsset(ctx, asset.BrowserDownloadURL)
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}

	checksums := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			checksums[parts[1]] = parts[0]
		}
	}
	return checksums, nil
}
