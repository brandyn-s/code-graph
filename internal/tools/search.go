package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxSourceLines is the upper bound for inline source inclusion.
// Functions longer than this are omitted to keep response size reasonable.
const maxSourceLines = 50

// searchGraphKnownArgs is every argument handleSearchGraph actually reads.
// Anything else a caller sends is silently dropped, which is how a plausible
// but WRONG call reads as a working one: `query="authentication token"` (a
// parameter search_graph has never declared) is accepted, ignored, and the
// response comes back ranked purely by node degree — Cargo.toml variables and
// markdown files, with nothing indicating the filter never applied. Callers
// reasonably assume a graph search takes a `query`; `search_graph` takes
// `name_pattern`, and `search_code_semantic` is the meaning-based tool.
//
// Reported as unrecognized_arguments rather than an error: rejecting outright
// would break any caller passing a harmless extra key, and the goal is to make
// a silently-ignored argument VISIBLE, not to fail the request.
var searchGraphKnownArgs = map[string]bool{
	"project": true, "label": true, "name_pattern": true, "qn_pattern": true,
	"file_pattern": true, "relationship": true, "direction": true,
	"min_degree": true, "max_degree": true, "min_complexity": true,
	"max_complexity": true, "limit": true, "offset": true,
	"exclude_entry_points": true, "include_connected": true,
	"case_sensitive": true, "exclude_labels": true, "sort_by": true,
	"include_source": true,
}

// unrecognizedArgs returns the sorted set of argument keys the handler does not
// read, so the response can surface them instead of dropping them silently.
func unrecognizedArgs(args map[string]any, known map[string]bool) []string {
	var unknown []string
	for k := range args {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// withUnrecognizedArgs returns responseData unchanged when every argument is
// recognized, and otherwise a COPY carrying the ignored-argument warning.
//
// Copying is the point, not a style choice: queryCache stores response maps by
// reference, and unrecognized args are deliberately excluded from the cache key
// (they don't change results). Annotating a cached/soon-to-be-cached map in
// place would replay one caller's warning to every later caller with the same
// filters. Pinned by TestSearchGraph_UnrecognizedArgs_DoesNotLeakToCleanCall.
func withUnrecognizedArgs(responseData, args map[string]any) map[string]any {
	if len(unrecognizedArgs(args, searchGraphKnownArgs)) == 0 {
		return responseData
	}
	annotated := make(map[string]any, len(responseData)+2)
	for k, v := range responseData {
		annotated[k] = v
	}
	annotateUnrecognizedArgs(annotated, args)
	return annotated
}

// annotateUnrecognizedArgs adds the ignored-argument warning to a search_graph
// response in place. Callers must pass a map that is not shared with the cache
// (see withUnrecognizedArgs).
func annotateUnrecognizedArgs(responseData, args map[string]any) {
	unknown := unrecognizedArgs(args, searchGraphKnownArgs)
	if len(unknown) == 0 {
		return
	}
	responseData["unrecognized_arguments"] = unknown
	responseData["unrecognized_arguments_note"] = "These arguments were IGNORED — results are NOT filtered by them. " +
		"search_graph filters on name_pattern (regex over the short name) and qn_pattern (regex over the qualified name); " +
		"for meaning-based search use search_code_semantic. With no name/qn pattern, results are ranked by node degree, " +
		"not by relevance to any query text."
	slog.Warn("search_graph.unrecognized_arguments", "args", unknown)
}

// searchParamsFromArgs builds SearchParams from a search_graph argument map.
// Extracted from handleSearchGraph so the handler stays under the funlen limit
// (it was already at the ceiling before the unrecognized-args warning landed);
// pure argument→params mapping with no behavior change.
func searchParamsFromArgs(args map[string]any) *store.SearchParams {
	params := &store.SearchParams{
		Label:              getStringArg(args, "label"),
		NamePattern:        getStringArg(args, "name_pattern"),
		QNPattern:          getStringArg(args, "qn_pattern"),
		FilePattern:        getStringArg(args, "file_pattern"),
		Relationship:       getStringArg(args, "relationship"),
		Direction:          getStringArg(args, "direction"),
		MinDegree:          getIntArg(args, "min_degree", -1),
		MaxDegree:          getIntArg(args, "max_degree", -1),
		MinComplexity:      getIntArg(args, "min_complexity", -1),
		MaxComplexity:      getIntArg(args, "max_complexity", -1),
		Limit:              getIntArg(args, "limit", 10),
		Offset:             getIntArg(args, "offset", 0),
		ExcludeEntryPoints: getBoolArg(args, "exclude_entry_points"),
		IncludeConnected:   getBoolArg(args, "include_connected"),
		CaseSensitive:      getBoolArg(args, "case_sensitive"),
	}

	// Parse exclude_labels array; default to excluding Community nodes
	if rawLabels, ok := args["exclude_labels"]; ok {
		if labelArr, ok := rawLabels.([]any); ok {
			for _, l := range labelArr {
				if str, ok := l.(string); ok {
					params.ExcludeLabels = append(params.ExcludeLabels, str)
				}
			}
		}
	} else {
		params.ExcludeLabels = []string{"Community"}
	}

	params.SortBy = getStringArg(args, "sort_by")
	return params
}

func searchGraphCacheKey(args map[string]any, params *store.SearchParams, includeSource bool) string {
	return fmt.Sprintf("search:%s:%s:%s:%s:%s:%s:%s:%d:%d:%d:%d:%d:%d:%t:%t:%s:%s:%t:%t",
		getStringArg(args, "project"), params.Label, params.NamePattern, params.QNPattern,
		params.FilePattern, params.Relationship, params.Direction,
		params.MinDegree, params.MaxDegree,
		params.MinComplexity, params.MaxComplexity,
		params.Limit, params.Offset,
		params.ExcludeEntryPoints, params.IncludeConnected,
		strings.Join(params.ExcludeLabels, ","), params.SortBy,
		params.CaseSensitive, includeSource)
}

func (s *Server) handleSearchGraph(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	params := searchParamsFromArgs(args)
	includeSource := getBoolArg(args, "include_source")

	// Build cache key from all filter params that affect results
	cacheKey := searchGraphCacheKey(args, params, includeSource)

	// Ordinary searches preserve the existing fast cache path. Evidence-backed
	// searches must first resolve and live-check the project identity, so their
	// cache path is handled below after the store and root are known.
	if !includeSource {
		if cached, ok := s.queryCache.Get(cacheKey); ok {
			payload := cached
			if cachedMap, isMap := cached.(map[string]any); isMap {
				payload = withUnrecognizedArgs(cachedMap, args)
			}
			result := jsonResult(payload)
			s.addUpdateNotice(result)
			return result, nil
		}
	}

	st, err := s.resolveStore(getStringArg(args, "project"))
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	projName := s.resolveProjectName(getStringArg(args, "project"))
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	// Resolve the root for source reads and live checkout identity validation.
	var rootPath string
	if proj, _ := st.GetProject(projName); proj != nil {
		rootPath = proj.RootPath
	}

	if includeSource {
		if cached, ok := s.queryCache.Get(cacheKey); ok {
			payload := cached
			if cachedMap, isMap := cached.(map[string]any); isMap {
				evidencePayload := s.withGraphEvidenceRefs(
					cachedMap,
					st,
					projName,
					rootPath,
				)
				payload = withUnrecognizedArgs(evidencePayload, args)
			}
			result := jsonResult(payload)
			s.addUpdateNotice(result)
			return result, nil
		}
	}

	params.Project = projName
	output, searchErr := st.Search(params)
	if searchErr != nil {
		return errResult(fmt.Sprintf("search: %v", searchErr)), nil
	}

	results := make([]map[string]any, 0, len(output.Results))
	for _, r := range output.Results {
		entry := map[string]any{
			"project":        projName,
			"name":           r.Node.Name,
			"qualified_name": r.Node.QualifiedName,
			"label":          r.Node.Label,
			"file_path":      r.Node.FilePath,
			"start_line":     r.Node.StartLine,
			"end_line":       r.Node.EndLine,
			"in_degree":      r.InDegree,
			"out_degree":     r.OutDegree,
		}

		if len(r.ConnectedNames) > 0 {
			entry["connected_names"] = r.ConnectedNames
		}

		// Include non-empty node properties (signature, return_type, decorators, security_role, etc.)
		for k, v := range r.Node.Properties {
			if v != nil {
				entry[k] = v
			}
		}

		// Inline source for short functions when requested
		if includeSource && rootPath != "" && r.Node.FilePath != "" &&
			r.Node.StartLine > 0 && r.Node.EndLine > 0 &&
			(r.Node.EndLine-r.Node.StartLine) <= maxSourceLines {
			absPath, pathErr := safePath(rootPath, r.Node.FilePath)
			if pathErr == nil {
				source, readErr := readLines(absPath, r.Node.StartLine, r.Node.EndLine)
				if readErr == nil {
					entry["source"] = source
				} else {
					slog.Debug("search.include_source.skip", "file", r.Node.FilePath, "err", readErr)
				}
			}
		}

		results = append(results, entry)
	}

	// Per METADATA_SCHEMA.md: surface freshness + provenance so consumers
	// can distinguish results served from a fresh index vs a stale one.
	// search_graph has no native confidence band (results are filter-and-rank,
	// not confidence-scored), so we omit the confidence block.
	indexedAt := ""
	if proj, _ := st.GetProject(projName); proj != nil {
		indexedAt = proj.IndexedAt
	}
	metadata := NewMetadataBuilder().
		WithFreshness(freshnessStateFromIndexedAt(indexedAt), indexedAt).
		WithProvenance("", "index").
		Build()

	responseData := map[string]any{
		"total":     output.Total,
		"limit":     params.Limit,
		"offset":    params.Offset,
		"has_more":  params.Offset+params.Limit < output.Total,
		"results":   results,
		"_metadata": metadata,
	}
	s.addIndexStatus(responseData)

	// Cache the raw result. Evidence refs are added to a response copy only
	// after a live identity check so no stale generation can leak from cache.
	s.queryCache.Set(cacheKey, responseData)
	if includeSource {
		responseData = s.withGraphEvidenceRefs(
			responseData,
			st,
			projName,
			rootPath,
		)
	}
	responseData = withUnrecognizedArgs(responseData, args)

	result := jsonResult(responseData)
	s.addUpdateNotice(result)
	return result, nil
}
