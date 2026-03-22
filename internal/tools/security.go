package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var testFilePattern = regexp.MustCompile(`(?i)(_test\.(go|rs|py|ts|js)|test_\w+\.(py|rs)|tests?[/\\]|__tests__[/\\]|spec[/\\]|_spec\.(rb|ts|js))`)
var testNamePattern = regexp.MustCompile(`(?i)^(test_|Test[A-Z])`)

func (s *Server) registerSecurityTools() {
	s.addTool(&mcp.Tool{
		Name: "query_security_surfaces",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Query security-tagged code elements for compliance evidence. Returns functions classified by security_role (auth_boundary, input_entry_point, sensitive_sink, crypto_operation, privilege_escalation, session_management, audit_logging) with granular security_subtype (e.g. sql_query, shell_exec, file_write for sinks; http_handler, cli_entry for entry points; encryption, hashing, signing for crypto). Use mode='tainted_paths' to find all call paths from input_entry_point nodes to sensitive_sink nodes — produces SI-10 STIG evidence directly. STIG mapping: AC-3 -> auth_boundary, SI-10 -> tainted_paths, SC-13 -> crypto_operation, IA-2 -> privilege_escalation, SC-23 -> session_management, AU-2 -> audit_logging.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"role": {
					"type": "string",
					"enum": ["auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation", "privilege_escalation", "session_management", "audit_logging"],
					"description": "Filter by security role. Omit for all roles. Ignored when mode='tainted_paths'."
				},
				"mode": {
					"type": "string",
					"enum": ["surfaces", "tainted_paths"],
					"description": "Query mode. 'surfaces' (default): list security-tagged nodes. 'tainted_paths': find call paths from input_entry_point to sensitive_sink nodes using BFS."
				},
				"project": {
					"type": "string",
					"description": "Project to query. Defaults to session project."
				},
				"limit": {
					"type": "integer",
					"description": "Max results per role (default 20) or max paths for tainted_paths (default 50)"
				},
				"depth": {
					"type": "integer",
					"description": "Max BFS depth for tainted_paths mode (1-6, default 4)"
				},
				"exclude_tests": {
					"type": "boolean",
					"description": "Exclude test files and test functions from sources and sinks (default true). Set false to include test code in taint analysis."
				}
			}
		}`),
	}, s.handleQuerySecuritySurfaces)
}

func (s *Server) handleQuerySecuritySurfaces(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	mode := getStringArg(args, "mode")
	if mode == "tainted_paths" {
		return s.handleTaintedPaths(st, projName, args)
	}

	return s.handleSurfacesQuery(st, projName, args)
}

func (s *Server) handleSurfacesQuery(st *store.Store, projName string, args map[string]any) (*mcp.CallToolResult, error) {
	roleFilter := getStringArg(args, "role")
	limit := getIntArg(args, "limit", 20)

	roles := []string{"auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation", "privilege_escalation", "session_management", "audit_logging"}
	if roleFilter != "" {
		roles = []string{roleFilter}
	}

	type surfaceEntry struct {
		Name            string `json:"name"`
		QualifiedName   string `json:"qualified_name"`
		Label           string `json:"label"`
		FilePath        string `json:"file_path"`
		SecurityRole    string `json:"security_role"`
		SecuritySubtype string `json:"security_subtype,omitempty"`
		Callers         int    `json:"callers"`
		Callees         int    `json:"callees"`
	}

	results := make(map[string][]surfaceEntry)
	totalCount := 0

	for _, role := range roles {
		nodes, findErr := st.FindNodesByProperty(projName, "", "security_role", role)
		if findErr != nil {
			continue
		}
		entries := make([]surfaceEntry, 0, len(nodes))
		for i, n := range nodes {
			if i >= limit {
				break
			}
			callers, callees := st.NodeDegree(n.ID)
			subtype, _ := n.Properties["security_subtype"].(string)
			entries = append(entries, surfaceEntry{
				Name:            n.Name,
				QualifiedName:   n.QualifiedName,
				Label:           n.Label,
				FilePath:        n.FilePath,
				SecurityRole:    role,
				SecuritySubtype: subtype,
				Callers:         callers,
				Callees:         callees,
			})
		}
		if len(entries) > 0 {
			results[role] = entries
			totalCount += len(nodes)
		}
	}

	responseData := map[string]any{
		"surfaces":    results,
		"total_count": totalCount,
		"stig_hints": map[string]string{
			"AC-3":  "Check auth_boundary nodes enforce access control on all input_entry_point paths",
			"SI-10": "Use mode='tainted_paths' to find all paths from input_entry_point to sensitive_sink",
			"SC-13": "Confirm crypto_operation nodes use FIPS-approved algorithms (check security_subtype for encryption/hashing/signing)",
			"IA-2":  "Verify privilege_escalation nodes require multi-factor or re-authentication before elevation",
			"SC-23": "Confirm session_management nodes enforce session authenticity and proper lifecycle (create/destroy/timeout)",
			"AU-2":  "Verify audit_logging nodes capture required auditable events per organization-defined list",
		},
	}

	return jsonResult(responseData), nil
}

// handleTaintedPaths finds call paths from input_entry_point nodes to sensitive_sink nodes.
func (s *Server) handleTaintedPaths(st *store.Store, projName string, args map[string]any) (*mcp.CallToolResult, error) {
	maxPaths := getIntArg(args, "limit", 50)
	depth := getIntArg(args, "depth", 4)
	if depth < 1 {
		depth = 1
	}
	if depth > 6 {
		depth = 6
	}

	// exclude_tests defaults to true
	excludeTests := true
	if v, ok := args["exclude_tests"]; ok {
		if b, ok := v.(bool); ok {
			excludeTests = b
		}
	}

	// Find all source nodes (input_entry_point)
	allSources, err := st.FindNodesByProperty(projName, "", "security_role", "input_entry_point")
	if err != nil {
		return errResult(fmt.Sprintf("find sources: %v", err)), nil
	}

	// Build sink ID set
	allSinks, err := st.FindNodesByProperty(projName, "", "security_role", "sensitive_sink")
	if err != nil {
		return errResult(fmt.Sprintf("find sinks: %v", err)), nil
	}

	// Filter out test code if requested
	sources := filterTestNodes(allSources, excludeTests)
	sinks := filterTestNodes(allSinks, excludeTests)

	sinkIDs := make(map[int64]*store.Node, len(sinks))
	for _, sink := range sinks {
		sinkIDs[sink.ID] = sink
	}

	if len(sources) == 0 || len(sinks) == 0 {
		return jsonResult(map[string]any{
			"tainted_paths": []any{},
			"sources":       len(sources),
			"sinks":         len(sinks),
			"message":       "No tainted paths found (need both input_entry_point and sensitive_sink nodes)",
		}), nil
	}

	type taintedPath struct {
		SourceName    string `json:"source_name"`
		SourceQN      string `json:"source_qn"`
		SourceSubtype string `json:"source_subtype,omitempty"`
		SourceFile    string `json:"source_file"`
		SinkName      string `json:"sink_name"`
		SinkQN        string `json:"sink_qn"`
		SinkSubtype   string `json:"sink_subtype,omitempty"`
		SinkFile      string `json:"sink_file"`
		Hops          int    `json:"hops"`
		PathNodes     []string `json:"path_via,omitempty"`
	}

	edgeTypes := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}
	var paths []taintedPath

	for _, src := range sources {
		if len(paths) >= maxPaths {
			break
		}

		result, bfsErr := st.BFS(src.ID, "outbound", edgeTypes, depth, 500)
		if bfsErr != nil {
			slog.Debug("tainted_paths.bfs.err", "src", src.Name, "err", bfsErr)
			continue
		}

		for _, nh := range result.Visited {
			if len(paths) >= maxPaths {
				break
			}
			if _, isSink := sinkIDs[nh.Node.ID]; !isSink {
				continue
			}

			srcSubtype, _ := src.Properties["security_subtype"].(string)
			sinkSubtype, _ := nh.Node.Properties["security_subtype"].(string)

			paths = append(paths, taintedPath{
				SourceName:    src.Name,
				SourceQN:      src.QualifiedName,
				SourceSubtype: srcSubtype,
				SourceFile:    src.FilePath,
				SinkName:      nh.Node.Name,
				SinkQN:        nh.Node.QualifiedName,
				SinkSubtype:   sinkSubtype,
				SinkFile:      nh.Node.FilePath,
				Hops:          nh.Hop,
			})
		}
	}

	responseData := map[string]any{
		"tainted_paths":  paths,
		"sources":        len(sources),
		"sinks":          len(sinks),
		"paths_found":    len(paths),
		"max_depth":      depth,
		"exclude_tests":  excludeTests,
		"stig_hint":      "SI-10: Each tainted path represents user input reaching a sensitive sink. Verify input validation exists on the path (check for auth_boundary nodes between source and sink).",
	}

	return jsonResult(responseData), nil
}

// filterTestNodes removes test files and test functions from a node list.
func filterTestNodes(nodes []*store.Node, exclude bool) []*store.Node {
	if !exclude {
		return nodes
	}
	filtered := make([]*store.Node, 0, len(nodes))
	for _, n := range nodes {
		if !isTestNode(n) {
			filtered = append(filtered, n)
		}
	}
	return filtered
}

// isTestNode returns true if the node is in a test file or has a test function name.
func isTestNode(n *store.Node) bool {
	if testFilePattern.MatchString(n.FilePath) {
		return true
	}
	if testNamePattern.MatchString(n.Name) {
		return true
	}
	if strings.Contains(n.QualifiedName, ".test.") || strings.Contains(n.QualifiedName, ".tests.") {
		return true
	}
	return false
}
