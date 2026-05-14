package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) handleTraceCallPath(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	funcName := getStringArg(args, "function_name")
	if funcName == "" {
		return errResult("function_name is required"), nil
	}

	depth := getIntArg(args, "depth", 3)
	if depth < 1 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}

	direction := getStringArg(args, "direction")
	if direction == "" {
		direction = "outbound"
	}

	riskLabels := getBoolArg(args, "risk_labels")
	// Default 0.45: filter speculative cross-crate matches that
	// resolved by name-only fuzzy matching (PSM test battery 2026-05-07
	// surfaced 4 such false positives at confidence 0.20-0.38 for a
	// 14-line function — wrong crates, same method name). Users who want
	// the full unfiltered trace can pass min_confidence=0 explicitly.
	// Bands: high (>=0.7), medium (>=0.45), speculative (<0.45).
	minConfidence := getFloatArg(args, "min_confidence", 0.45)
	includeSource := getBoolArg(args, "include_source")

	project := getStringArg(args, "project")
	effectiveProject := s.resolveProjectName(project)

	// Ambiguity guard: if function_name is a bare name (no qualifier separator)
	// AND multiple nodes share that exact name, return a structured ambiguous
	// response instead of silently picking the first match. This was the F8
	// failure mode surfaced by the 2026-05-13 PSM tool-comparison battery:
	// trace_call_path("ControlProto::new") resolved to AssetSN::new in a
	// different file because findNodeAcrossProjects walked the projects list
	// and took nodes[0]. With this guard, the caller gets the candidate QNs
	// and can retry with a fully-qualified name to disambiguate. A "bare name"
	// is recognized as a string with no `.` — code-graph QNs use dot separators
	// (e.g. `<project>.<module>.<Type>.new`). Type-style `Type::method` strings
	// are also ambiguous by construction and reach this branch.
	if !strings.Contains(funcName, ".") {
		ambig := s.findAmbiguousNodesByName(funcName, effectiveProject, 5)
		if len(ambig) > 1 {
			suggList := make([]map[string]string, len(ambig))
			for i, n := range ambig {
				suggList[i] = map[string]string{
					"name":           n.Name,
					"qualified_name": n.QualifiedName,
					"label":          n.Label,
					"file_path":      n.FilePath,
				}
			}
			return jsonResult(map[string]any{
				"status":      "ambiguous",
				"message":     fmt.Sprintf("function name %q matches %d nodes; re-call trace_call_path with a fully-qualified name from the suggestions below to disambiguate", funcName, len(ambig)),
				"suggestions": suggList,
			}), nil
		}
	}

	// Find the function node
	rootNode, foundProject, findErr := s.findNodeAcrossProjects(funcName, effectiveProject)
	if findErr != nil && !strings.HasPrefix(findErr.Error(), "node not found") {
		return errResult(findErr.Error()), nil
	}
	if rootNode == nil {
		// Fuzzy fallback: search for similar names and return structured suggestions
		suggestions := s.findSimilarNodes(funcName, effectiveProject, 5)
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
				"message":     fmt.Sprintf("function not found: %s — use a name from the suggestions below", funcName),
				"suggestions": suggList,
			}), nil
		}
		return errResult(fmt.Sprintf("function not found: %s", funcName)), nil
	}

	// Get the store for the found project
	st, err := s.router.ForProject(foundProject)
	if err != nil {
		return errResult(fmt.Sprintf("store: %v", err)), nil
	}

	edgeTypes := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}

	allVisited, allEdges, bfsErr := runTraceBFS(st, rootNode.ID, direction, edgeTypes, depth, minConfidence)
	if bfsErr != nil {
		return errResult(fmt.Sprintf("bfs err: %v", bfsErr)), nil
	}

	if riskLabels {
		allVisited = store.DeduplicateHops(allVisited)
	}

	var hops []hopEntry
	if riskLabels {
		hops = buildHopsWithRisk(allVisited)
	} else {
		hops = buildHops(allVisited)
	}

	responseData := buildTraceResponse(st, rootNode, foundProject, hops, allVisited, allEdges)
	if riskLabels {
		responseData["impact_summary"] = store.BuildImpactSummary(allVisited, allEdges)
	}
	responseData["module"] = s.getModuleInfo(st, rootNode, foundProject)

	// Inline source for short functions in hop nodes and root
	if includeSource {
		proj, _ := st.GetProject(foundProject)
		if proj != nil {
			inlineTraceSource(responseData, proj.RootPath)
		}
	}
	s.addIndexStatus(responseData)

	result := jsonResult(responseData)
	s.addUpdateNotice(result)
	return result, nil
}

func runTraceBFS(st *store.Store, rootID int64, direction string, edgeTypes []string, depth int, minConfidence float64) ([]*store.NodeHop, []store.EdgeInfo, error) {
	if direction == "both" {
		var allVisited []*store.NodeHop
		var allEdges []store.EdgeInfo
		outResult, outErr := st.BFS(rootID, "outbound", edgeTypes, depth, 200)
		if outErr == nil {
			allVisited = append(allVisited, outResult.Visited...)
			allEdges = append(allEdges, outResult.Edges...)
		}
		inResult, inErr := st.BFS(rootID, "inbound", edgeTypes, depth, 200)
		if inErr == nil {
			allVisited = append(allVisited, inResult.Visited...)
			allEdges = append(allEdges, inResult.Edges...)
		}
		if minConfidence > 0 {
			allEdges = filterEdgesByConfidence(allEdges, minConfidence)
		}
		return allVisited, allEdges, nil
	}
	result, err := st.BFS(rootID, direction, edgeTypes, depth, 200)
	if err != nil {
		return nil, nil, err
	}
	edges := result.Edges
	if minConfidence > 0 {
		edges = filterEdgesByConfidence(edges, minConfidence)
	}
	return result.Visited, edges, nil
}

// filterEdgesByConfidence removes edges below the threshold.
// Edges with confidence=0 (no confidence set, e.g. HTTP_CALLS) are kept.
func filterEdgesByConfidence(edges []store.EdgeInfo, minConfidence float64) []store.EdgeInfo {
	filtered := make([]store.EdgeInfo, 0, len(edges))
	for _, e := range edges {
		if e.Confidence == 0 || e.Confidence >= minConfidence {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func buildTraceResponse(st *store.Store, rootNode *store.Node, project string, hops []hopEntry, visited []*store.NodeHop, edges []store.EdgeInfo) map[string]any {
	proj, _ := st.GetProject(project)
	indexedAt := ""
	if proj != nil {
		indexedAt = proj.IndexedAt
	}

	// Sum unresolved_call_count across root + visited nodes. This signals
	// how many call-sites the extractor saw but could not bind to a concrete
	// callee (closures, fn-pointers, trait-objects, dynamic dispatch, builtins,
	// methods on dynamically-typed objects). Surfacing the total lets callers
	// distinguish "no callees, confidently" from "no callees, extraction
	// missed dispatch sites".
	unresolvedTotal := nodeUnresolvedCount(rootNode)
	for _, nh := range visited {
		if nh != nil && nh.Node != nil {
			unresolvedTotal += nodeUnresolvedCount(nh.Node)
		}
	}

	// confidence_band classifies the trace by resolved/(resolved+unresolved).
	// "high" — extractor bound the call sites well; trust the edges
	// "medium" — partial coverage; trust the edges but expect gaps
	// "low" — most call sites unresolved; combine with grep before trusting
	// "speculative" — zero edges and >0 unresolved; trace is silent on real calls
	resolvedCount := len(edges)
	band := traceConfidenceBand(resolvedCount, unresolvedTotal)

	// Structured metadata block per METADATA_SCHEMA.md.
	// Generalizes confidence_band/unresolved_call_count under _metadata.
	// Top-level fields preserved for backwards compatibility.
	metaRationale := ""
	totalCalls := resolvedCount + unresolvedTotal
	if totalCalls > 0 {
		pct := (resolvedCount * 100) / totalCalls
		metaRationale = formatRatioRationale(resolvedCount, totalCalls, pct)
	}
	metadata := NewMetadataBuilder().
		WithFreshness(freshnessStateFromIndexedAt(indexedAt), indexedAt).
		WithProvenance("", "index").
		WithConfidence(band, metaRationale).
		Build()

	return map[string]any{
		"root":                  buildNodeInfo(rootNode),
		"hops":                  hops,
		"edges":                 buildEdgeList(edges),
		"indexed_at":            indexedAt,
		"total_results":         len(visited),
		"unresolved_call_count": unresolvedTotal,
		"confidence_band":       band,
		"_metadata":             metadata,
	}
}

// formatRatioRationale produces a short string like "432 of 480 calls resolved (90%)".
func formatRatioRationale(resolved, total, pct int) string {
	return itoaTools(resolved) + " of " + itoaTools(total) + " calls resolved (" + itoaTools(pct) + "%)"
}

func itoaTools(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// freshnessStateFromIndexedAt returns "current" when indexedAt is non-empty,
// "unknown" otherwise. Tools needing finer-grained staleness should call
// WithStaleness directly.
func freshnessStateFromIndexedAt(indexedAt string) string {
	if indexedAt == "" {
		return "unknown"
	}
	return "current"
}

func nodeUnresolvedCount(n *store.Node) int {
	if n == nil || n.Properties == nil {
		return 0
	}
	v, ok := n.Properties["unresolved_call_count"]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// traceConfidenceBand classifies a function's call-resolution quality.
//
// Thresholds were originally heuristic (0.8/0.5/0.2 — added 2026-05-05 in
// the initial confidence_band ship). 2026-05-05b: replaced with empirical
// thresholds from a probe over all 11 indexed projects (n=18,783 functions
// with both resolved and unresolved calls; bench/research/
// confidence_band_distribution.py). The distribution is sharply bimodal:
//   - 72% of nodes cluster at ratio >= 0.95 (perfect/near-perfect resolution)
//   - ~10% cluster at ratio < 0.10 (essentially all calls unresolved)
//   - Middle bands (0.10-0.95) are sparse and overrepresent partial-extraction
//     cases (e.g., Python with some method calls resolved, others on
//     dynamically-typed Path objects unresolved).
// Heuristic 0.8/0.5 thresholds put 72% of nodes in "high" — too generous
// to be informative. Empirical 0.95/0.10 thresholds match the natural
// breakpoints in the data and produce calibrated output.
//
// Per-language follow-up: psm (Rust/TS) shows P10=1.00
// across the board (well-typed languages resolve cleanly). Python projects
// (.claude, mcp-servers, rmf-corsair, mcp-infra) skew low. Future work could
// emit a band-per-language threshold; for now, global thresholds give
// honest signal to callers.
func traceConfidenceBand(resolved, unresolved int) string {
	total := resolved + unresolved
	if total == 0 {
		// No calls reported at all — likely a leaf or a function the extractor
		// considers fully resolved-as-empty. Distinct from "speculative".
		return "high"
	}
	if resolved == 0 && unresolved > 0 {
		return "speculative"
	}
	ratio := float64(resolved) / float64(total)
	switch {
	case ratio >= 0.95:
		// Empirically the dominant cluster — 72% of nodes hit this in the
		// 2026-05-05 probe across 11 projects.
		return "high"
	case ratio >= 0.10:
		// The sparse middle. Partial extraction; trust the edges that ARE
		// there but expect gaps. ~18% of nodes land here.
		return "medium"
	default:
		// Below 10% resolved — essentially "the extractor saw N call sites
		// and bound 0 or 1 of them." ~10% of nodes hit this. Distinct from
		// "speculative" only by having at least one resolved edge.
		return "low"
	}
}

func buildNodeInfo(n *store.Node) map[string]any {
	info := map[string]any{
		"name":           n.Name,
		"qualified_name": n.QualifiedName,
		"label":          n.Label,
		"file_path":      n.FilePath,
		"start_line":     n.StartLine,
		"end_line":       n.EndLine,
	}
	if sig, ok := n.Properties["signature"]; ok {
		info["signature"] = sig
	}
	if rt, ok := n.Properties["return_type"]; ok {
		info["return_type"] = rt
	}
	return info
}

func (s *Server) getModuleInfo(st *store.Store, funcNode *store.Node, project string) map[string]any {
	if funcNode.FilePath == "" {
		return map[string]any{}
	}

	modules, err := st.FindNodesByLabel(project, "Module")
	if err != nil {
		return map[string]any{}
	}

	for _, m := range modules {
		if m.FilePath == funcNode.FilePath {
			info := map[string]any{"name": m.Name}
			if constants, ok := m.Properties["constants"]; ok {
				info["constants"] = constants
			}
			return info
		}
	}
	return map[string]any{}
}

type hopEntry struct {
	Hop   int              `json:"hop"`
	Nodes []map[string]any `json:"nodes"`
}

func buildHops(visited []*store.NodeHop) []hopEntry {
	hopMap := map[int][]map[string]any{}
	for _, nh := range visited {
		info := map[string]any{
			"name":           nh.Node.Name,
			"qualified_name": nh.Node.QualifiedName,
			"label":          nh.Node.Label,
			"file_path":      nh.Node.FilePath,
			"start_line":     nh.Node.StartLine,
			"end_line":       nh.Node.EndLine,
		}
		if sig, ok := nh.Node.Properties["signature"]; ok {
			info["signature"] = sig
		}
		hopMap[nh.Hop] = append(hopMap[nh.Hop], info)
	}

	var hops []hopEntry
	for h := 1; h <= len(hopMap); h++ {
		if nodes, ok := hopMap[h]; ok {
			hops = append(hops, hopEntry{Hop: h, Nodes: nodes})
		}
	}
	return hops
}

func buildHopsWithRisk(visited []*store.NodeHop) []hopEntry {
	hopMap := map[int][]map[string]any{}
	for _, nh := range visited {
		info := map[string]any{
			"name":           nh.Node.Name,
			"qualified_name": nh.Node.QualifiedName,
			"label":          nh.Node.Label,
			"file_path":      nh.Node.FilePath,
			"start_line":     nh.Node.StartLine,
			"end_line":       nh.Node.EndLine,
			"risk":           string(store.HopToRisk(nh.Hop)),
			"hop":            nh.Hop,
		}
		if sig, ok := nh.Node.Properties["signature"]; ok {
			info["signature"] = sig
		}
		hopMap[nh.Hop] = append(hopMap[nh.Hop], info)
	}

	var hops []hopEntry
	for h := 1; h <= len(hopMap); h++ {
		if nodes, ok := hopMap[h]; ok {
			hops = append(hops, hopEntry{Hop: h, Nodes: nodes})
		}
	}
	return hops
}

// findAmbiguousNodesByName returns all nodes whose exact Name equals the
// input. Returns at most `limit` results. Used by handleTraceCallPath to
// detect bare-name ambiguity (multiple `new` methods on different types,
// multiple `Default` impls, etc.) so the tool can return a structured
// ambiguous response instead of silently picking nodes[0]. Differs from
// findSimilarNodes (substring match) in that this requires exact equality
// — only nodes whose Name field is exactly the input are returned.
func (s *Server) findAmbiguousNodesByName(name, project string, limit int) []*store.Node {
	effectiveProject := s.resolveProjectName(project)
	if effectiveProject == "" {
		return nil
	}
	if !s.router.HasProject(effectiveProject) {
		return nil
	}
	st, err := s.router.ForProject(effectiveProject)
	if err != nil {
		return nil
	}
	return findAmbiguousNodesByNameInStore(st, name, limit)
}

// findAmbiguousNodesByNameInStore is the testable inner of
// findAmbiguousNodesByName: given a store, return up to `limit` nodes whose
// exact Name equals the input. Exported only for unit tests that supply a
// store directly without going through s.router.
func findAmbiguousNodesByNameInStore(st *store.Store, name string, limit int) []*store.Node {
	projects, _ := st.ListProjects()
	if len(projects) == 0 {
		return nil
	}
	projName := projects[0].Name
	all, findErr := st.FindNodesByName(projName, name)
	if findErr != nil {
		return nil
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// findSimilarNodes searches for nodes whose name contains the input string (case-insensitive).
func (s *Server) findSimilarNodes(name, project string, limit int) []*store.Node {
	effectiveProject := s.resolveProjectName(project)
	if effectiveProject == "" {
		return nil
	}
	if !s.router.HasProject(effectiveProject) {
		return nil
	}
	st, err := s.router.ForProject(effectiveProject)
	if err != nil {
		return nil
	}
	// Get actual project name from DB
	projName := effectiveProject
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}
	params := &store.SearchParams{
		Project:       projName,
		NamePattern:   regexp.QuoteMeta(name),
		Limit:         limit,
		MinDegree:     -1,
		MaxDegree:     -1,
		ExcludeLabels: []string{"Community"},
	}
	out, searchErr := st.Search(params)
	if searchErr != nil {
		return nil
	}
	nodes := make([]*store.Node, len(out.Results))
	for i, r := range out.Results {
		nodes[i] = r.Node
	}
	return nodes
}

// inlineTraceSource adds source code to the root node and hop nodes in the trace response.
// Only inlines functions under maxSourceLines (defined in search.go).
func inlineTraceSource(responseData map[string]any, rootPath string) {
	// Inline root node source
	if root, ok := responseData["root"].(map[string]any); ok {
		inlineNodeSource(root, rootPath)
	}

	// Inline hop node sources
	if hops, ok := responseData["hops"].([]hopEntry); ok {
		for _, hop := range hops {
			for _, node := range hop.Nodes {
				inlineNodeSource(node, rootPath)
			}
		}
	}
}

// inlineNodeSource reads and attaches source to a node info map if the function is short enough.
func inlineNodeSource(nodeInfo map[string]any, rootPath string) {
	filePath, _ := nodeInfo["file_path"].(string)
	startF, _ := nodeInfo["start_line"].(int)
	endF, _ := nodeInfo["end_line"].(int)

	if filePath == "" || startF == 0 || endF == 0 {
		return
	}
	if endF-startF > maxSourceLines {
		return
	}

	absPath, pathErr := safePath(rootPath, filePath)
	if pathErr != nil {
		return
	}
	source, readErr := readLines(absPath, startF, endF)
	if readErr != nil {
		return
	}
	nodeInfo["source"] = source
}

func buildEdgeList(edges []store.EdgeInfo) []map[string]any {
	result := make([]map[string]any, 0, len(edges))
	for _, e := range edges {
		entry := map[string]any{
			"from": e.FromName,
			"to":   e.ToName,
			"type": e.Type,
		}
		if e.Confidence > 0 {
			entry["confidence"] = e.Confidence
		}
		result = append(result, entry)
	}
	return result
}
