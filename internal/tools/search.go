package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxSourceLines is the upper bound for inline source inclusion.
// Functions longer than this are omitted to keep response size reasonable.
const maxSourceLines = 50

func (s *Server) handleSearchGraph(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	params := &store.SearchParams{
		Label:              getStringArg(args, "label"),
		NamePattern:        getStringArg(args, "name_pattern"),
		QNPattern:          getStringArg(args, "qn_pattern"),
		FilePattern:        getStringArg(args, "file_pattern"),
		Relationship:       getStringArg(args, "relationship"),
		Direction:          getStringArg(args, "direction"),
		MinDegree:          getIntArg(args, "min_degree", -1),
		MaxDegree:          getIntArg(args, "max_degree", -1),
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
	includeSource := getBoolArg(args, "include_source")

	st, err := s.resolveStore(getStringArg(args, "project"))
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	projName := s.resolveProjectName(getStringArg(args, "project"))
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	// Resolve project root for source reading
	var rootPath string
	if includeSource {
		proj, _ := st.GetProject(projName)
		if proj != nil {
			rootPath = proj.RootPath
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

	responseData := map[string]any{
		"total":    output.Total,
		"limit":    params.Limit,
		"offset":   params.Offset,
		"has_more": params.Offset+params.Limit < output.Total,
		"results":  results,
	}
	s.addIndexStatus(responseData)

	result := jsonResult(responseData)
	s.addUpdateNotice(result)
	return result, nil
}
