package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerDataFlowTool() {
	s.addTool(&mcp.Tool{
		Name: "trace_data_flow",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Analyze interprocedural graph reachability from a source function. Follows outbound CALLS, READS, WRITES, and USAGE edges via BFS and classifies reachable nodes by security role. This is graph connectivity, not variable-level taint analysis: it does not model values, sanitizers, control flow, or source-to-sink flow semantics. Use required_assurance='variable_level_taint' to fail closed with a CodeQL handoff instead of returning reachability as vulnerability-grade evidence.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"source": {
					"type": "string",
					"description": "Function name to trace data flow from (e.g. 'handleRequest', 'parse_input'). Exact name or qualified name."
				},
				"depth": {
					"type": "integer",
					"description": "Maximum BFS depth (1-5, default 3). Higher depths find more transitive flows but may include noise."
				},
				"project": {
					"type": "string",
					"description": "Project to trace in. Defaults to session project."
				},
				"required_assurance": {
					"type": "string",
					"enum": ["graph_reachability", "variable_level_taint"],
					"description": "Required analysis assurance. graph_reachability (default) uses the code graph. variable_level_taint returns a structured CodeQL handoff and does not return graph connectivity as taint evidence."
				}
			},
			"required": ["source"]
		}`),
	}, s.handleDataFlow)
}

func (s *Server) handleDataFlow(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	source := getStringArg(args, "source")
	if source == "" {
		return errResult("source is required"), nil
	}
	requiredAssurance := getStringArg(args, "required_assurance")
	if requiredAssurance == "" {
		requiredAssurance = "graph_reachability"
	}
	if requiredAssurance != "graph_reachability" && requiredAssurance != "variable_level_taint" {
		return errResult("required_assurance must be 'graph_reachability' or 'variable_level_taint'"), nil
	}
	if requiredAssurance == "variable_level_taint" {
		return jsonResult(map[string]any{
			"status":               "requires_external_analyzer",
			"requested_source":     source,
			"recommended_analyzer": "CodeQL",
			"reason":               "trace_data_flow models interprocedural graph connectivity, not variable-level source-to-sink flow",
			"analysis_contract": map[string]any{
				"analysis_kind":        "variable_level_taint",
				"provided_by_tool":     false,
				"variable_level_taint": true,
			},
			"handoff": map[string]any{
				"required_artifacts": []string{"CodeQL database built from the target revision", "language-appropriate data-flow or taint-tracking query", "source, sink, and sanitizer models"},
				"acceptance":         "Use CodeQL path results for vulnerability-grade flow claims; graph reachability may be retained only as discovery context.",
			},
			"_metadata": s.stdStatusToolMetadata(),
		}), nil
	}

	depth := getIntArg(args, "depth", 3)
	if depth < 1 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}

	project := getStringArg(args, "project")
	effectiveProject := s.resolveProjectName(project)

	// Find the source node
	rootNode, foundProject, findErr := s.findNodeAcrossProjects(source, effectiveProject)
	if findErr != nil && !strings.HasPrefix(findErr.Error(), "node not found") {
		return errResult(findErr.Error()), nil
	}

	// If not found by simple name, try as qualified name
	if rootNode == nil {
		st, storeErr := s.resolveStore(effectiveProject)
		if storeErr != nil {
			return errResult(storeErr.Error()), nil
		}
		projects, _ := st.ListProjects()
		if len(projects) > 0 {
			node, qnErr := st.FindNodeByQN(projects[0].Name, source)
			if qnErr == nil && node != nil {
				rootNode = node
				foundProject = projects[0].Name
			}
		}
	}

	if rootNode == nil {
		// Try fuzzy suggestions
		suggestions := s.findSimilarNodes(source, effectiveProject, 5)
		if len(suggestions) > 0 {
			suggList := make([]map[string]string, len(suggestions))
			for i, n := range suggestions {
				suggList[i] = map[string]string{
					"name":           n.Name,
					"qualified_name": n.QualifiedName,
					"label":          n.Label,
				}
			}
			return jsonResult(map[string]any{
				"status":      "not_found",
				"message":     fmt.Sprintf("source function not found: %s - use a name from the suggestions below", source),
				"suggestions": suggList,
			}), nil
		}
		return errResult(fmt.Sprintf("source function not found: %s", source)), nil
	}

	// Get the store for BFS
	st, err := s.router.ForProject(foundProject)
	if err != nil {
		return errResult(fmt.Sprintf("store: %v", err)), nil
	}

	// BFS outbound through data flow edges
	edgeTypes := []string{"CALLS", "WRITES", "READS", "USAGE"}
	result, bfsErr := st.BFS(rootNode.ID, "outbound", edgeTypes, depth, 200)
	if bfsErr != nil {
		slog.Debug("trace_data_flow.bfs.err", "source", source, "err", bfsErr)
		return errResult(fmt.Sprintf("BFS error: %v", bfsErr)), nil
	}

	// Classify visited nodes by security role
	var sinks []map[string]any
	var crypto []map[string]any
	var auth []map[string]any
	var flowPath []map[string]any

	for _, nh := range result.Visited {
		role := ""
		if r, ok := nh.Node.Properties["security_role"]; ok {
			if rs, ok := r.(string); ok {
				role = rs
			}
		}

		entry := map[string]any{
			"name":           nh.Node.Name,
			"qualified_name": nh.Node.QualifiedName,
			"label":          nh.Node.Label,
			"file_path":      nh.Node.FilePath,
			"hop":            nh.Hop,
			"security_role":  role,
		}

		flowPath = append(flowPath, entry)

		switch role {
		case "sensitive_sink":
			sinks = append(sinks, entry)
		case "crypto_operation":
			crypto = append(crypto, entry)
		case "auth_boundary":
			auth = append(auth, entry)
		}
	}

	// Sort flow path by hop then name for stable output
	sort.Slice(flowPath, func(i, j int) bool {
		hi, _ := flowPath[i]["hop"].(int)
		hj, _ := flowPath[j]["hop"].(int)
		if hi != hj {
			return hi < hj
		}
		ni, _ := flowPath[i]["name"].(string)
		nj, _ := flowPath[j]["name"].(string)
		return ni < nj
	})

	responseData := map[string]any{
		"source":      source,
		"total_nodes": len(result.Visited),
		"sinks_found": len(sinks),
		"sinks":       sinks,
		"crypto":      crypto,
		"auth":        auth,
		"flow_path":   flowPath,
		"edges":       buildEdgeList(result.Edges),
		"_metadata":   s.stdReadGraphMetadata(effectiveProject),
		"analysis_contract": map[string]any{
			"analysis_kind":        "interprocedural_graph_reachability",
			"variable_level_taint": false,
			"edge_semantics":       "CALLS_READS_WRITES_USAGE_connectivity",
			"soundness":            "heuristic",
			"allowed_claim":        "a graph path connects the source function to the returned node",
			"forbidden_claim":      "a runtime value or attacker-controlled datum reaches the returned node",
		},
	}
	s.addIndexStatus(responseData)

	toolResult := jsonResult(responseData)
	s.addUpdateNotice(toolResult)
	return toolResult, nil
}
