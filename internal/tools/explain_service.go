package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerExplainServiceTool() {
	s.addTool(&mcp.Tool{
		Name: "explain_service",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Get a comprehensive overview of an entire service/crate — its entry point, HTTP routes, env vars, cross-service dependencies, security surfaces, and test coverage. Designed for non-developers who need to understand what a service does and how it fits into the system. Pass the top-level directory name (e.g., 'controlsd', 'apid', 'trackerd').",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"service": {
					"type": "string",
					"description": "Top-level directory name of the service (e.g., 'controlsd', 'apid', 'trackerd', 'sysmanager')"
				},
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				}
			},
			"required": ["service"],
			"additionalProperties": false
		}`),
	}, s.handleExplainService)
}

// edgeConfidence extracts the confidence score from an edge's properties.
// Returns 1.0 for edges without a confidence property (e.g., HTTP_CALLS, TESTS).
func edgeConfidence(e *store.Edge) float64 {
	if e.Properties == nil {
		return 1.0
	}
	if c, ok := e.Properties["confidence"].(float64); ok {
		return c
	}
	return 1.0
}

// commonMethodNames are method/function names too generic to be reliable call targets.
// Tree-sitter fuzzy matching resolves these to whichever crate defines a function
// with that name, producing false cross-service dependencies.
var commonMethodNames = map[string]bool{
	"ok": true, "map": true, "into": true, "from": true, "new": true,
	"default": true, "clone": true, "fmt": true, "drop": true, "try_from": true,
	"try_into": true, "as_ref": true, "as_mut": true, "deref": true,
	"error": true, "debug": true, "info": true, "warn": true, "trace": true,
	"clamp": true, "min": true, "max": true, "abs": true, "len": true,
	"get": true, "set": true, "push": true, "pop": true, "append": true,
	"insert": true, "remove": true, "contains": true, "is_empty": true,
	"unwrap": true, "expect": true, "write": true, "read": true, "flush": true,
	"close": true, "open": true, "run": true, "start": true, "stop": true,
	"init": true, "setup": true, "update": true, "reset": true,
	"next": true, "iter": true, "collect": true, "filter": true, "find": true,
	"search": true, "sort": true, "reverse": true,
	"to_string": true, "to_owned": true, "parse": true,
	"print": true, "println": true, "format": true, "display": true,
	"plot": true, "store": true, "load": true, "save": true, "send": true,
	"is_some": true, "is_none": true, "is_ok": true, "is_err": true,
	"fill": true, "matches": true, "zip": true, "take": true, "skip": true,
	"eq": true, "ne": true, "cmp": true, "hash": true, "encode": true, "decode": true,
	"lock": true, "unlock": true, "recv": true, "spawn": true, "join": true,
	"poll": true, "await": true, "and_then": true, "or_else": true, "map_err": true,
}

//nolint:gocognit,cyclop,funlen // multi-phase service analysis — complexity is inherent in the cross-cutting queries
func (s *Server) handleExplainService(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	service := getStringArg(args, "service")
	if service == "" {
		return errResult("missing required 'service' parameter"), nil
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

	prefix := service + "/"
	allNodes, err := st.AllNodes(projName)
	if err != nil {
		return errResult(fmt.Sprintf("query nodes: %v", err)), nil
	}

	var serviceNodes []*store.Node
	for _, n := range allNodes {
		if strings.HasPrefix(n.FilePath, prefix) {
			serviceNodes = append(serviceNodes, n)
		}
	}

	if len(serviceNodes) == 0 {
		return errResult(fmt.Sprintf("no nodes found for service %q — check the directory name", service)), nil
	}

	serviceIDs := make(map[int64]bool, len(serviceNodes))
	for _, n := range serviceNodes {
		serviceIDs[n.ID] = true
	}

	// Entry points
	var entryPoints []map[string]any
	for _, n := range serviceNodes {
		if n.Name == "main" && n.Label == "Function" {
			entryPoints = append(entryPoints, map[string]any{
				"name": n.Name, "file": n.FilePath, "line": n.StartLine,
			})
		}
	}

	// Routes
	var routes []map[string]any
	for _, n := range serviceNodes {
		if n.Label == "Route" {
			routes = append(routes, map[string]any{
				"name": n.Name, "file": n.FilePath,
			})
		}
	}

	// Env vars
	var envVars []string
	envSeen := make(map[string]bool)
	for _, n := range serviceNodes {
		edges, _ := st.FindEdgesBySourceAndType(n.ID, "READS_ENV")
		for _, e := range edges {
			if envNode, findErr := st.FindNodeByID(e.TargetID); findErr == nil && envNode != nil {
				if !envSeen[envNode.Name] {
					envSeen[envNode.Name] = true
					envVars = append(envVars, envNode.Name)
				}
			}
		}
	}

	// Cross-service dependencies (filtered by confidence + common-name blocklist)
	const minConfidence = 0.3

	type crossDep struct {
		From       string  `json:"from_function"`
		To         string  `json:"to_function"`
		ToCrate    string  `json:"to_crate"`
		EdgeType   string  `json:"edge_type"`
		Confidence float64 `json:"confidence"`
	}
	var depsOut, depsIn []crossDep
	depOutSeen := make(map[string]bool)
	depInSeen := make(map[string]bool)

	for _, n := range serviceNodes {
		if n.Label != "Function" && n.Label != "Method" {
			continue
		}
		for _, edgeType := range []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"} {
			edges, _ := st.FindEdgesBySourceAndType(n.ID, edgeType)
			for _, e := range edges {
				if serviceIDs[e.TargetID] {
					continue
				}
				conf := edgeConfidence(e)
				if conf < minConfidence {
					continue
				}
				target, _ := st.FindNodeByID(e.TargetID)
				if target == nil || target.FilePath == "" {
					continue
				}
				if commonMethodNames[strings.ToLower(target.Name)] {
					continue
				}
				toCrate := extractTopLevelCrate(target.FilePath)
				key := n.Name + "->" + target.Name + "@" + toCrate
				if !depOutSeen[key] && len(depsOut) < 20 {
					depOutSeen[key] = true
					depsOut = append(depsOut, crossDep{From: n.Name, To: target.Name, ToCrate: toCrate, EdgeType: edgeType, Confidence: conf})
				}
			}
		}
		for _, edgeType := range []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"} {
			edges, _ := st.FindEdgesByTargetAndType(n.ID, edgeType)
			for _, e := range edges {
				if serviceIDs[e.SourceID] {
					continue
				}
				conf := edgeConfidence(e)
				if conf < minConfidence {
					continue
				}
				source, _ := st.FindNodeByID(e.SourceID)
				if source == nil || source.FilePath == "" {
					continue
				}
				if commonMethodNames[strings.ToLower(n.Name)] {
					continue
				}
				fromCrate := extractTopLevelCrate(source.FilePath)
				key := source.Name + "@" + fromCrate + "->" + n.Name
				if !depInSeen[key] && len(depsIn) < 20 {
					depInSeen[key] = true
					depsIn = append(depsIn, crossDep{From: source.Name, To: n.Name, ToCrate: fromCrate, EdgeType: edgeType, Confidence: conf})
				}
			}
		}
	}

	// Security surfaces
	type secEntry struct {
		Name    string `json:"name"`
		Role    string `json:"role"`
		Subtype string `json:"subtype,omitempty"`
		File    string `json:"file"`
	}
	var securitySurfaces []secEntry
	for _, n := range serviceNodes {
		if n.Properties == nil {
			continue
		}
		role, _ := n.Properties["security_role"].(string)
		if role == "" {
			continue
		}
		subtype, _ := n.Properties["security_subtype"].(string)
		securitySurfaces = append(securitySurfaces, secEntry{
			Name: n.Name, Role: role, Subtype: subtype, File: n.FilePath,
		})
	}

	// Test coverage
	testCount, untestedFunctions, totalFunctions := 0, 0, 0
	for _, n := range serviceNodes {
		if n.Label != "Function" && n.Label != "Method" {
			continue
		}
		totalFunctions++
		testEdges, _ := st.FindEdgesByTargetAndType(n.ID, "TESTS")
		if len(testEdges) > 0 {
			testCount += len(testEdges)
		} else {
			untestedFunctions++
		}
	}

	// File count by language
	langCounts := make(map[string]int)
	files := make(map[string]bool)
	for _, n := range serviceNodes {
		if n.FilePath != "" && !files[n.FilePath] {
			files[n.FilePath] = true
			langCounts[svcFileLanguage(n.FilePath)]++
		}
	}

	result := map[string]any{
		"service":            service,
		"file_count":         len(files),
		"node_count":         len(serviceNodes),
		"languages":          langCounts,
		"functions":          totalFunctions,
		"test_edges":         testCount,
		"untested_functions": untestedFunctions,
	}
	if len(entryPoints) > 0 {
		result["entry_points"] = entryPoints
	}
	if len(routes) > 0 {
		result["routes"] = routes
		result["route_count"] = len(routes)
	}
	if len(envVars) > 0 {
		result["env_vars"] = envVars
	}
	if len(depsOut) > 0 {
		result["depends_on"] = depsOut
	}
	if len(depsIn) > 0 {
		result["depended_by"] = depsIn
	}
	if len(securitySurfaces) > 0 {
		result["security_surfaces"] = securitySurfaces
	}

	return jsonResult(result), nil
}

func svcFileLanguage(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return "other"
	}
	switch path[idx:] {
	case ".rs":
		return "Rust"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".js", ".jsx":
		return "JavaScript"
	case ".py":
		return "Python"
	case ".go":
		return "Go"
	case ".nix":
		return "Nix"
	case ".tf":
		return "HCL"
	case ".toml":
		return "TOML"
	case ".yaml", ".yml":
		return "YAML"
	case ".sql":
		return "SQL"
	case ".html":
		return "HTML"
	case ".sh":
		return "Bash"
	default:
		return path[idx:]
	}
}
