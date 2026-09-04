package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeChannel(t *testing.T) {
	for raw, want := range map[string]string{
		"": ChannelStable, "stable": ChannelStable, "rc": ChannelRC, " RC ": ChannelRC,
		"beta": ChannelStable, "nightly": ChannelStable,
	} {
		if got := NormalizeChannel(raw); got != want {
			t.Errorf("NormalizeChannel(%q) = %q, want %q", raw, got, want)
		}
	}
}

// The stable channel asks GitHub for /releases/latest, which never returns a
// prerelease; the rc channel scans the list and picks the highest version
// including release candidates, skipping drafts.
func TestFetchNewestRelease_ChannelSelection(t *testing.T) {
	latest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.9.0","assets":[]}`))
	}))
	defer latest.Close()
	list := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.9.1-rc.1","prerelease":true,"draft":false,"assets":[]},
			{"tag_name":"v0.9.2","prerelease":false,"draft":true,"assets":[]},
			{"tag_name":"v0.9.0","prerelease":false,"draft":false,"assets":[]},
			{"tag_name":"v0.9.1-rc.2","prerelease":true,"draft":false,"assets":[]}
		]`))
	}))
	defer list.Close()

	oldLatest, oldList := ReleaseURL, ReleaseListURL
	ReleaseURL, ReleaseListURL = latest.URL, list.URL
	defer func() { ReleaseURL, ReleaseListURL = oldLatest, oldList }()

	stable, err := FetchNewestRelease(context.Background(), ChannelStable)
	if err != nil {
		t.Fatalf("stable: %v", err)
	}
	if stable.TagName != "v0.9.0" {
		t.Errorf("stable picked %q, want v0.9.0", stable.TagName)
	}

	rc, err := FetchNewestRelease(context.Background(), ChannelRC)
	if err != nil {
		t.Fatalf("rc: %v", err)
	}
	if rc.TagName != "v0.9.1-rc.2" {
		t.Errorf("rc picked %q, want v0.9.1-rc.2 (drafts skipped, rc.2 > rc.1)", rc.TagName)
	}
	if CompareVersions("0.9.1-rc.2", "0.9.1") >= 0 {
		t.Error("a release candidate must sort below its release")
	}
}
