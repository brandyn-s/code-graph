package tools

import (
	"fmt"
	"testing"
)

func TestBuildUnsupportedTelemetry_Tiers(t *testing.T) {
	tally := map[string]int{
		".kt":   2, // cut language — reported at any count
		".rb":   1, // cut language — count 1 still reported
		".xyz":  5, // unknown, >= 3 → reported
		"noext": 7, // unknown, >= 3 → reported
		".bak":  2, // unknown, < 3 → filtered
	}

	total, cut, unknown := buildUnsupportedTelemetry(tally)

	if total != 17 {
		t.Errorf("total = %d, want 17", total)
	}

	if len(cut) != 2 {
		t.Fatalf("cut = %v, want 2 entries", cut)
	}
	if c := cut[".kt"]; c["count"] != 2 || c["language"] != "kotlin" {
		t.Errorf(`cut[".kt"] = %v, want {count: 2, language: kotlin}`, c)
	}
	if c := cut[".rb"]; c["count"] != 1 || c["language"] != "ruby" {
		t.Errorf(`cut[".rb"] = %v, want {count: 1, language: ruby} — cut tier reports any count`, c)
	}

	// Sorted by count descending; .bak filtered by the >= 3 floor.
	if len(unknown) != 2 {
		t.Fatalf("unknown = %v, want 2 entries", unknown)
	}
	if unknown[0]["extension"] != "noext" || unknown[0]["count"] != 7 {
		t.Errorf("unknown[0] = %v, want {extension: noext, count: 7}", unknown[0])
	}
	if unknown[1]["extension"] != ".xyz" || unknown[1]["count"] != 5 {
		t.Errorf("unknown[1] = %v, want {extension: .xyz, count: 5}", unknown[1])
	}
}

func TestBuildUnsupportedTelemetry_Empty(t *testing.T) {
	for name, tally := range map[string]map[string]int{"nil": nil, "empty": {}} {
		total, cut, unknown := buildUnsupportedTelemetry(tally)
		if total != 0 || cut != nil || unknown != nil {
			t.Errorf("%s tally: got (%d, %v, %v), want (0, nil, nil)", name, total, cut, unknown)
		}
	}
}

func TestBuildUnsupportedTelemetry_Top10Cap(t *testing.T) {
	tally := make(map[string]int)
	for i := 0; i < 12; i++ {
		tally[fmt.Sprintf(".ext%02d", i)] = 3 + i // counts 3..14, all unknown
	}

	total, _, unknown := buildUnsupportedTelemetry(tally)

	if want := (3 + 14) * 12 / 2; total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
	if len(unknown) != 10 {
		t.Fatalf("unknown capped at 10, got %d", len(unknown))
	}
	if unknown[0]["count"] != 14 {
		t.Errorf("unknown[0].count = %v, want 14 (highest first)", unknown[0]["count"])
	}
	if unknown[9]["count"] != 5 {
		t.Errorf("unknown[9].count = %v, want 5 (counts 3 and 4 dropped by the cap)", unknown[9]["count"])
	}
}
