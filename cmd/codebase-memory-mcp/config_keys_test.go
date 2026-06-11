package main

// CLI config-key validation for the report.skip.<project> dynamic class
// (sticky skip_report preference, 2026-06-11). The first seeding attempt
// failed because isKnownConfigKey only accepted the three static keys.

import "testing"

func TestIsKnownConfigKeyReportSkipClass(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"auto_index", true},
		{"report.skip.Users-x-Documents-GitHub-code-search", true},
		{"report.skip.p", true},
		{"report.skip.", false}, // bare prefix names no project
		{"report.skipX", false},
		{"report", false},
		{"totally.unknown", false},
	}
	for _, c := range cases {
		if got := isKnownConfigKey(c.key); got != c.want {
			t.Errorf("isKnownConfigKey(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestNormalizeBoolValue(t *testing.T) {
	for _, in := range []string{"true", "TRUE", "on", "1"} {
		if v, ok := normalizeBoolValue(in); !ok || v != "true" {
			t.Errorf("normalizeBoolValue(%q) = (%q, %v), want (true, true)", in, v, ok)
		}
	}
	for _, in := range []string{"false", "Off", "0"} {
		if v, ok := normalizeBoolValue(in); !ok || v != "false" {
			t.Errorf("normalizeBoolValue(%q) = (%q, %v), want (false, true)", in, v, ok)
		}
	}
	if _, ok := normalizeBoolValue("maybe"); ok {
		t.Error("normalizeBoolValue(\"maybe\") accepted")
	}
}
