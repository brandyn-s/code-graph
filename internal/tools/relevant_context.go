package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerRelevantContextTool() {
	s.addTool(&mcp.Tool{
		Name: "get_relevant_context",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "CALL THIS BEFORE editing code in an indexed project. Given target files, returns the minimal set of related files you need to read — callers, callees, tests, and change-coupled files — ranked by relevance within a token budget. With include_content=true, returns file contents directly so you don't need separate Read calls. Prevents wasting tokens on irrelevant files and missing files that would cause regressions.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"files": {
					"type": "array",
					"items": {"type": "string"},
					"description": "File paths being modified (relative to repo root, e.g. 'src/auth/jwt.rs')"
				},
				"token_budget": {
					"type": "integer",
					"description": "Maximum total tokens to include (default 8000). Files are added highest-priority first until budget is exhausted."
				},
				"include_content": {
					"type": "boolean",
					"description": "If true, inline file contents in the response (up to token budget). Eliminates the need for separate Read calls. Default: false."
				},
				"project": {
					"type": "string",
					"description": "Project to query. Defaults to session project."
				}
			},
			"required": ["files"]
		}`),
	}, s.handleRelevantContext)
}

// contextFile represents a file recommended for reading with its relevance metadata.
type contextFile struct {
	File         string `json:"file"`
	Relationship string `json:"relationship"`
	Priority     int    `json:"priority"`
	TokenEst     int    `json:"token_estimate"`
	Symbols      int    `json:"symbols"`
	Content      string `json:"content,omitempty"`
}

//nolint:gocognit // multi-phase context gathering — files, callers, callees, tests, coupling, content inlining
func (s *Server) handleRelevantContext(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	files := getStringSliceArg(args, "files")
	if len(files) == 0 {
		return errResult("files parameter is required and must be non-empty"), nil
	}

	tokenBudget := getIntArg(args, "token_budget", 8000)
	if tokenBudget < 500 {
		tokenBudget = 500
	}
	includeContent := getBoolArg(args, "include_content")

	st, err := s.resolveStore(getStringArg(args, "project"))
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	// Resolve repo root for reading file contents
	var repoRoot string
	if includeContent {
		repoRoot, err = s.resolveProjectRoot(getStringArg(args, "project"))
		if err != nil {
			// Fall back to no content if root can't be resolved
			includeContent = false
		}
	}

	projName := s.resolveProjectName(getStringArg(args, "project"))
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	// Collect files by relationship type with priority
	// Priority: 1=target, 2=direct caller/callee, 3=test, 4=change-coupled, 5=transitive
	fileScores := make(map[string]*contextFile)

	// Step 1: Target files themselves (priority 1)
	var targetNodeIDs []int64
	for _, f := range files {
		f = strings.ReplaceAll(f, "\\", "/")
		nodes, findErr := st.FindNodesByFile(projName, f)
		if findErr != nil || len(nodes) == 0 {
			// Try suffix match for partial paths
			nodes = findNodesByFileSuffix(st, projName, f)
		}
		for _, n := range nodes {
			targetNodeIDs = append(targetNodeIDs, n.ID)
		}
		if _, ok := fileScores[f]; !ok {
			lineEst := estimateFileLines(nodes)
			fileScores[f] = &contextFile{
				File:         f,
				Relationship: "target",
				Priority:     1,
				TokenEst:     lineEst * 4, // ~4 chars per token rough estimate
				Symbols:      len(nodes),
			}
		}
	}

	if len(targetNodeIDs) == 0 {
		return jsonResult(map[string]any{
			"files":   []any{},
			"summary": fmt.Sprintf("No symbols found in graph for files: %v. Is the project indexed?", files),
		}), nil
	}

	// Step 2: Direct callers and callees (priority 2)
	callEdgeTypes := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}
	outEdges, _ := st.FindEdgesBySourceIDs(targetNodeIDs, callEdgeTypes)
	inEdges, _ := st.FindEdgesByTargetIDs(targetNodeIDs, callEdgeTypes)

	callerCalleeIDs := make(map[int64]string) // nodeID -> relationship
	for _, edges := range outEdges {
		for _, e := range edges {
			callerCalleeIDs[e.TargetID] = "callee"
		}
	}
	for _, edges := range inEdges {
		for _, e := range edges {
			callerCalleeIDs[e.SourceID] = "caller"
		}
	}

	addNodesAsFiles(st, callerCalleeIDs, fileScores, 2, files)

	// Step 3: Tests covering target symbols (priority 3)
	testIDs := make(map[int64]string)
	for _, nid := range targetNodeIDs {
		edges, findErr := st.FindEdgesByTargetAndType(nid, "TESTS")
		if findErr != nil {
			continue
		}
		for _, e := range edges {
			testIDs[e.SourceID] = "test"
		}
	}

	addNodesAsFiles(st, testIDs, fileScores, 3, files)

	// Step 4: Change-coupled files (priority 4)
	changeCoupledFiles := findChangeCoupledFiles(st, projName, files)
	for f, score := range changeCoupledFiles {
		if _, exists := fileScores[f]; !exists {
			fileScores[f] = &contextFile{
				File:         f,
				Relationship: fmt.Sprintf("change_coupled (%.2f)", score),
				Priority:     4,
				TokenEst:     500, // estimate without node data
				Symbols:      0,
			}
		}
	}

	// Step 5: Transitive callers/callees (priority 5) — 2nd hop
	transitiveIDs := make(map[int64]string)
	var hop1IDs []int64
	for id := range callerCalleeIDs {
		hop1IDs = append(hop1IDs, id)
	}
	if len(hop1IDs) > 0 {
		outEdges2, _ := st.FindEdgesBySourceIDs(hop1IDs, callEdgeTypes)
		inEdges2, _ := st.FindEdgesByTargetIDs(hop1IDs, callEdgeTypes)
		for _, edges := range outEdges2 {
			for _, e := range edges {
				if _, isTarget := callerCalleeIDs[e.TargetID]; !isTarget {
					transitiveIDs[e.TargetID] = "transitive_callee"
				}
			}
		}
		for _, edges := range inEdges2 {
			for _, e := range edges {
				if _, isTarget := callerCalleeIDs[e.SourceID]; !isTarget {
					transitiveIDs[e.SourceID] = "transitive_caller"
				}
			}
		}
	}

	addNodesAsFiles(st, transitiveIDs, fileScores, 5, files)

	// Sort by priority, then by token estimate (smaller first within same priority)
	result := make([]*contextFile, 0, len(fileScores))
	for _, cf := range fileScores {
		result = append(result, cf)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		return result[i].TokenEst < result[j].TokenEst
	})

	// Apply token budget
	var selected []*contextFile
	var excluded []*contextFile
	usedTokens := 0
	for _, cf := range result {
		if usedTokens+cf.TokenEst <= tokenBudget {
			selected = append(selected, cf)
			usedTokens += cf.TokenEst
		} else {
			excluded = append(excluded, cf)
		}
	}

	// Read file contents if requested
	if includeContent && repoRoot != "" {
		for _, cf := range selected {
			content, readErr := readWholeFile(filepath.Join(repoRoot, cf.File))
			if readErr == nil {
				cf.Content = content
			}
		}
	}

	// Summary stats
	byRelationship := make(map[string]int)
	for _, cf := range selected {
		rel := cf.Relationship
		if strings.HasPrefix(rel, "change_coupled") {
			rel = "change_coupled"
		}
		byRelationship[rel]++
	}

	return jsonResult(map[string]any{
		"files":            selected,
		"excluded":         len(excluded),
		"tokens_used":      usedTokens,
		"token_budget":     tokenBudget,
		"total_candidates": len(result),
		"by_relationship":  byRelationship,
		"include_content":  includeContent,
		"summary": fmt.Sprintf(
			"%d files selected (%d tokens), %d excluded over budget. Relationships: %v",
			len(selected), usedTokens, len(excluded), byRelationship,
		),
	}), nil
}

// addNodesAsFiles resolves node IDs to files and adds them to the fileScores map.
func addNodesAsFiles(st *store.Store, nodeMap map[int64]string, fileScores map[string]*contextFile, priority int, targetFiles []string) {
	targetSet := make(map[string]bool, len(targetFiles))
	for _, f := range targetFiles {
		targetSet[strings.ReplaceAll(f, "\\", "/")] = true
	}

	ids := make([]int64, 0, len(nodeMap))
	for id := range nodeMap {
		ids = append(ids, id)
	}

	nodes, _ := st.FindNodesByIDs(ids)
	// Group by file
	fileNodes := make(map[string]int)
	fileRel := make(map[string]string)
	for _, n := range nodes {
		fp := strings.ReplaceAll(n.FilePath, "\\", "/")
		if targetSet[fp] {
			continue // Skip target files themselves
		}
		fileNodes[fp]++
		if _, ok := fileRel[fp]; !ok {
			fileRel[fp] = nodeMap[n.ID]
		}
	}

	for fp, count := range fileNodes {
		if _, exists := fileScores[fp]; exists {
			continue // Already added at higher priority
		}
		fileScores[fp] = &contextFile{
			File:         fp,
			Relationship: fileRel[fp],
			Priority:     priority,
			TokenEst:     count * 200, // ~200 tokens per symbol as estimate
			Symbols:      count,
		}
	}
}

// findNodesByFileSuffix tries suffix matching when exact file path doesn't match.
func findNodesByFileSuffix(st *store.Store, project, filePath string) []*store.Node {
	filePath = strings.ReplaceAll(filePath, "\\", "/")
	// Try matching just the filename portion
	parts := strings.Split(filePath, "/")
	if len(parts) == 0 {
		return nil
	}

	// Search for nodes whose file_path ends with the given path
	allNodes, err := st.FindNodesByFile(project, filePath)
	if err == nil && len(allNodes) > 0 {
		return allNodes
	}

	// Try without leading directory
	for i := 0; i < len(parts)-1; i++ {
		suffix := strings.Join(parts[i:], "/")
		nodes, findErr := st.FindNodesByFile(project, suffix)
		if findErr == nil && len(nodes) > 0 {
			return nodes
		}
	}
	return nil
}

// estimateFileLines estimates the total line count for nodes in a file.
func estimateFileLines(nodes []*store.Node) int {
	if len(nodes) == 0 {
		return 100 // default estimate
	}
	maxLine := 0
	for _, n := range nodes {
		if n.EndLine > maxLine {
			maxLine = n.EndLine
		}
	}
	if maxLine == 0 {
		return 100
	}
	return maxLine
}

// findChangeCoupledFiles finds files that change together with the target files.
func findChangeCoupledFiles(st *store.Store, project string, targetFiles []string) map[string]float64 {
	targetSet := make(map[string]bool, len(targetFiles))
	for _, f := range targetFiles {
		targetSet[strings.ReplaceAll(f, "\\", "/")] = true
	}

	edges, err := st.FindEdgesByType(project, "FILE_CHANGES_WITH")
	if err != nil {
		return nil
	}

	coupled := make(map[string]float64)
	for _, e := range edges {
		src, _ := st.FindNodeByID(e.SourceID)
		tgt, _ := st.FindNodeByID(e.TargetID)
		if src == nil || tgt == nil {
			continue
		}

		srcPath := strings.ReplaceAll(src.FilePath, "\\", "/")
		tgtPath := strings.ReplaceAll(tgt.FilePath, "\\", "/")

		score := 0.0
		if e.Properties != nil {
			if s, ok := e.Properties["coupling_score"].(float64); ok {
				score = s
			}
		}
		if score < 0.3 {
			continue
		}

		if targetSet[srcPath] && !targetSet[tgtPath] {
			if existing, ok := coupled[tgtPath]; !ok || score > existing {
				coupled[tgtPath] = score
			}
		}
		if targetSet[tgtPath] && !targetSet[srcPath] {
			if existing, ok := coupled[srcPath]; !ok || score > existing {
				coupled[srcPath] = score
			}
		}
	}
	return coupled
}

// readWholeFile reads a file's full content, capped at 64KB to prevent memory issues.
func readWholeFile(path string) (string, error) {
	const maxSize = 64 * 1024
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, maxSize), maxSize)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		fmt.Fprintf(&sb, "%4d | %s\n", lineNum, scanner.Text())
		if sb.Len() > maxSize {
			fmt.Fprintf(&sb, "... (truncated at %d lines)\n", lineNum)
			break
		}
	}
	return sb.String(), scanner.Err()
}

// getStringSliceArg extracts a string array argument from parsed args.
func getStringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
