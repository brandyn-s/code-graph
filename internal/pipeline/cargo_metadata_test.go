package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseCargoMetadataSimple verifies the single-crate case. A crate
// `my_app` with crates.io deps {serde, tokio, anyhow, serde-json}
// should produce ExternalCrates = {serde, tokio, anyhow, serde_json}
// (note normalization of serde-json → serde_json) and
// WorkspaceMembers = {my_app}.
func TestParseCargoMetadataSimple(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "cargo-metadata-simple.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	res, err := parseCargoMetadata(raw)
	if err != nil {
		t.Fatalf("parseCargoMetadata: %v", err)
	}

	wantExternal := []string{"serde", "tokio", "anyhow", "serde_json"}
	for _, c := range wantExternal {
		if !res.ExternalCrates[c] {
			t.Errorf("expected %q in ExternalCrates, got %v", c, keys(res.ExternalCrates))
		}
	}
	if len(res.ExternalCrates) != len(wantExternal) {
		t.Errorf("ExternalCrates size: got %d, want %d (%v)",
			len(res.ExternalCrates), len(wantExternal), keys(res.ExternalCrates))
	}

	if !res.WorkspaceMembers["my_app"] {
		t.Errorf("expected my_app in WorkspaceMembers, got %v", keys(res.WorkspaceMembers))
	}
	// Externals must NOT contain own-crate name
	if res.ExternalCrates["my_app"] {
		t.Errorf("my_app is an own crate but appeared in ExternalCrates")
	}
}

// TestParseCargoMetadataWorkspace verifies workspace deduplication.
// A workspace with members {service-a, service-b, service-c} where
// service-a depends on (service-b path dep, tokio external) must:
//   - classify tokio as external
//   - classify service-b, service-c as workspace members
//   - NOT classify service-b as external (path dep, no source)
//
// Names normalize via `-` → `_`: service-a → service_a, etc.
func TestParseCargoMetadataWorkspace(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "cargo-metadata-workspace.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	res, err := parseCargoMetadata(raw)
	if err != nil {
		t.Fatalf("parseCargoMetadata: %v", err)
	}

	for _, m := range []string{"service_a", "service_b", "service_c"} {
		if !res.WorkspaceMembers[m] {
			t.Errorf("expected %q in WorkspaceMembers, got %v", m, keys(res.WorkspaceMembers))
		}
	}
	if !res.ExternalCrates["tokio"] {
		t.Errorf("expected tokio in ExternalCrates")
	}
	if res.ExternalCrates["service_b"] {
		t.Errorf("service_b is a workspace member but appeared in ExternalCrates")
	}
	if res.ExternalCrates["service_c"] {
		t.Errorf("service_c is a workspace member but appeared in ExternalCrates")
	}
	if len(res.ExternalCrates) != 1 {
		t.Errorf("ExternalCrates size: got %d, want 1 (only tokio); have %v",
			len(res.ExternalCrates), keys(res.ExternalCrates))
	}
}

// TestParseCargoMetadataMalformed verifies error on invalid JSON.
// Graceful failure mode is critical — populateCargoMetadata logs and
// continues with nil sets on parse error.
func TestParseCargoMetadataMalformed(t *testing.T) {
	_, err := parseCargoMetadata([]byte("not json {"))
	if err == nil {
		t.Error("expected error on malformed JSON, got nil")
	}
}

// TestNormalizeCargoCrateName pins the `-` → `_` convention.
func TestNormalizeCargoCrateName(t *testing.T) {
	cases := map[string]string{
		"serde":          "serde",
		"serde_json":     "serde_json",
		"serde-json":     "serde_json",
		"futures-util":   "futures_util",
		"tracing":        "tracing",
		"a-b-c":          "a_b_c",
	}
	for in, want := range cases {
		if got := normalizeCargoCrateName(in); got != want {
			t.Errorf("normalizeCargoCrateName(%q) = %q, want %q", in, got, want)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
