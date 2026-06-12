package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerHealthTool() {
	s.addTool(&mcp.Tool{
		Name: "index_health",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Check index health by comparing graph coverage against files on disk. Reports: file count on disk vs indexed, orphaned nodes (files deleted since indexing), missing files (on disk but not indexed), enrichment version staleness, and node/edge counts. Use after indexing to verify coverage or to diagnose why symbols are missing from search results.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project to check. Defaults to session project."
				}
			}
		}`),
	}, s.handleIndexHealth)
}

func (s *Server) handleIndexHealth(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	project := getStringArg(args, "project")
	effectiveProject := s.resolveProjectName(project)
	if effectiveProject == "" {
		return errResult("no project specified and no session project detected; pass project parameter"), nil
	}

	st, err := s.resolveStore(effectiveProject)
	if err != nil {
		return errResult(err.Error()), nil
	}

	// Get project metadata
	proj, err := st.GetProject(effectiveProject)
	if err != nil {
		return errResult(fmt.Sprintf("project %q: %v", effectiveProject, err)), nil
	}
	if proj == nil {
		return errResult(fmt.Sprintf("project %q not found", effectiveProject)), nil
	}
	if proj.RootPath == "" {
		return errResult("project has no root_path — reindex with repo_path"), nil
	}

	// Discover files on disk; tally unsupported extensions while walking
	// (2026-06-10 grammar-cut visibility — files with no grammar otherwise
	// vanish from coverage stats by construction, see cut_languages.go).
	unsupportedTally := make(map[string]int)
	diskFiles, err := discover.Discover(ctx, proj.RootPath, &discover.Options{Mode: discover.ModeFull, UnsupportedTally: unsupportedTally})
	if err != nil {
		return errResult(fmt.Sprintf("discover files: %v", err)), nil
	}
	diskSet := make(map[string]bool, len(diskFiles))
	for _, f := range diskFiles {
		diskSet[f.RelPath] = true
	}

	// Get indexed file hashes from store
	indexedHashes, err := st.GetFileHashes(effectiveProject)
	if err != nil {
		return errResult(fmt.Sprintf("get file hashes: %v", err)), nil
	}
	indexedSet := make(map[string]bool, len(indexedHashes))
	for relPath := range indexedHashes {
		indexedSet[relPath] = true
	}

	// Compare: missing from index (on disk but not indexed)
	var missingFiles []string
	for relPath := range diskSet {
		if !indexedSet[relPath] {
			missingFiles = append(missingFiles, relPath)
		}
	}
	sort.Strings(missingFiles)

	// Compare: orphaned in index (indexed but not on disk)
	var orphanedFiles []string
	for relPath := range indexedSet {
		if !diskSet[relPath] {
			orphanedFiles = append(orphanedFiles, relPath)
		}
	}
	sort.Strings(orphanedFiles)

	// Node/edge counts
	nodeCount, _ := st.CountNodes(effectiveProject)
	edgeCount, _ := st.CountEdges(effectiveProject)

	// Resolver rule + confidence tier distribution. Each CALLS edge
	// already records which resolver rule emitted it and which confidence
	// tier it lands in — surface those aggregates so operators can see
	// the precision posture of an index without sampling edges.
	resolverRuleCounts, _ := st.CallsResolverRuleStats(effectiveProject)
	confidenceTierCounts, _ := st.CallsConfidenceTierStats(effectiveProject)

	// Build response
	enrichmentStale := proj.EnrichmentVersion != pipeline.EnrichmentVersion

	// B4 (post-roundtable Phase B): grammar-version data + parse-error
	// rate from the canary corpus. The data is hand-recorded in
	// bench/research/grammar_canaries/baselines.json (see GRAMMARS.md);
	// surface it here so MCP consumers can call index_health and get
	// drift visibility without running the script themselves.
	grammarVersions, grammarVersionsAge, _ := loadGrammarVersionsAge()

	responseData := map[string]any{
		"project":            effectiveProject,
		"files_on_disk":      len(diskFiles),
		"files_indexed":      len(indexedHashes),
		"missing_from_index": len(missingFiles),
		"orphaned_in_index":  len(orphanedFiles),
		"nodes":              nodeCount,
		"edges":              edgeCount,
		"enrichment_version": proj.EnrichmentVersion,
		"enrichment_current": pipeline.EnrichmentVersion,
		"enrichment_stale":   enrichmentStale,
		"indexed_at":         proj.IndexedAt,
	}
	if len(grammarVersions) > 0 {
		responseData["grammar_versions"] = grammarVersions
	}
	if grammarVersionsAge >= 0 {
		responseData["grammar_versions_age_days"] = grammarVersionsAge
	}
	if len(resolverRuleCounts) > 0 {
		responseData["calls_by_resolver_rule"] = resolverRuleCounts
	}
	if len(confidenceTierCounts) > 0 {
		responseData["calls_by_confidence_tier"] = confidenceTierCounts
	}
	unsupportedTotal, cutLanguageFiles, unknownExtensions := buildUnsupportedTelemetry(unsupportedTally)
	if unsupportedTotal > 0 {
		responseData["unsupported_extension_files"] = unsupportedTotal
	}
	if len(cutLanguageFiles) > 0 {
		responseData["cut_language_files"] = cutLanguageFiles
	}
	if len(unknownExtensions) > 0 {
		responseData["unknown_extensions"] = unknownExtensions
	}

	// Include file lists if small enough to be useful
	const maxListSize = 20
	if len(missingFiles) > 0 && len(missingFiles) <= maxListSize {
		responseData["missing_files"] = missingFiles
	}
	if len(orphanedFiles) > 0 && len(orphanedFiles) <= maxListSize {
		responseData["orphaned_files"] = orphanedFiles
	}

	// Structured _metadata block per METADATA_SCHEMA.md.
	// index_health is a self-reporting tool; freshness is its own subject.
	metadata := NewMetadataBuilder().
		WithFreshness(freshnessStateFromIndexedAt(proj.IndexedAt), proj.IndexedAt).
		WithProvenance("", "index").
		WithGrammarVersions(grammarVersions).
		Build()
	responseData["_metadata"] = metadata

	s.addIndexStatus(responseData)

	result := jsonResult(responseData)
	s.addUpdateNotice(result)
	return result, nil
}

// buildUnsupportedTelemetry splits an UnsupportedTally into two reporting
// tiers with different signal-to-noise: cut_language_files covers extensions
// from the 2026-06-10 grammar cut and is reported at ANY count — a single
// Kotlin file appearing in a redacted repo is the adoption-lag signal the cut
// created. unknown_extensions is informational: top 10 remaining extensions
// with >= 3 files (filters one-off junk like a stray .bak). The returned
// total counts every tallied file regardless of tier.
func buildUnsupportedTelemetry(tally map[string]int) (total int, cut map[string]map[string]any, unknown []map[string]any) {
	if len(tally) == 0 {
		return 0, nil, nil
	}
	type extCount struct {
		ext   string
		count int
	}
	var unknownCounts []extCount
	for ext, count := range tally {
		total += count
		if hint, ok := discover.CutLanguageHints[ext]; ok {
			if cut == nil {
				cut = make(map[string]map[string]any)
			}
			cut[ext] = map[string]any{"count": count, "language": hint}
		} else if count >= 3 {
			unknownCounts = append(unknownCounts, extCount{ext, count})
		}
	}
	sort.Slice(unknownCounts, func(i, j int) bool {
		if unknownCounts[i].count != unknownCounts[j].count {
			return unknownCounts[i].count > unknownCounts[j].count
		}
		return unknownCounts[i].ext < unknownCounts[j].ext
	})
	if len(unknownCounts) > 10 {
		unknownCounts = unknownCounts[:10]
	}
	for _, ec := range unknownCounts {
		unknown = append(unknown, map[string]any{"extension": ec.ext, "count": ec.count})
	}
	return total, cut, unknown
}
