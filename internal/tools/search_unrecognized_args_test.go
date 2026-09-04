package tools

// search_graph silently drops arguments it does not read. That turns a
// plausible-but-wrong call into an apparently-working one: `query=` is not a
// declared search_graph parameter (the tool filters on name_pattern /
// qn_pattern), so passing it yields a response ranked purely by node degree
// with nothing indicating the intended filter never applied. Observed live on
// Observed live on a Go service repo (2026-07-27): query="authentication token validation" and query="token"
// returned byte-identical top hits (Cargo.toml variables, markdown files),
// while name_pattern="token" correctly returned 50 token nodes.
//
// These tests pin the warning AND the two properties that are easy to get
// wrong once a response cache is in play:
//   1. the warning appears on a cache MISS,
//   2. it appears on a cache HIT for the same filters,
//   3. it does NOT leak onto a clean call that shares those filters.
// (3) is the one a naive "annotate then cache" implementation breaks — the
// unrecognized args are deliberately excluded from the cache key because they
// don't affect results, so caching the annotated map would pin one caller's
// warning onto every later caller.

import (
	"testing"
)

func TestSearchGraph_UnrecognizedArgs_WarnsOnCacheMiss(t *testing.T) {
	s := newServerWithSeededProject(t)

	resp := metadataResponseFromHandler(t, s.handleSearchGraph, "search_graph",
		map[string]any{
			"project": "test",
			"query":   "authentication token validation", // not a search_graph param
		})

	raw, ok := resp["unrecognized_arguments"].([]any)
	if !ok {
		t.Fatalf("expected unrecognized_arguments for an undeclared `query` arg; keys=%v", mapKeys(resp))
	}
	if len(raw) != 1 || raw[0] != "query" {
		t.Errorf("unrecognized_arguments = %v, want [query]", raw)
	}
	if _, ok := resp["unrecognized_arguments_note"].(string); !ok {
		t.Error("expected an unrecognized_arguments_note explaining name_pattern / search_code_semantic")
	}
}

func TestSearchGraph_UnrecognizedArgs_WarnsOnCacheHit(t *testing.T) {
	s := newServerWithSeededProject(t)
	args := map[string]any{"project": "test", "query": "whatever"}

	// First call populates the cache.
	if _, ok := metadataResponseFromHandler(t, s.handleSearchGraph, "search_graph", args)["unrecognized_arguments"]; !ok {
		t.Fatal("first (cache-miss) call did not warn")
	}
	// Second identical call is served from cache and must still warn.
	second := metadataResponseFromHandler(t, s.handleSearchGraph, "search_graph", args)
	if _, ok := second["unrecognized_arguments"]; !ok {
		t.Errorf("cache-hit call lost the warning; keys=%v", mapKeys(second))
	}
}

// TestSearchGraph_UnrecognizedArgs_DoesNotLeakToCleanCall is the anti-pollution
// property. A bad call and a clean call with the SAME filters share a cache
// key; the clean one must come back without a warning.
func TestSearchGraph_UnrecognizedArgs_DoesNotLeakToCleanCall(t *testing.T) {
	s := newServerWithSeededProject(t)

	// Bad call first, so the cache entry is created by the annotated request.
	bad := metadataResponseFromHandler(t, s.handleSearchGraph, "search_graph",
		map[string]any{"project": "test", "query": "noise"})
	if _, ok := bad["unrecognized_arguments"]; !ok {
		t.Fatal("setup: bad call did not warn")
	}

	// Clean call, same effective filters (no name_pattern in either) → same key.
	clean := metadataResponseFromHandler(t, s.handleSearchGraph, "search_graph",
		map[string]any{"project": "test"})
	if got, present := clean["unrecognized_arguments"]; present {
		t.Errorf("clean call inherited a cached warning: unrecognized_arguments = %v", got)
	}
	if _, present := clean["unrecognized_arguments_note"]; present {
		t.Error("clean call inherited a cached unrecognized_arguments_note")
	}
}

// TestSearchGraph_KnownArgs_NoWarning guards against false positives: every
// documented parameter must be recognized. A drift between the schema and
// searchGraphKnownArgs would otherwise warn on entirely valid calls.
func TestSearchGraph_KnownArgs_NoWarning(t *testing.T) {
	s := newServerWithSeededProject(t)

	resp := metadataResponseFromHandler(t, s.handleSearchGraph, "search_graph",
		map[string]any{
			"project":              "test",
			"label":                "Function",
			"name_pattern":         "handle",
			"qn_pattern":           ".*handler.*",
			"file_pattern":         "*.rs",
			"min_degree":           0,
			"max_degree":           99,
			"limit":                5,
			"offset":               0,
			"exclude_entry_points": false,
			"include_connected":    false,
			"case_sensitive":       false,
			"sort_by":              "relevance",
			"include_source":       false,
		})

	if got, present := resp["unrecognized_arguments"]; present {
		t.Errorf("documented arguments flagged as unrecognized: %v — searchGraphKnownArgs has drifted from the schema", got)
	}
}
