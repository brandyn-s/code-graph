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

		Description: "Query security-tagged code elements for compliance evidence. Returns functions classified by security_role (auth_boundary, input_entry_point, sensitive_sink, crypto_operation, privilege_escalation, session_management, audit_logging, sanitizer) with granular security_subtype. Use mode='tainted_paths' to find call paths from entry points to sinks — annotates each (source, sink) pair as 'sanitized' only when EVERY path between them crosses a sanitizer/auth_boundary node, and 'unsanitized' when at least one path reaches the sink without crossing any sanitizer (sound: a sanitizer on an unrelated branch never masks a real path). Sources are refined: main() functions are replaced by their first-hop callees that handle external input. STIG mapping: AC-3 -> auth_boundary, SI-10 -> tainted_paths, SC-13 -> crypto_operation.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"role": {
					"type": "string",
					"enum": ["auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation", "privilege_escalation", "session_management", "audit_logging", "sanitizer"],
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

	roles := []string{"auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation", "privilege_escalation", "session_management", "audit_logging", "sanitizer"}
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

	// Per METADATA_SCHEMA.md: surface freshness + provenance.
	// Confidence band: rule-based detection (not probabilistic), so report
	// "high" if results were found, "unknown" if no surfaces matched.
	indexedAt := ""
	if proj, _ := st.GetProject(projName); proj != nil {
		indexedAt = proj.IndexedAt
	}
	confBand := "unknown"
	confRationale := ""
	if totalCount > 0 {
		confBand = "high"
		confRationale = "rule-based extractor; results match deterministic security_role tags"
	}
	metadata := NewMetadataBuilder().
		WithFreshness(freshnessStateFromIndexedAt(indexedAt), indexedAt).
		WithProvenance("", "index").
		WithConfidence(confBand, confRationale).
		Build()

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
		"_metadata": metadata,
	}

	return jsonResult(responseData), nil
}

// handleTaintedPaths finds call paths from input_entry_point nodes to sensitive_sink nodes.
// Sources are refined: main() functions are replaced by their first-hop callees that handle
// external input. Each path is annotated as sanitized/unsanitized based on intermediate nodes.
func (s *Server) handleTaintedPaths(st *store.Store, projName string, args map[string]any) (*mcp.CallToolResult, error) {
	maxPaths := getIntArg(args, "limit", 50)
	depth := getIntArg(args, "depth", 4)
	if depth < 1 {
		depth = 1
	}
	if depth > 6 {
		depth = 6
	}

	excludeTests := true
	if v, ok := args["exclude_tests"]; ok {
		if b, ok := v.(bool); ok {
			excludeTests = b
		}
	}

	// Find all entry points
	allSources, err := st.FindNodesByProperty(projName, "", "security_role", "input_entry_point")
	if err != nil {
		return errResult(fmt.Sprintf("find sources: %v", err)), nil
	}

	allSinks, err := st.FindNodesByProperty(projName, "", "security_role", "sensitive_sink")
	if err != nil {
		return errResult(fmt.Sprintf("find sinks: %v", err)), nil
	}

	sources := filterTestNodes(allSources, excludeTests)
	sinks := filterTestNodes(allSinks, excludeTests)

	// Source refinement: replace main() with first-hop callees that handle external input.
	// main() is too coarse — the actual trust boundary is the first function receiving external data.
	refined := refineSources(st, sources)

	// Build sanitizer ID set for path annotation
	allSanitizers, _ := st.FindNodesByProperty(projName, "", "security_role", "sanitizer")
	allAuthBoundaries, _ := st.FindNodesByProperty(projName, "", "security_role", "auth_boundary")
	sanitizerIDs := make(map[int64]string, len(allSanitizers)+len(allAuthBoundaries))
	for _, n := range allSanitizers {
		sanitizerIDs[n.ID] = n.Name
	}
	for _, n := range allAuthBoundaries {
		sanitizerIDs[n.ID] = n.Name
	}

	sinkIDs := make(map[int64]*store.Node, len(sinks))
	for _, sink := range sinks {
		sinkIDs[sink.ID] = sink
	}

	if len(refined) == 0 || len(sinks) == 0 {
		return jsonResult(map[string]any{
			"tainted_paths": []any{},
			"sources":       len(refined),
			"sinks":         len(sinks),
			"sanitizers":    len(sanitizerIDs),
			"message":       "No tainted paths found",
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
		Sanitized     bool   `json:"sanitized"`
		SanitizedBy   string `json:"sanitized_by,omitempty"`
	}

	edgeTypes := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}
	var paths []taintedPath

	for _, src := range refined {
		if len(paths) >= maxPaths {
			break
		}

		result, bfsErr := st.BFS(src.ID, "outbound", edgeTypes, depth, 500)
		if bfsErr != nil {
			slog.Debug("tainted_paths.bfs.err", "src", src.Name, "err", bfsErr)
			continue
		}

		// A sink can appear multiple times in result.Visited (once per hop it is
		// reachable at); report each (source, sink) pair once, at its minimal hop.
		seenSink := make(map[int64]bool)
		for _, nh := range result.Visited {
			if len(paths) >= maxPaths {
				break
			}
			if _, isSink := sinkIDs[nh.Node.ID]; !isSink {
				continue
			}
			if seenSink[nh.Node.ID] {
				continue
			}
			seenSink[nh.Node.ID] = true

			srcSubtype, _ := src.Properties["security_subtype"].(string)
			sinkSubtype, _ := nh.Node.Properties["security_subtype"].(string)

			// Sound check: the pair is sanitized only if EVERY source->sink path
			// crosses a sanitizer/auth_boundary node (not merely if one exists
			// somewhere in the reachable set).
			sanitized, sanitizedBy := pathSanitized(st, src.ID, nh.Node.ID, edgeTypes, depth, sanitizerIDs)

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
				Sanitized:     sanitized,
				SanitizedBy:   sanitizedBy,
			})
		}
	}

	// Count sanitized vs unsanitized
	sanitizedCount := 0
	for i := range paths {
		if paths[i].Sanitized {
			sanitizedCount++
		}
	}

	responseData := map[string]any{
		"tainted_paths":     paths,
		"sources":           len(refined),
		"sources_original":  len(sources),
		"sinks":             len(sinks),
		"sanitizers":        len(sanitizerIDs),
		"paths_found":       len(paths),
		"paths_sanitized":   sanitizedCount,
		"paths_unsanitized": len(paths) - sanitizedCount,
		"max_depth":         depth,
		"exclude_tests":     excludeTests,
		"stig_hint":         "SI-10: An unsanitized pair means at least one path reaches the sink without crossing any sanitizer/auth_boundary node (user input may reach the sink unvalidated). A sanitized pair means every path from the source to the sink crosses a sanitizer/auth_boundary node.",
	}

	return jsonResult(responseData), nil
}

// refineSources replaces main() entry points with their first-hop callees that
// handle external input (handlers, parsers, readers). Non-main entry points
// (HTTP handlers, Route nodes) are kept as-is.
func refineSources(st *store.Store, sources []*store.Node) []*store.Node {
	var refined []*store.Node
	seen := make(map[int64]bool)

	for _, src := range sources {
		if src.Name != "main" && src.Name != "Main" {
			// Non-main entry points are already specific enough
			if !seen[src.ID] {
				seen[src.ID] = true
				refined = append(refined, src)
			}
			continue
		}

		// For main(), find first-hop callees and use them as sources instead
		edges, err := st.FindEdgesBySourceAndType(src.ID, "CALLS")
		if err != nil || len(edges) == 0 {
			// Can't resolve callees — keep main as fallback
			if !seen[src.ID] {
				seen[src.ID] = true
				refined = append(refined, src)
			}
			continue
		}

		added := 0
		for _, e := range edges {
			callee, findErr := st.FindNodeByID(e.TargetID)
			if findErr != nil || callee == nil {
				continue
			}
			if seen[callee.ID] {
				continue
			}
			// Skip trivial callees (init, setup, logging, defer-only functions)
			if isInfraCallee(callee.Name) {
				continue
			}
			seen[callee.ID] = true
			// Inherit the entry point role for BFS purposes
			if callee.Properties == nil {
				callee.Properties = map[string]any{}
			}
			if _, hasRole := callee.Properties["security_subtype"]; !hasRole {
				callee.Properties["security_subtype"] = "trust_boundary"
			}
			refined = append(refined, callee)
			added++
		}

		// If no suitable callees found, keep main
		if added == 0 && !seen[src.ID] {
			seen[src.ID] = true
			refined = append(refined, src)
		}
	}

	return refined
}

// isInfraCallee returns true for infrastructure/boilerplate function names
// that don't handle external input (logging setup, init, etc.).
var infraCalleePattern = regexp.MustCompile(`(?i)^(init|setup|configure|log_?init|set_?logger|set_?log|debug_?setup|env_?logger|tracing_?init|panic_?hook|signal_?handler|defer_|cleanup|shutdown|close|drop)$`)

func isInfraCallee(name string) bool {
	return infraCalleePattern.MatchString(name)
}

// pathSanitized reports whether EVERY call path from sourceID to sinkID (within
// maxDepth hops, over edgeTypes) passes through a sanitizer or auth_boundary
// node. It is sound for compliance use: it returns sanitized=false whenever any
// sanitizer-free path exists, so a genuinely-unsanitized path can never be
// masked by a sanitizer that merely sits on an unrelated branch.
//
// Mechanism: the pair is sanitized iff the sink is unreachable from the source
// once every sanitizer node is cut from the graph (store.ReachableExcluding).
// When sanitized, a concrete witness path is reconstructed via parent pointers
// (store.ShortestPath) and the first sanitizer on it is returned as evidence.
//
// On a query error the function fails safe — it reports unsanitized rather than
// risk masking a real taint path in compliance output.
func pathSanitized(st *store.Store, sourceID, sinkID int64, edgeTypes []string, maxDepth int, sanitizerIDs map[int64]string) (sanitized bool, sanitizerName string) {
	exclude := make(map[int64]bool, len(sanitizerIDs))
	for id := range sanitizerIDs {
		exclude[id] = true
	}

	reachable, err := st.ReachableExcluding(sourceID, sinkID, "outbound", edgeTypes, maxDepth, exclude)
	if err != nil {
		slog.Debug("tainted_paths.sanitize_check.err", "source", sourceID, "sink", sinkID, "err", err)
		return false, "" // fail safe: do not claim sanitized on error
	}
	if reachable {
		// A sanitizer-free path exists — the pair is unsanitized.
		return false, ""
	}

	// Every path crosses a sanitizer. Reconstruct a witness path for evidence.
	if path, perr := st.ShortestPath(sourceID, sinkID, "outbound", edgeTypes, maxDepth); perr == nil {
		for _, id := range path {
			if id == sourceID || id == sinkID {
				continue
			}
			if name, ok := sanitizerIDs[id]; ok {
				return true, name
			}
		}
	}
	return true, ""
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
