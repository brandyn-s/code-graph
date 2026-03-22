package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerCyclesTool() {
	s.addTool(&mcp.Tool{
		Name: "detect_cycles",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Detect circular dependencies between packages or modules. Finds cycles where package A depends on B depends on C depends on A, indicating architectural coupling that should be broken. Reports each cycle with the packages involved and the edge types (CALLS, IMPORTS, HTTP_CALLS) that form it. Use for architecture reviews, refactoring planning, and preventing new cycles.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"level": {
					"type": "string",
					"enum": ["package", "file"],
					"description": "Granularity: 'package' (default) for package-level cycles, 'file' for file-level cycles. Package-level is faster and more actionable."
				},
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				},
				"max_depth": {
					"type": "integer",
					"description": "Maximum cycle length to detect (2-8, default 5). Longer cycles take more time."
				}
			}
		}`),
	}, s.handleDetectCycles)
}

func (s *Server) handleDetectCycles(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
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

	level := getStringArg(args, "level")
	if level == "" {
		level = "package"
	}
	maxDepth := getIntArg(args, "max_depth", 5)
	if maxDepth < 2 {
		maxDepth = 2
	}
	if maxDepth > 8 {
		maxDepth = 8
	}

	// Build package/file dependency graph from edges
	edgeTypes := []string{"CALLS", "IMPORTS", "HTTP_CALLS", "ASYNC_CALLS"}
	depGraph := buildDependencyGraph(st, projName, level, edgeTypes)

	// Find cycles using DFS
	cycles := findCycles(depGraph, maxDepth)

	type cycleResult struct {
		Nodes     []string `json:"nodes"`
		Length    int      `json:"length"`
		EdgeTypes []string `json:"edge_types,omitempty"`
	}

	var results []cycleResult
	for _, cycle := range cycles {
		results = append(results, cycleResult{
			Nodes:  cycle,
			Length: len(cycle),
		})
	}

	// Sort by length (shorter cycles are more actionable)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Length < results[j].Length
	})

	responseData := map[string]any{
		"cycles":       results,
		"cycles_found": len(results),
		"level":        level,
		"max_depth":    maxDepth,
	}

	if len(results) == 0 {
		responseData["message"] = fmt.Sprintf("No dependency cycles found at %s level (max depth %d).", level, maxDepth)
	}

	return jsonResult(responseData), nil
}

// depEdge represents a directed dependency between two containers (packages or files).
type depEdge struct {
	from, to string
}

// buildDependencyGraph builds a package-level or file-level dependency graph.
func buildDependencyGraph(st *store.Store, project, level string, edgeTypes []string) map[string]map[string]bool {
	graph := make(map[string]map[string]bool)

	for _, edgeType := range edgeTypes {
		edges, err := st.FindEdgesByType(project, edgeType)
		if err != nil {
			continue
		}
		for _, e := range edges {
			src, _ := st.FindNodeByID(e.SourceID)
			tgt, _ := st.FindNodeByID(e.TargetID)
			if src == nil || tgt == nil {
				continue
			}

			var fromContainer, toContainer string
			if level == "package" {
				fromContainer = extractPackage(src.QualifiedName)
				toContainer = extractPackage(tgt.QualifiedName)
			} else {
				fromContainer = src.FilePath
				toContainer = tgt.FilePath
			}

			if fromContainer == "" || toContainer == "" || fromContainer == toContainer {
				continue
			}

			if graph[fromContainer] == nil {
				graph[fromContainer] = make(map[string]bool)
			}
			graph[fromContainer][toContainer] = true
		}
	}

	return graph
}

// extractPackage extracts the package component from a qualified name.
// E.g., "project.internal.store.Store.FindNode" -> "internal.store"
func extractPackage(qn string) string {
	parts := splitQN(qn)
	if len(parts) < 3 {
		return ""
	}
	// Skip project prefix and node name suffix, take the middle as package
	// Heuristic: everything between the first and last two segments
	if len(parts) <= 3 {
		return parts[1]
	}
	// Join middle segments as the package path
	end := len(parts) - 1
	if end > 4 {
		end = 4 // Cap package depth to avoid overly specific paths
	}
	pkg := ""
	for i := 1; i < end; i++ {
		if pkg != "" {
			pkg += "."
		}
		pkg += parts[i]
	}
	return pkg
}

// splitQN splits a qualified name by dots.
func splitQN(qn string) []string {
	var parts []string
	current := ""
	for _, c := range qn {
		if c == '.' {
			if current != "" {
				parts = append(parts, current)
			}
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// findCycles uses iterative DFS to find all unique cycles up to maxDepth.
func findCycles(graph map[string]map[string]bool, maxDepth int) [][]string {
	var cycles [][]string
	seen := make(map[string]bool) // canonical cycle key -> already reported

	for startNode := range graph {
		// DFS stack: each entry is (current_node, path_so_far)
		type frame struct {
			node string
			path []string
		}
		stack := []frame{{node: startNode, path: []string{startNode}}}

		for len(stack) > 0 {
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if len(f.path) > maxDepth {
				continue
			}

			neighbors := graph[f.node]
			for next := range neighbors {
				if next == startNode && len(f.path) >= 2 {
					// Found a cycle back to start
					cycle := make([]string, len(f.path))
					copy(cycle, f.path)

					key := canonicalCycleKey(cycle)
					if !seen[key] {
						seen[key] = true
						cycles = append(cycles, cycle)
					}
					continue
				}

				// Don't revisit nodes in the current path
				inPath := false
				for _, p := range f.path {
					if p == next {
						inPath = true
						break
					}
				}
				if inPath {
					continue
				}

				newPath := make([]string, len(f.path)+1)
				copy(newPath, f.path)
				newPath[len(f.path)] = next
				stack = append(stack, frame{node: next, path: newPath})
			}
		}
	}

	return cycles
}

// canonicalCycleKey produces a canonical string for a cycle so we can dedup
// rotations (A->B->C == B->C->A == C->A->B).
func canonicalCycleKey(cycle []string) string {
	if len(cycle) == 0 {
		return ""
	}

	// Find the lexicographically smallest rotation
	minIdx := 0
	for i := 1; i < len(cycle); i++ {
		if cycle[i] < cycle[minIdx] {
			minIdx = i
		}
	}

	key := ""
	for i := 0; i < len(cycle); i++ {
		idx := (minIdx + i) % len(cycle)
		if key != "" {
			key += " -> "
		}
		key += cycle[idx]
	}
	return key
}
