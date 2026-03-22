package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerExplainTool() {
	s.addTool(&mcp.Tool{
		Name: "explain_symbol",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Get a comprehensive explanation of a function, class, or module — who calls it, what it calls, which tests cover it, what env vars it reads, which package/cluster it belongs to, and its security classification. Designed for onboarding and context-switching: understand any symbol's role in the codebase in one call. Returns suggestions if the name is ambiguous.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Function, class, or module name to explain. Accepts short name or qualified name."
				},
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				}
			},
			"required": ["name"]
		}`),
	}, s.handleExplainSymbol)
}

func (s *Server) handleExplainSymbol(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	name := getStringArg(args, "name")
	if name == "" {
		return errResult("missing required 'name' parameter"), nil
	}

	project := getStringArg(args, "project")
	node, projName, err := s.findNodeAcrossProjects(name, project)
	if err != nil {
		// Try qualified name suffix match
		st, storeErr := s.resolveStore(project)
		if storeErr != nil {
			return errResult(err.Error()), nil
		}
		resolvedProj := s.resolveProjectName(project)
		projects, _ := st.ListProjects()
		if len(projects) > 0 {
			resolvedProj = projects[0].Name
		}
		nodes, findErr := st.FindNodesByQNSuffix(resolvedProj, name)
		if findErr != nil || len(nodes) == 0 {
			return errResult(fmt.Sprintf("symbol not found: %s", name)), nil
		}
		if len(nodes) > 5 {
			// Too ambiguous
			suggestions := make([]string, 0, 5)
			for i, n := range nodes {
				if i >= 5 {
					break
				}
				suggestions = append(suggestions, fmt.Sprintf("%s (%s:%d)", n.QualifiedName, n.FilePath, n.StartLine))
			}
			return jsonResult(map[string]any{
				"status":      "ambiguous",
				"matches":     len(nodes),
				"suggestions": suggestions,
				"hint":        "Use a more specific name or qualified_name to disambiguate.",
			}), nil
		}
		node = nodes[0]
		projName = resolvedProj
	}

	st, err := s.resolveStore(project)
	if err != nil {
		return errResult(err.Error()), nil
	}

	// Build the explanation
	explanation := buildExplanation(st, node, projName)

	return jsonResult(explanation), nil
}

func buildExplanation(st *store.Store, node *store.Node, projName string) map[string]any {
	result := map[string]any{
		"name":           node.Name,
		"qualified_name": node.QualifiedName,
		"label":          node.Label,
		"file_path":      node.FilePath,
		"start_line":     node.StartLine,
		"end_line":       node.EndLine,
	}

	// Properties (signature, return type, decorators, security role)
	if node.Properties != nil {
		props := make(map[string]any)
		for k, v := range node.Properties {
			props[k] = v
		}
		result["properties"] = props
	}

	// Callers (who calls this)
	callerEdges, _ := st.FindEdgesByTargetAndType(node.ID, "CALLS")
	if len(callerEdges) > 0 {
		callers := make([]map[string]any, 0, len(callerEdges))
		for i, e := range callerEdges {
			if i >= 10 {
				break
			}
			if caller, findErr := st.FindNodeByID(e.SourceID); findErr == nil && caller != nil {
				callers = append(callers, map[string]any{
					"name":      caller.Name,
					"qn":        caller.QualifiedName,
					"file_path": caller.FilePath,
				})
			}
		}
		result["callers"] = callers
		result["caller_count"] = len(callerEdges)
	}

	// Callees (what this calls)
	calleeEdges, _ := st.FindEdgesBySourceAndType(node.ID, "CALLS")
	if len(calleeEdges) > 0 {
		callees := make([]map[string]any, 0, len(calleeEdges))
		for i, e := range calleeEdges {
			if i >= 10 {
				break
			}
			if callee, findErr := st.FindNodeByID(e.TargetID); findErr == nil && callee != nil {
				callees = append(callees, map[string]any{
					"name":      callee.Name,
					"qn":        callee.QualifiedName,
					"file_path": callee.FilePath,
				})
			}
		}
		result["callees"] = callees
		result["callee_count"] = len(calleeEdges)
	}

	// Tests that cover this symbol
	testEdges, _ := st.FindEdgesByTargetAndType(node.ID, "TESTS")
	if len(testEdges) > 0 {
		tests := make([]map[string]any, 0, len(testEdges))
		for i, e := range testEdges {
			if i >= 10 {
				break
			}
			if testNode, findErr := st.FindNodeByID(e.SourceID); findErr == nil && testNode != nil {
				tests = append(tests, map[string]any{
					"name":      testNode.Name,
					"file_path": testNode.FilePath,
				})
			}
		}
		result["tests"] = tests
		result["test_count"] = len(testEdges)
	} else {
		result["test_count"] = 0
		result["test_hint"] = "No tests found covering this symbol."
	}

	// Env vars read by this function
	envEdges, _ := st.FindEdgesBySourceAndType(node.ID, "READS_ENV")
	if len(envEdges) > 0 {
		envVars := make([]string, 0, len(envEdges))
		for _, e := range envEdges {
			if envNode, findErr := st.FindNodeByID(e.TargetID); findErr == nil && envNode != nil {
				envVars = append(envVars, envNode.Name)
			}
		}
		result["env_vars"] = envVars
	}

	// Package context (extract from QN)
	pkg := extractPackageFromQN(node.QualifiedName)
	if pkg != "" {
		result["package"] = pkg
	}

	// Community/cluster membership
	memberEdges, _ := st.FindEdgesBySourceAndType(node.ID, "MEMBER_OF")
	if len(memberEdges) > 0 {
		for _, e := range memberEdges {
			if community, findErr := st.FindNodeByID(e.TargetID); findErr == nil && community != nil {
				result["cluster"] = community.Name
				break
			}
		}
	}

	// Cross-service connections (HTTP_CALLS)
	httpOut, _ := st.FindEdgesBySourceAndType(node.ID, "HTTP_CALLS")
	httpIn, _ := st.FindEdgesByTargetAndType(node.ID, "HTTP_CALLS")
	if len(httpOut)+len(httpIn) > 0 {
		var crossService []string
		for _, e := range httpOut {
			if tgt, findErr := st.FindNodeByID(e.TargetID); findErr == nil && tgt != nil {
				crossService = append(crossService, fmt.Sprintf("calls -> %s", tgt.Name))
			}
		}
		for _, e := range httpIn {
			if src, findErr := st.FindNodeByID(e.SourceID); findErr == nil && src != nil {
				crossService = append(crossService, fmt.Sprintf("called by <- %s", src.Name))
			}
		}
		result["cross_service"] = crossService
	}

	return result
}

// extractPackageFromQN extracts the package path from a qualified name.
func extractPackageFromQN(qn string) string {
	parts := strings.Split(qn, ".")
	if len(parts) <= 2 {
		return ""
	}
	// Skip project prefix (first segment) and symbol name (last segment)
	// Join the middle as the package path
	end := len(parts) - 1
	if end > 1 {
		return strings.Join(parts[1:end], ".")
	}
	return ""
}
