package tools

import "testing"

func TestTraceConfidenceBand(t *testing.T) {
	// Empirical thresholds (2026-05-05b): high>=0.95, medium>=0.10, low<0.10,
	// speculative=resolved==0 && unresolved>0. Distribution-derived from
	// bench/research/confidence_band_distribution.py over all 11 indexed
	// projects.
	cases := []struct {
		name       string
		resolved   int
		unresolved int
		want       string
	}{
		{"no calls at all", 0, 0, "high"},
		{"all resolved", 10, 0, "high"},
		{"95% resolved", 19, 1, "high"},
		{"high band boundary (95%)", 95, 5, "high"},
		{"just below high (94%)", 94, 6, "medium"},
		{"medium band (60%)", 6, 4, "medium"},
		{"medium band (30%)", 3, 7, "medium"},
		{"medium band boundary (10%)", 1, 9, "medium"},
		{"just below medium (9%)", 9, 91, "low"},
		{"low band (5%)", 5, 95, "low"},
		{"low band (1 of 100)", 1, 99, "low"},
		{"speculative — extractor blind", 0, 5, "speculative"},
		{"speculative — single dispatch site", 0, 1, "speculative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := traceConfidenceBand(c.resolved, c.unresolved)
			if got != c.want {
				t.Errorf("traceConfidenceBand(%d, %d) = %q, want %q",
					c.resolved, c.unresolved, got, c.want)
			}
		})
	}
}
