package main

import (
	"reflect"
	"testing"
)

func TestSplitOperandAcceptsFileBeforeOrAfterFlags(t *testing.T) {
	valueFlags := map[string]bool{"-repo": true, "--repo": true}
	cases := []struct {
		args     []string
		wantFile string
		wantRest []string
	}{
		{[]string{"a.cgraph.zst", "--repo", "/x", "--allow-stale"}, "a.cgraph.zst", []string{"--repo", "/x", "--allow-stale"}},
		{[]string{"--repo", "/x", "a.cgraph.zst", "--json"}, "a.cgraph.zst", []string{"--repo", "/x", "--json"}},
		{[]string{"--repo=/x", "--force", "a.cgraph.zst"}, "a.cgraph.zst", []string{"--repo=/x", "--force"}},
		{[]string{"--allow-stale"}, "", []string{"--allow-stale"}},
		{[]string{"a", "b"}, "a", []string{"b"}},
	}
	for _, tc := range cases {
		file, rest := splitOperand(tc.args, valueFlags)
		if file != tc.wantFile || !reflect.DeepEqual(rest, tc.wantRest) {
			t.Errorf("splitOperand(%v) = %q, %v; want %q, %v", tc.args, file, rest, tc.wantFile, tc.wantRest)
		}
	}
}
