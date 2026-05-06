package tools

import (
	"sort"
	"testing"
)

// TestMetadataCoverage_ListsAreCoherent verifies that:
//  1. instrumentedTools, pendingTools, and excludedTools are mutually
//     disjoint.
//  2. Every entry in allMCPToolNames appears in exactly one of the
//     three lists.
//  3. Every entry in instrumented/pending/excluded appears in
//     allMCPToolNames (no orphans).
//
// This catches the "added a new tool but forgot to categorize it"
// failure mode at CI time.
func TestMetadataCoverage_ListsAreCoherent(t *testing.T) {
	all := make(map[string]bool, len(allMCPToolNames))
	for _, name := range allMCPToolNames {
		all[name] = true
	}

	instrumented := make(map[string]bool, len(instrumentedTools))
	for _, name := range instrumentedTools {
		if instrumented[name] {
			t.Errorf("instrumentedTools contains duplicate: %s", name)
		}
		instrumented[name] = true
	}

	// Check disjointness across the three sets.
	for name := range instrumented {
		if _, ok := pendingTools[name]; ok {
			t.Errorf("tool %q in BOTH instrumentedTools and pendingTools", name)
		}
		if _, ok := excludedTools[name]; ok {
			t.Errorf("tool %q in BOTH instrumentedTools and excludedTools", name)
		}
	}
	for name := range pendingTools {
		if _, ok := excludedTools[name]; ok {
			t.Errorf("tool %q in BOTH pendingTools and excludedTools", name)
		}
	}

	// Check completeness: every name in allMCPToolNames appears
	// in exactly one of the three sets.
	for name := range all {
		hits := 0
		if instrumented[name] {
			hits++
		}
		if _, ok := pendingTools[name]; ok {
			hits++
		}
		if _, ok := excludedTools[name]; ok {
			hits++
		}
		if hits == 0 {
			t.Errorf("tool %q is in allMCPToolNames but uncategorized — add to instrumentedTools, pendingTools, or excludedTools", name)
		}
		if hits > 1 {
			t.Errorf("tool %q is categorized in %d sets — must be exactly 1", name, hits)
		}
	}

	// Check no orphans: every entry in instrumented/pending/excluded
	// appears in allMCPToolNames.
	for name := range instrumented {
		if !all[name] {
			t.Errorf("instrumentedTools contains %q but allMCPToolNames does not — stale entry?", name)
		}
	}
	for name := range pendingTools {
		if !all[name] {
			t.Errorf("pendingTools contains %q but allMCPToolNames does not — stale entry?", name)
		}
	}
	for name := range excludedTools {
		if !all[name] {
			t.Errorf("excludedTools contains %q but allMCPToolNames does not — stale entry?", name)
		}
	}
}

// TestMetadataCoverage_InstrumentedNonEmpty is a sanity check: there must
// be at least the four reference implementations from Plan 1 A1.
func TestMetadataCoverage_InstrumentedNonEmpty(t *testing.T) {
	if len(instrumentedTools) < 4 {
		t.Fatalf("expected >= 4 instrumented tools (Plan 1 A1 reference implementations), got %d: %v",
			len(instrumentedTools), instrumentedTools)
	}
	required := []string{
		"trace_call_path",
		"search_graph",
		"query_security_surfaces",
		"index_health",
	}
	have := make(map[string]bool, len(instrumentedTools))
	for _, name := range instrumentedTools {
		have[name] = true
	}
	for _, name := range required {
		if !have[name] {
			t.Errorf("required Plan 1 A1 reference tool %q is not in instrumentedTools", name)
		}
	}
}

// TestMetadataCoverage_AllMCPToolsListIsSorted enforces that
// allMCPToolNames is alphabetically sorted, matching how it was
// originally enumerated. Sorting makes diffs reviewable when a new
// tool is added.
func TestMetadataCoverage_AllMCPToolsListIsSorted(t *testing.T) {
	cp := make([]string, len(allMCPToolNames))
	copy(cp, allMCPToolNames)
	sort.Strings(cp)
	for i := range cp {
		if cp[i] != allMCPToolNames[i] {
			t.Errorf("allMCPToolNames not sorted at index %d: have %q, sorted version expects %q",
				i, allMCPToolNames[i], cp[i])
		}
	}
}
