package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
		Description: "Detect circular dependencies between crates, packages, or files. Default 'crate' level uses the top-level directory (e.g., 'doomper', 'libtracker') as the unit — best for monorepos. Reports each cycle with the crates involved. Use for architecture reviews and preventing new circular dependencies.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"level": {
					"type": "string",
					"enum": ["crate", "package", "file"],
					"description": "Granularity: 'crate' (default) for top-level directory cycles, 'package' for submodule-level, 'file' for file-level. Crate-level is fastest and most actionable for monorepos."
				},
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				},
				"max_depth": {
					"type": "integer",
					"description": "Maximum cycle length to detect (2-6, default 4). Longer cycles take more time."
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
		level = "crate"
	}
	maxDepth := getIntArg(args, "max_depth", 4)
	if maxDepth < 2 {
		maxDepth = 2
	}
	if maxDepth > 6 {
		maxDepth = 6
	}

	edgeTypes := []string{"CALLS", "IMPORTS", "HTTP_CALLS", "ASYNC_CALLS"}
	depGraph := buildDependencyGraph(st, projName, level, edgeTypes)

	cycles := findCycles(depGraph, maxDepth)

	type cycleResult struct {
		Nodes  []string `json:"nodes"`
		Length int      `json:"length"`
	}

	var results []cycleResult
	for _, cycle := range cycles {
		results = append(results, cycleResult{
			Nodes:  cycle,
			Length: len(cycle),
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Length != results[j].Length {
			return results[i].Length < results[j].Length
		}
		return fmt.Sprint(results[i].Nodes) < fmt.Sprint(results[j].Nodes)
	})

	// Cap output at 100 cycles
	truncated := false
	if len(results) > 100 {
		results = results[:100]
		truncated = true
	}

	responseData := map[string]any{
		"cycles":       results,
		"cycles_found": len(results),
		"level":        level,
		"max_depth":    maxDepth,
		"graph_nodes":  len(depGraph),
		"_metadata":    s.stdReadGraphMetadata(projName),
	}

	if truncated {
		responseData["truncated"] = true
		responseData["hint"] = "Results capped at 100 cycles. Use a smaller max_depth or 'crate' level to reduce results."
	}

	if len(results) == 0 {
		responseData["message"] = fmt.Sprintf("No dependency cycles found at %s level (max depth %d).", level, maxDepth)
	}

	return jsonResult(responseData), nil
}

// buildDependencyGraph builds a dependency graph at the specified granularity.
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
			switch level {
			case "crate":
				fromContainer = extractTopLevelCrate(src.FilePath)
				toContainer = extractTopLevelCrate(tgt.FilePath)
			case "package":
				fromContainer = extractSubpackage(src.FilePath)
				toContainer = extractSubpackage(tgt.FilePath)
			case "file":
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

// extractSubpackage extracts the first two path segments as the subpackage.
// E.g., "doomper/src/recorder.rs" -> "doomper/src"
func extractSubpackage(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	parts := strings.SplitN(path, "/", 4)
	if len(parts) < 2 {
		return path
	}
	if len(parts) >= 3 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// findCycles uses iterative DFS to find all unique cycles up to maxDepth.
func findCycles(graph map[string]map[string]bool, maxDepth int) [][]string {
	var cycles [][]string
	seen := make(map[string]bool)

	for startNode := range graph {
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

			for next := range graph[f.node] {
				if next == startNode && len(f.path) >= 2 {
					cycle := make([]string, len(f.path))
					copy(cycle, f.path)
					key := canonicalCycleKey(cycle)
					if !seen[key] {
						seen[key] = true
						cycles = append(cycles, cycle)
					}
					continue
				}

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

// canonicalCycleKey deduplicates rotations of the same cycle.
func canonicalCycleKey(cycle []string) string {
	if len(cycle) == 0 {
		return ""
	}
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
