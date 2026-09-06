package main

import "testing"

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name, stamped, module, want string
	}{
		{"stamp wins", "0.9.2", "v0.9.1", "0.9.2"},
		{"go install module version", unstampedVersion, "v0.9.2", "0.9.2"},
		{"pseudo-version kept", unstampedVersion, "v0.9.3-0.20260905120000-abcdef123456", "0.9.3-0.20260905120000-abcdef123456"},
		{"source build stays default", unstampedVersion, "(devel)", unstampedVersion},
		{"no build info stays default", unstampedVersion, "", unstampedVersion},
	}
	for _, tc := range cases {
		if got := resolveVersion(tc.stamped, tc.module); got != tc.want {
			t.Errorf("%s: resolveVersion(%q, %q) = %q, want %q", tc.name, tc.stamped, tc.module, got, tc.want)
		}
	}
}
