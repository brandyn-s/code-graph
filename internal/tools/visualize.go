package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed visualize_template.html
var visualizeTemplate string

func (s *Server) registerVisualizeTool() {
	s.addTool(&mcp.Tool{
		Name: "visualize",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Generate an interactive D3.js force-directed graph visualization of the codebase as a self-contained HTML file. Nodes represent functions, classes, modules, and routes. Edges represent calls, imports, HTTP links, and other relationships. Features: color-coded node labels, edge-type toggle checkboxes, search/highlight, click-to-inspect panel. Use file_pattern to scope to a subdirectory for large codebases. Opens in any browser.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project to visualize. Defaults to session project."
				},
				"output_path": {
					"type": "string",
					"description": "Path for the output HTML file. Default: <cache>/reports/<project>/<project>-graph.html, so the indexed checkout is not modified. A relative path resolves under the project root; an absolute path must be inside the project root or the cache report directory."
				},
				"file_pattern": {
					"type": "string",
					"description": "Glob pattern to filter nodes by file path (e.g. '*.py', 'internal/*'). Reduces graph size for large codebases."
				},
				"max_nodes": {
					"type": "integer",
					"description": "Maximum number of nodes to include (default 500, max 5000). Nodes are selected by connectivity (highest degree first)."
				},
				"include_labels": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Node labels to include. Default: ['Function', 'Class', 'Method', 'Interface', 'Route', 'Module']. Use ['File', 'Package'] to see structural hierarchy."
				}
			}
		}`),
	}, s.handleVisualize)
}

//nolint:funlen // builds HTML visualization with graph data, styles, and interactivity
func (s *Server) handleVisualize(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	project := getStringArg(args, "project")
	st, err := s.resolveStore(project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	projName := s.resolveProjectName(project)
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	proj, err := st.GetProject(projName)
	if err != nil || proj == nil {
		return errResult(fmt.Sprintf("project %q not found", projName)), nil //nolint:nilerr // MCP pattern: user-facing error result
	}

	filePattern := getStringArg(args, "file_pattern")
	maxNodes := getIntArg(args, "max_nodes", 500)
	if maxNodes < 10 {
		maxNodes = 10
	}
	if maxNodes > 5000 {
		maxNodes = 5000
	}

	includeLabels := parseStringArray(args, "include_labels")
	if len(includeLabels) == 0 {
		includeLabels = []string{"Function", "Class", "Method", "Interface", "Route", "Module"}
	}

	allNodes, err := st.AllNodes(projName)
	if err != nil {
		return errResult(fmt.Sprintf("fetch nodes: %v", err)), nil
	}

	// Filter by label and file pattern
	labelSet := make(map[string]bool, len(includeLabels))
	for _, l := range includeLabels {
		labelSet[l] = true
	}

	var filtered []*store.Node
	for _, n := range allNodes {
		if !labelSet[n.Label] {
			continue
		}
		if filePattern != "" && !vizMatchFile(n.FilePath, filePattern) {
			continue
		}
		filtered = append(filtered, n)
	}

	// Sort by degree and cap at maxNodes
	if len(filtered) > maxNodes {
		degrees := make(map[int64]int, len(filtered))
		for _, n := range filtered {
			out, _ := st.FindEdgesBySource(n.ID)
			in, _ := st.FindEdgesByTarget(n.ID)
			degrees[n.ID] = len(out) + len(in)
		}
		// Degree desc, then ID asc as the deterministic tiebreaker. Two
		// nodes with equal degree would otherwise compete for the
		// maxNodes cap in random order, so identical inputs could exclude
		// different nodes from the visualization across runs.
		sort.Slice(filtered, func(i, j int) bool {
			if degrees[filtered[i].ID] != degrees[filtered[j].ID] {
				return degrees[filtered[i].ID] > degrees[filtered[j].ID]
			}
			return filtered[i].ID < filtered[j].ID
		})
		filtered = filtered[:maxNodes]
	}

	// Build node ID set for edge filtering
	nodeIDs := make(map[int64]bool, len(filtered))
	for _, n := range filtered {
		nodeIDs[n.ID] = true
	}

	// Collect edges between visible nodes
	type vizEdge struct {
		Source int64  `json:"source"`
		Target int64  `json:"target"`
		Type   string `json:"type"`
	}
	var vizEdges []vizEdge
	edgeSeen := make(map[string]bool)

	for _, n := range filtered {
		outEdges, _ := st.FindEdgesBySource(n.ID)
		for _, e := range outEdges {
			if !nodeIDs[e.TargetID] {
				continue
			}
			key := fmt.Sprintf("%d-%d-%s", e.SourceID, e.TargetID, e.Type)
			if edgeSeen[key] {
				continue
			}
			edgeSeen[key] = true
			vizEdges = append(vizEdges, vizEdge{
				Source: e.SourceID,
				Target: e.TargetID,
				Type:   e.Type,
			})
		}
	}

	// Build JSON for template injection
	type vizNode struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Label string `json:"label"`
		File  string `json:"file"`
		Line  int    `json:"line"`
	}
	vizNodes := make([]vizNode, len(filtered))
	for i, n := range filtered {
		vizNodes[i] = vizNode{
			ID:    n.ID,
			Name:  n.Name,
			Label: n.Label,
			File:  n.FilePath,
			Line:  n.StartLine,
		}
	}

	nodesJSON, _ := json.Marshal(vizNodes)
	edgesJSON, _ := json.Marshal(vizEdges)

	edgeTypes := make(map[string]bool)
	for _, e := range vizEdges {
		edgeTypes[e.Type] = true
	}
	edgeTypeList := make([]string, 0, len(edgeTypes))
	for t := range edgeTypes {
		edgeTypeList = append(edgeTypeList, t)
	}
	sort.Strings(edgeTypeList)
	edgeTypesJSON, _ := json.Marshal(edgeTypeList)

	// Inject data into HTML template
	safeProjName := html.EscapeString(projName)
	page := visualizeTemplate
	page = strings.Replace(page, "/*NODES_DATA*/", string(nodesJSON), 1)
	page = strings.Replace(page, "/*EDGES_DATA*/", string(edgesJSON), 1)
	page = strings.Replace(page, "/*EDGE_TYPES*/", string(edgeTypesJSON), 1)
	page = strings.ReplaceAll(page, "/*PROJECT_NAME*/", safeProjName)
	page = strings.Replace(page, "/*NODE_COUNT*/", fmt.Sprintf("%d", len(vizNodes)), 1)
	page = strings.Replace(page, "/*EDGE_COUNT*/", fmt.Sprintf("%d", len(vizEdges)), 1)

	// Determine output path: the cache report directory by default, the
	// checkout only when the caller asks for it explicitly.
	outputPath, pathErr := s.resolveGeneratedOutputPath(
		proj.RootPath, projName, getStringArg(args, "output_path"),
		filepath.Join(s.reportsDir(projName), projName+"-graph.html"), ".html")
	if pathErr != nil {
		return errResult(fmt.Sprintf("output_path rejected: %v", pathErr)), nil
	}

	if writeErr := os.WriteFile(outputPath, []byte(page), 0o600); writeErr != nil { //nolint:gosec // operator-chosen path, containment-checked above
		return errResult(fmt.Sprintf("write file: %v", writeErr)), nil
	}

	responseData := map[string]any{
		"status":      "created",
		"output_path": outputPath,
		"nodes":       len(vizNodes),
		"edges":       len(vizEdges),
		"edge_types":  edgeTypeList,
		"hint":        fmt.Sprintf("Open %s in a browser to explore the graph.", outputPath),
		"_metadata":   s.stdReadGraphMetadata(projName),
	}

	return jsonResult(responseData), nil
}

// vizMatchFile checks if a file path matches a glob pattern, handling ** and base-name patterns.
func vizMatchFile(path, pattern string) bool {
	// Try matching against the base filename
	if matched, err := filepath.Match(pattern, filepath.Base(path)); err == nil && matched {
		return true
	}
	// Try matching against the full path
	if matched, err := filepath.Match(pattern, path); err == nil && matched {
		return true
	}
	// Handle ** patterns with simple substring matching
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			prefix := strings.TrimRight(parts[0], "/\\")
			suffix := strings.TrimLeft(parts[1], "/\\")
			prefixOK := prefix == "" || strings.Contains(path, prefix)
			suffixOK := suffix == ""
			if !suffixOK {
				if matched, err := filepath.Match(suffix, filepath.Base(path)); err == nil && matched {
					suffixOK = true
				}
			}
			return prefixOK && suffixOK
		}
	}
	// Simple substring for patterns like "internal/*"
	if strings.Contains(pattern, "/") || strings.Contains(pattern, "\\") {
		clean := strings.ReplaceAll(pattern, "\\", "/")
		cleanPath := strings.ReplaceAll(path, "\\", "/")
		dirPart := clean
		if idx := strings.LastIndex(clean, "/"); idx >= 0 {
			dirPart = clean[:idx]
		}
		return strings.Contains(cleanPath, dirPart)
	}
	return false
}
