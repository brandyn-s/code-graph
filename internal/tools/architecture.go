package tools

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// validArchAspects lists all recognized aspect names.
var validArchAspects = map[string]bool{
	"all": true, "languages": true, "packages": true, "entry_points": true,
	"routes": true, "hotspots": true, "boundaries": true, "services": true,
	"layers": true, "clusters": true, "file_tree": true, "adr": true,
}

func (s *Server) handleGetArchitecture(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	aspects, validErr := parseAspects(args)
	if validErr != nil {
		return errResult(validErr.Error()), nil
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

	info, err := st.GetArchitecture(projName, aspects)
	if err != nil {
		return errResult(fmt.Sprintf("architecture: %v", err)), nil
	}

	responseData := buildArchResponse(projName, info)
	addADRToResponse(responseData, aspects, st, projName)

	s.addIndexStatus(responseData)
	result := jsonResult(responseData)
	s.addUpdateNotice(result)
	return result, nil
}

// parseAspects extracts and validates the aspects array from tool arguments.
func parseAspects(args map[string]any) ([]string, error) {
	rawAspects, ok := args["aspects"]
	if !ok {
		return []string{"all"}, nil
	}
	arr, ok := rawAspects.([]any)
	if !ok {
		return []string{"all"}, nil
	}
	var aspects []string
	for _, a := range arr {
		str, ok := a.(string)
		if !ok {
			continue
		}
		if !validArchAspects[str] {
			return nil, fmt.Errorf("unknown aspect: %q", str)
		}
		aspects = append(aspects, str)
	}
	if len(aspects) == 0 {
		return []string{"all"}, nil
	}
	return aspects, nil
}

// buildArchResponse converts ArchitectureInfo fields into a response map,
// including only non-nil aspects.
func buildArchResponse(projName string, info *store.ArchitectureInfo) map[string]any {
	data := map[string]any{"project": projName}
	if info.Languages != nil {
		data["languages"] = info.Languages
	}
	if info.Packages != nil {
		data["packages"] = info.Packages
	}
	if info.EntryPoints != nil {
		data["entry_points"] = info.EntryPoints
	}
	if info.Routes != nil {
		data["routes"] = info.Routes
	}
	if info.Hotspots != nil {
		data["hotspots"] = info.Hotspots
	}
	if info.Boundaries != nil {
		data["boundaries"] = info.Boundaries
	}
	if info.Services != nil {
		data["services"] = info.Services
	}
	if info.Layers != nil {
		data["layers"] = info.Layers
	}
	if info.Clusters != nil {
		data["clusters"] = info.Clusters
	}
	if info.FileTree != nil {
		data["file_tree"] = info.FileTree
	}
	return data
}

// addADRToResponse includes the stored ADR in the response when requested.
func addADRToResponse(data map[string]any, aspects []string, st *store.Store, projName string) {
	wantADR := false
	for _, a := range aspects {
		if a == "adr" || a == "all" {
			wantADR = true
			break
		}
	}
	if !wantADR {
		return
	}
	adr, getErr := st.GetADR(projName)
	if getErr != nil || adr == nil {
		data["adr"] = nil
		hint := "No ADR yet. Create one with manage_adr(mode='store', content='## PURPOSE\\n...\\n\\n## STACK\\n...'). For guided creation: explore the codebase, enter plan mode, draft collaboratively, then store. Sections: PURPOSE, STACK, ARCHITECTURE, PATTERNS, TRADEOFFS, PHILOSOPHY."
		if docs, err := st.FindArchitectureDocs(projName); err == nil && len(docs) > 0 {
			hint += fmt.Sprintf(" Existing architecture docs found: %v — consider reading these first.", docs)
		}
		data["adr_hint"] = hint
		return
	}
	data["adr"] = map[string]any{
		"text":       adr.Content,
		"updated_at": adr.UpdatedAt,
	}
}

func (s *Server) handleManageADR(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	mode := getStringArg(args, "mode")
	if mode == "" {
		return errResult("mode is required ('get', 'store', 'update', 'delete', or 'auto')"), nil
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

	switch mode {
	case "get":
		include := parseStringArray(args, "include")
		if err := validateSectionFilter(include); err != nil {
			return errResult(err.Error()), nil
		}
		return s.handleADRGet(st, projName, include)
	case "store":
		content := getStringArg(args, "content")
		return s.handleADRStore(st, projName, content)
	case "update":
		sections := getMapStringArg(args, "sections")
		return s.handleADRUpdate(st, projName, sections)
	case "delete":
		return s.handleADRDelete(st, projName)
	case "auto":
		return s.handleADRAuto(st, projName)
	default:
		return errResult(fmt.Sprintf("invalid mode: %q (use 'get', 'store', 'update', 'delete', or 'auto')", mode)), nil
	}
}

func (s *Server) handleADRGet(st *store.Store, projName string, include []string) (*mcp.CallToolResult, error) {
	adr, err := st.GetADR(projName)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("get ADR: %w", err)
		}
		hint := "No ADR yet. Create one with manage_adr(mode='store', content='## PURPOSE\\n...\\n\\n## STACK\\n...\\n\\n## ARCHITECTURE\\n...\\n\\n## PATTERNS\\n...\\n\\n## TRADEOFFS\\n...\\n\\n## PHILOSOPHY\\n...'). All 6 sections required: PURPOSE, STACK, ARCHITECTURE, PATTERNS, TRADEOFFS, PHILOSOPHY."
		if docs, findErr := st.FindArchitectureDocs(projName); findErr == nil && len(docs) > 0 {
			hint += fmt.Sprintf(" Existing architecture docs found: %v — consider reading these first.", docs)
		}
		return jsonResult(map[string]any{
			"project":  projName,
			"adr":      nil,
			"adr_hint": hint,
		}), nil
	}

	sections := store.ParseADRSections(adr.Content)

	const alignHint = "If you are drafting or finalizing a plan, validate it against the ADR: " +
		"check ARCHITECTURE for structural fit, PATTERNS for convention compliance, " +
		"STACK for technology alignment, and PHILOSOPHY for principle adherence. " +
		"Flag any conflicts before proceeding."

	// Filter sections if include list is provided.
	if len(include) > 0 {
		filtered := make(map[string]string, len(include))
		for _, name := range include {
			if content, ok := sections[name]; ok {
				filtered[name] = content
			}
		}
		return jsonResult(map[string]any{
			"project":        projName,
			"sections":       filtered,
			"updated_at":     adr.UpdatedAt,
			"alignment_hint": alignHint,
		}), nil
	}

	return jsonResult(map[string]any{
		"project":        projName,
		"sections":       sections,
		"text":           adr.Content,
		"updated_at":     adr.UpdatedAt,
		"alignment_hint": alignHint,
	}), nil
}

func (s *Server) handleADRStore(st *store.Store, projName, content string) (*mcp.CallToolResult, error) {
	if content == "" {
		return errResult("content is required for mode='store'"), nil
	}
	if len(content) > store.MaxADRLength() {
		return errResult(fmt.Sprintf("ADR too long (%d chars, max %d)", len(content), store.MaxADRLength())), nil
	}
	if err := store.ValidateADRContent(content); err != nil {
		return errResult(err.Error()), nil
	}
	if err := st.StoreADR(projName, content); err != nil {
		return errResult(fmt.Sprintf("store ADR: %v", err)), nil
	}
	return jsonResult(map[string]any{
		"status":     "stored",
		"project":    projName,
		"updated_at": store.Now(),
	}), nil
}

func (s *Server) handleADRUpdate(st *store.Store, projName string, sections map[string]string) (*mcp.CallToolResult, error) {
	if len(sections) == 0 {
		return errResult("sections is required for mode='update' — e.g. {\"PURPOSE\": \"...\", \"STACK\": \"...\"}"), nil
	}
	if err := store.ValidateADRSectionKeys(sections); err != nil {
		return errResult(err.Error()), nil
	}
	adr, err := st.UpdateADRSections(projName, sections)
	if err != nil {
		return errResult(fmt.Sprintf("update ADR: %v", err)), nil
	}
	parsed := store.ParseADRSections(adr.Content)
	return jsonResult(map[string]any{
		"status":     "updated",
		"project":    projName,
		"sections":   parsed,
		"text":       adr.Content,
		"updated_at": adr.UpdatedAt,
	}), nil
}

func (s *Server) handleADRDelete(st *store.Store, projName string) (*mcp.CallToolResult, error) {
	if err := st.DeleteADR(projName); err != nil {
		return errResult(fmt.Sprintf("delete ADR: %v", err)), nil
	}
	return jsonResult(map[string]any{
		"status":  "deleted",
		"project": projName,
	}), nil
}

func (s *Server) handleADRAuto(st *store.Store, projName string) (*mcp.CallToolResult, error) {
	// Compute architecture analysis with all aspects.
	info, err := st.GetArchitecture(projName, []string{"all"})
	if err != nil {
		return errResult(fmt.Sprintf("compute architecture: %v", err)), nil
	}

	// Get node and edge counts for the PURPOSE section.
	nodeCount, _ := st.CountNodes(projName)
	edgeCount, _ := st.CountEdges(projName)

	// Build each section from the architecture data.
	purpose := fmt.Sprintf("%s - Indexed codebase with %d nodes and %d edges.", projName, nodeCount, edgeCount)

	stack := formatStackSection(info.Languages)
	arch := formatArchitectureSection(info.Packages)
	patterns := formatPatternsSection(info.Hotspots, info.EntryPoints, info.Routes)
	tradeoffs := formatTradeoffsSection(info.Boundaries)
	philosophy := "Auto-generated from codebase analysis. Review and refine each section to capture intent, constraints, and decisions that static analysis cannot infer."

	// Render the full ADR content.
	sections := map[string]string{
		"PURPOSE":      purpose,
		"STACK":        stack,
		"ARCHITECTURE": arch,
		"PATTERNS":     patterns,
		"TRADEOFFS":    tradeoffs,
		"PHILOSOPHY":   philosophy,
	}
	content := store.RenderADR(sections)

	if len(content) > store.MaxADRLength() {
		return errResult(fmt.Sprintf("auto-generated ADR too long (%d chars, max %d); index has too many symbols - use mode='store' with manual content", len(content), store.MaxADRLength())), nil
	}

	if err := st.StoreADR(projName, content); err != nil {
		return errResult(fmt.Sprintf("store ADR: %v", err)), nil
	}

	return jsonResult(map[string]any{
		"status":     "stored",
		"project":    projName,
		"mode":       "auto",
		"updated_at": store.Now(),
		"hint":       "Auto-generated ADR stored. Use manage_adr(mode='get') to review, then manage_adr(mode='update', sections={...}) to refine individual sections.",
	}), nil
}

// formatStackSection builds the STACK section from language analysis.
func formatStackSection(langs []store.LanguageCount) string {
	if len(langs) == 0 {
		return "No languages detected."
	}
	total := 0
	for _, l := range langs {
		total += l.FileCount
	}
	var lines []string
	for _, l := range langs {
		pct := 0.0
		if total > 0 {
			pct = float64(l.FileCount) / float64(total) * 100
		}
		lines = append(lines, fmt.Sprintf("- %s: %d files (%.0f%%)", l.Language, l.FileCount, pct))
	}
	return strings.Join(lines, "\n")
}

// formatArchitectureSection builds the ARCHITECTURE section from package analysis.
func formatArchitectureSection(pkgs []store.PackageSummary) string {
	if len(pkgs) == 0 {
		return "No packages detected."
	}
	var lines []string
	for _, p := range pkgs {
		lines = append(lines, fmt.Sprintf("- %s: %d nodes, fan-in=%d, fan-out=%d", p.Name, p.NodeCount, p.FanIn, p.FanOut))
	}
	return strings.Join(lines, "\n")
}

// formatPatternsSection builds the PATTERNS section from hotspots, entry points, and routes.
func formatPatternsSection(hotspots []store.HotspotFunction, entryPoints []store.EntryPointInfo, routes []store.RouteInfo) string {
	var parts []string

	if len(hotspots) > 0 {
		parts = append(parts, "### Hotspots (most-called functions)")
		limit := len(hotspots)
		if limit > 5 {
			limit = 5
		}
		for _, h := range hotspots[:limit] {
			parts = append(parts, fmt.Sprintf("- %s (fan-in=%d)", h.QualifiedName, h.FanIn))
		}
	}

	if len(entryPoints) > 0 {
		parts = append(parts, "### Entry Points")
		limit := len(entryPoints)
		if limit > 5 {
			limit = 5
		}
		for _, ep := range entryPoints[:limit] {
			parts = append(parts, fmt.Sprintf("- %s (%s)", ep.Name, ep.File))
		}
	}

	if len(routes) > 0 {
		parts = append(parts, "### HTTP Routes")
		limit := len(routes)
		if limit > 5 {
			limit = 5
		}
		for _, r := range routes[:limit] {
			if r.Method != "" {
				parts = append(parts, fmt.Sprintf("- %s %s -> %s", r.Method, r.Path, r.Handler))
			} else {
				parts = append(parts, fmt.Sprintf("- %s -> %s", r.Path, r.Handler))
			}
		}
	}

	if len(parts) == 0 {
		return "No patterns detected."
	}
	return strings.Join(parts, "\n")
}

// formatTradeoffsSection builds the TRADEOFFS section from cross-package boundaries.
func formatTradeoffsSection(boundaries []store.CrossPkgBoundary) string {
	if len(boundaries) == 0 {
		return "No cross-package boundaries detected."
	}
	var lines []string
	lines = append(lines, "Cross-package call volumes (potential coupling):")
	limit := len(boundaries)
	if limit > 5 {
		limit = 5
	}
	for _, b := range boundaries[:limit] {
		lines = append(lines, fmt.Sprintf("- %s -> %s: %d calls", b.From, b.To, b.CallCount))
	}
	return strings.Join(lines, "\n")
}

// parseStringArray extracts a string array from tool arguments.
func parseStringArray(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// validateSectionFilter checks that all names in the include filter are canonical sections.
func validateSectionFilter(include []string) error {
	if len(include) == 0 {
		return nil
	}
	return store.ValidateADRSectionKeys(stringsToMap(include))
}

// stringsToMap converts a string slice to a map[string]string for key validation.
func stringsToMap(ss []string) map[string]string {
	m := make(map[string]string, len(ss))
	for _, s := range ss {
		m[s] = ""
	}
	return m
}
