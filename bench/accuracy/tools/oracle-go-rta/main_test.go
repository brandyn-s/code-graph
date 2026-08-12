package main

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestSyntheticFixtureMatchesHandEnumeratedCoordinates(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "synthetic", "go-minimal"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := buildOracle(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"helpers/helpers.go:3>helpers/helpers.go:7": true,
		"main.go:9>helpers/helpers.go:3":            true,
		"main.go:9>main.go:14":                      true,
		"main.go:14>main.go:18":                     true,
		"main.go:20>main.go:9":                      true,
	}
	got := make(map[string]bool, len(output.Edges))
	for _, edge := range output.Edges {
		if edge.Dynamic {
			t.Fatalf("synthetic fixture unexpectedly emitted dynamic edge: %+v", edge)
		}
		key := fmt.Sprintf(
			"%s:%d>%s:%d",
			edge.Caller.File,
			edge.Caller.Line,
			edge.Callee.File,
			edge.Callee.Line,
		)
		got[key] = true
	}
	if len(got) != len(want) {
		t.Fatalf("edge count = %d, want %d: %#v", len(got), len(want), got)
	}
	for key := range want {
		if !got[key] {
			t.Errorf("missing hand-enumerated edge %s", key)
		}
	}
}
