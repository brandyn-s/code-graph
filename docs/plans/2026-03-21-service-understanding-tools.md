# Service Understanding Tools Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add three tools for non-developers who need to understand a large monorepo: explain_service (full service context), service_map (domain-grouped overview), and diff_services (service-level change summary between git refs).

**Architecture:** Each tool queries the existing graph (nodes, edges, properties) using Store methods. No new pipeline passes needed — all data already exists from indexing. The tools assemble higher-level views from existing low-level graph data.

**Tech Stack:** Go, SQLite queries via Store, git CLI for diff_services.

---

### Task 1: explain_service Tool

**Files:**
- Create: `internal/tools/explain_service.go`
- Modify: `internal/tools/tools.go:439-455` (add registration)

**Step 1: Create `internal/tools/explain_service.go`**

The tool takes a crate/service name (e.g., "controlsd", "apid", "trackerd") and assembles:
- Entry point (main function)
- All HTTP routes defined in the service
- All env vars read by functions in this service
- Functions that call into OTHER crates (cross-service dependencies)
- Functions called BY other crates (who depends on this service)
- Security-tagged functions (auth, sinks, sanitizers, crypto)
- Test count and coverage summary
- File count and primary language

```go
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
			"required": ["service"]
		}`),
	}, s.handleExplainService)
}

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

	// Query all nodes whose file_path starts with the service directory
	// Using raw SQL via a helper since Store doesn't have a prefix query
	prefix := service + "/"
	allNodes, err := st.AllNodes(projName)
	if err != nil {
		return errResult(fmt.Sprintf("query nodes: %v", err)), nil
	}

	// Filter to service nodes
	var serviceNodes []*store.Node
	for _, n := range allNodes {
		if strings.HasPrefix(n.FilePath, prefix) {
			serviceNodes = append(serviceNodes, n)
		}
	}

	if len(serviceNodes) == 0 {
		return errResult(fmt.Sprintf("no nodes found for service %q — check the directory name", service)), nil
	}

	// Build the service ID set for cross-service analysis
	serviceIDs := make(map[int64]bool, len(serviceNodes))
	for _, n := range serviceNodes {
		serviceIDs[n.ID] = true
	}

	// Collect entry points
	var entryPoints []map[string]any
	for _, n := range serviceNodes {
		if n.Name == "main" && n.Label == "Function" {
			entryPoints = append(entryPoints, map[string]any{
				"name": n.Name, "file": n.FilePath, "line": n.StartLine,
			})
		}
	}

	// Collect routes
	var routes []map[string]any
	for _, n := range serviceNodes {
		if n.Label == "Route" {
			routes = append(routes, map[string]any{
				"name": n.Name, "file": n.FilePath,
			})
		}
	}

	// Collect env vars read by this service
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

	// Cross-service dependencies: functions in THIS service that call functions in OTHER services
	type crossDep struct {
		From       string `json:"from_function"`
		To         string `json:"to_function"`
		ToCrate    string `json:"to_crate"`
		EdgeType   string `json:"edge_type"`
	}
	var depsOut []crossDep
	var depsIn []crossDep
	depOutSeen := make(map[string]bool)
	depInSeen := make(map[string]bool)

	for _, n := range serviceNodes {
		if n.Label != "Function" && n.Label != "Method" {
			continue
		}
		// Outbound: this service calls other services
		for _, edgeType := range []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"} {
			edges, _ := st.FindEdgesBySourceAndType(n.ID, edgeType)
			for _, e := range edges {
				if serviceIDs[e.TargetID] {
					continue // same service, skip
				}
				target, _ := st.FindNodeByID(e.TargetID)
				if target == nil || target.FilePath == "" {
					continue
				}
				toCrate := extractTopLevelCrate(target.FilePath)
				key := n.Name + "->" + target.Name + "@" + toCrate
				if !depOutSeen[key] {
					depOutSeen[key] = true
					depsOut = append(depsOut, crossDep{
						From: n.Name, To: target.Name, ToCrate: toCrate, EdgeType: edgeType,
					})
				}
			}
		}
		// Inbound: other services call this service
		for _, edgeType := range []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"} {
			edges, _ := st.FindEdgesByTargetAndType(n.ID, edgeType)
			for _, e := range edges {
				if serviceIDs[e.SourceID] {
					continue
				}
				source, _ := st.FindNodeByID(e.SourceID)
				if source == nil || source.FilePath == "" {
					continue
				}
				fromCrate := extractTopLevelCrate(source.FilePath)
				key := source.Name + "@" + fromCrate + "->" + n.Name
				if !depInSeen[key] {
					depInSeen[key] = true
					depsIn = append(depsIn, crossDep{
						From: source.Name, To: n.Name, ToCrate: fromCrate, EdgeType: edgeType,
					})
				}
			}
		}
	}

	// Cap cross-deps for output size
	if len(depsOut) > 20 {
		depsOut = depsOut[:20]
	}
	if len(depsIn) > 20 {
		depsIn = depsIn[:20]
	}

	// Security surfaces in this service
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
	testCount := 0
	untestedFunctions := 0
	totalFunctions := 0
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
			ext := fileExtension(n.FilePath)
			langCounts[ext]++
		}
	}

	result := map[string]any{
		"service":     service,
		"file_count":  len(files),
		"node_count":  len(serviceNodes),
		"languages":   langCounts,
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
		result["depends_on_crates"] = uniqueCrates(depsOut)
	}
	if len(depsIn) > 0 {
		result["depended_by"] = depsIn
		result["depended_by_crates"] = uniqueCratesIn(depsIn)
	}
	if len(securitySurfaces) > 0 {
		result["security_surfaces"] = securitySurfaces
	}
	result["functions"] = totalFunctions
	result["test_edges"] = testCount
	result["untested_functions"] = untestedFunctions

	return jsonResult(result), nil
}

// fileExtension returns the file extension mapped to a language name.
func fileExtension(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return "other"
	}
	ext := path[idx:]
	switch ext {
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
	case ".css":
		return "CSS"
	case ".sh":
		return "Bash"
	default:
		return ext
	}
}

func uniqueCrates(deps []crossDep) []string {
	seen := make(map[string]bool)
	var result []string
	for _, d := range deps {
		if !seen[d.ToCrate] {
			seen[d.ToCrate] = true
			result = append(result, d.ToCrate)
		}
	}
	return result
}

func uniqueCratesIn(deps []crossDep) []string {
	seen := make(map[string]bool)
	var result []string
	for _, d := range deps {
		if !seen[d.ToCrate] {
			seen[d.ToCrate] = true
			result = append(result, d.ToCrate)
		}
	}
	return result
}
```

**Step 2: Verify compilation**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go vet ./internal/tools/`
Expected: No errors

---

### Task 2: service_map Tool

**Files:**
- Create: `internal/tools/service_map.go`
- Modify: `internal/tools/tools.go:439-455` (add registration)

**Step 1: Create `internal/tools/service_map.go`**

The tool:
- Finds all top-level crates that have a `main` function (= services)
- For each service, counts nodes, routes, security tags
- Classifies services into domains based on filename patterns and known naming conventions
- Shows inter-service edges (which services call which)

Domain classification heuristics:
- **navigation**: anavd, sbfd, modspewd, odometerd, garminaisd, compassd
- **perception**: trackerd, cropsd, adsbd, cloudadsbd, radd, libfuser, libtracker
- **autonomy**: controlsd, planner*, appliedd, trajd, paddled, ebrake
- **propulsion**: pentad, powerctl, powerd
- **safety**: saftd, capnd, alertgen
- **communications**: telem-bridge, pubmsg, zynoh, apid
- **recording**: doomper, mcapestry, mcap*, libreplay, foxglove*
- **management**: sysmanager, configd, stated, logmand, vmond, prometheusd, assetman, assetdiscovery
- **calibration**: calibration*, mast-bringup, septentrio-setup, bringup
- **infrastructure**: redacted-platform-terraform, nix, auth-gateway, headscale*
- **ui**: ship-os, sartv
- **library**: lib* (shared libs, not services)

```go
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

var domainPatterns = map[string][]string{
	"navigation":    {"anavd", "sbfd", "modspewd", "odometerd", "garminaisd", "compassd", "adsbd", "cloudadsbd"},
	"perception":    {"trackerd", "cropsd", "radd", "libfuser", "libtracker"},
	"autonomy":      {"controlsd", "planner", "appliedd", "trajd", "paddled", "ebrake"},
	"propulsion":    {"pentad", "powerctl", "powerd"},
	"safety":        {"saftd", "capnd", "alertgen"},
	"communications": {"telem-bridge", "pubmsg", "zynoh", "apid"},
	"recording":     {"doomper", "mcapestry", "mcap", "libreplay", "foxglove"},
	"management":    {"sysmanager", "configd", "stated", "logmand", "vmond", "prometheusd", "assetman", "assetdiscovery", "device-registry"},
	"calibration":   {"calibration", "mast-bringup", "septentrio", "bringup"},
	"infrastructure": {"redacted-platform-terraform", "nix", "auth-gateway", "headscale", "aws-auth"},
	"ui":            {"ship-os", "sartv"},
	"simulation":    {"libsim", "sim_core", "hitlman", "dojo"},
	"data":          {"mcs_rs", "gimme", "nominal", "torchyd"},
}

func classifyDomain(serviceName string) string {
	lower := strings.ToLower(serviceName)
	for domain, patterns := range domainPatterns {
		for _, p := range patterns {
			if lower == p || strings.HasPrefix(lower, p) {
				return domain
			}
		}
	}
	if strings.HasPrefix(lower, "lib") {
		return "library"
	}
	return "other"
}

func (s *Server) registerServiceMapTool() {
	s.addTool(&mcp.Tool{
		Name: "service_map",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Get a domain-organized map of all services in the codebase. Groups services into domains (navigation, perception, autonomy, propulsion, safety, communications, recording, management, calibration, infrastructure, simulation, data, ui). Shows each service's size, routes, security surfaces, and which other services it communicates with. Designed for understanding the overall system architecture.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				},
				"include_libraries": {
					"type": "boolean",
					"description": "Include shared libraries (lib*) in the map (default false — only services with main functions)."
				}
			}
		}`),
	}, s.handleServiceMap)
}

func (s *Server) handleServiceMap(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	includeLibs := getBoolArg(args, "include_libraries")

	allNodes, err := st.AllNodes(projName)
	if err != nil {
		return errResult(fmt.Sprintf("query nodes: %v", err)), nil
	}

	// Group nodes by top-level crate
	crateNodes := make(map[string][]*store.Node)
	for _, n := range allNodes {
		if n.FilePath == "" {
			continue
		}
		crate := extractTopLevelCrate(n.FilePath)
		if crate != "" {
			crateNodes[crate] = append(crateNodes[crate], n)
		}
	}

	// Determine which crates are services (have main function)
	type serviceInfo struct {
		Name            string   `json:"name"`
		Domain          string   `json:"domain"`
		NodeCount       int      `json:"nodes"`
		Functions       int      `json:"functions"`
		Routes          int      `json:"routes"`
		SecurityTagged  int      `json:"security_tagged"`
		HasMain         bool     `json:"has_main"`
		DependsOn       []string `json:"depends_on,omitempty"`
	}

	var services []serviceInfo

	for crate, nodes := range crateNodes {
		hasMain := false
		functions := 0
		routeCount := 0
		secTagged := 0

		for _, n := range nodes {
			if n.Name == "main" && n.Label == "Function" {
				hasMain = true
			}
			if n.Label == "Function" || n.Label == "Method" {
				functions++
			}
			if n.Label == "Route" {
				routeCount++
			}
			if n.Properties != nil {
				if _, ok := n.Properties["security_role"]; ok {
					secTagged++
				}
			}
		}

		if !hasMain && !includeLibs {
			// Check if it's a library worth showing
			domain := classifyDomain(crate)
			if domain != "library" && domain != "other" {
				// Domain-classified non-main crate — include anyway
			} else if !includeLibs {
				continue
			}
		}

		// Find cross-crate dependencies (which other crates this one calls)
		crateIDs := make(map[int64]bool, len(nodes))
		for _, n := range nodes {
			crateIDs[n.ID] = true
		}
		depCrates := make(map[string]bool)
		for _, n := range nodes {
			if n.Label != "Function" && n.Label != "Method" {
				continue
			}
			for _, edgeType := range []string{"CALLS", "HTTP_CALLS"} {
				edges, _ := st.FindEdgesBySourceAndType(n.ID, edgeType)
				for _, e := range edges {
					if crateIDs[e.TargetID] {
						continue
					}
					target, _ := st.FindNodeByID(e.TargetID)
					if target == nil || target.FilePath == "" {
						continue
					}
					toCrate := extractTopLevelCrate(target.FilePath)
					if toCrate != "" && toCrate != crate {
						depCrates[toCrate] = true
					}
				}
			}
			// Limit per-service dep scanning to avoid slowness
			if len(depCrates) > 15 {
				break
			}
		}

		var deps []string
		for d := range depCrates {
			deps = append(deps, d)
		}
		sort.Strings(deps)

		services = append(services, serviceInfo{
			Name:           crate,
			Domain:         classifyDomain(crate),
			NodeCount:      len(nodes),
			Functions:      functions,
			Routes:         routeCount,
			SecurityTagged: secTagged,
			HasMain:        hasMain,
			DependsOn:      deps,
		})
	}

	// Group by domain
	byDomain := make(map[string][]serviceInfo)
	for _, svc := range services {
		byDomain[svc.Domain] = append(byDomain[svc.Domain], svc)
	}
	// Sort within each domain by name
	for domain := range byDomain {
		sort.Slice(byDomain[domain], func(i, j int) bool {
			return byDomain[domain][i].Name < byDomain[domain][j].Name
		})
	}

	// Domain order
	domainOrder := []string{"navigation", "perception", "autonomy", "propulsion", "safety",
		"communications", "recording", "management", "calibration", "data",
		"simulation", "infrastructure", "ui", "library", "other"}

	type domainGroup struct {
		Domain   string        `json:"domain"`
		Services []serviceInfo `json:"services"`
		Count    int           `json:"count"`
	}
	var grouped []domainGroup
	for _, d := range domainOrder {
		if svcs, ok := byDomain[d]; ok && len(svcs) > 0 {
			grouped = append(grouped, domainGroup{
				Domain:   d,
				Services: svcs,
				Count:    len(svcs),
			})
		}
	}

	responseData := map[string]any{
		"domains":        grouped,
		"total_services": len(services),
		"total_domains":  len(grouped),
	}

	return jsonResult(responseData), nil
}
```

**Step 2: Verify compilation**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go vet ./internal/tools/`
Expected: No errors

---

### Task 3: diff_services Tool

**Files:**
- Create: `internal/tools/diff_services.go`
- Modify: `internal/tools/tools.go:439-455` (add registration)

**Step 1: Create `internal/tools/diff_services.go`**

The tool:
- Takes two git refs (e.g., "v1.0.0" and "HEAD", or a branch name)
- Runs `git diff --name-only` between them
- Groups changed files by top-level crate
- Reports: which services were added/modified, how many files changed per service, which security surfaces were in the diff

```go
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerDiffServicesTool() {
	s.addTool(&mcp.Tool{
		Name: "diff_services",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Compare two git refs and report which services changed, how many files per service, and which security-tagged files were modified. Designed for STIG reassessment: 'what changed since the last version we assessed?' Returns services grouped by change magnitude, plus a list of security-relevant changes.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"from_ref": {
					"type": "string",
					"description": "Starting git ref (tag, branch, or commit SHA). Example: 'v1.0.0', 'main~50', 'abc1234'"
				},
				"to_ref": {
					"type": "string",
					"description": "Ending git ref (default: 'HEAD')"
				},
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				}
			},
			"required": ["from_ref"]
		}`),
	}, s.handleDiffServices)
}

func (s *Server) handleDiffServices(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	fromRef := getStringArg(args, "from_ref")
	if fromRef == "" {
		return errResult("missing required 'from_ref' parameter"), nil
	}
	toRef := getStringArg(args, "to_ref")
	if toRef == "" {
		toRef = "HEAD"
	}

	// Validate refs (prevent injection)
	for _, ref := range []string{fromRef, toRef} {
		if strings.ContainsAny(ref, ";|&$`\\\"'<>(){}") {
			return errResult(fmt.Sprintf("invalid ref: %q", ref)), nil
		}
	}

	project := getStringArg(args, "project")
	effectiveProject := s.resolveProjectName(project)

	_, repoPath, _, resolveErr := s.resolveDetectRepo(effectiveProject)
	if resolveErr != nil {
		return resolveErr, nil
	}

	// Run git diff --name-status between refs
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "diff", "--name-status", fromRef+".."+toRef)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return errResult(fmt.Sprintf("git diff failed: %v", err)), nil
	}

	// Parse output into per-service changes
	type fileChange struct {
		Status string
		Path   string
	}

	var changes []fileChange
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			changes = append(changes, fileChange{Status: parts[0], Path: parts[1]})
		}
	}

	// Group by service (top-level directory)
	type serviceDiff struct {
		Name         string   `json:"name"`
		Domain       string   `json:"domain"`
		FilesChanged int      `json:"files_changed"`
		FilesAdded   int      `json:"files_added"`
		FilesDeleted int      `json:"files_deleted"`
		IsNew        bool     `json:"is_new,omitempty"`
		SecurityFiles []string `json:"security_files,omitempty"`
	}

	serviceChanges := make(map[string]*serviceDiff)
	securityPatterns := []string{"auth", "crypto", "tls", "cert", "security", "password", "token", "session", "permission", "opa", "policy"}

	for _, ch := range changes {
		crate := extractTopLevelCrate(ch.Path)
		if crate == "" {
			continue
		}
		if serviceChanges[crate] == nil {
			serviceChanges[crate] = &serviceDiff{
				Name:   crate,
				Domain: classifyDomain(crate),
			}
		}
		sd := serviceChanges[crate]
		sd.FilesChanged++
		switch ch.Status {
		case "A":
			sd.FilesAdded++
		case "D":
			sd.FilesDeleted++
		}

		// Check if file is security-relevant
		lower := strings.ToLower(ch.Path)
		for _, pat := range securityPatterns {
			if strings.Contains(lower, pat) {
				sd.SecurityFiles = append(sd.SecurityFiles, ch.Path)
				break
			}
		}
	}

	// Sort by files changed (most changed first)
	var sorted []serviceDiff
	for _, sd := range serviceChanges {
		sorted = append(sorted, *sd)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].FilesChanged > sorted[j].FilesChanged
	})

	// Identify new services (all files added)
	for i := range sorted {
		if sorted[i].FilesAdded == sorted[i].FilesChanged && sorted[i].FilesChanged > 0 {
			sorted[i].IsNew = true
		}
	}

	// Count security-impacted services
	securityImpacted := 0
	var allSecurityFiles []string
	for _, sd := range sorted {
		if len(sd.SecurityFiles) > 0 {
			securityImpacted++
			allSecurityFiles = append(allSecurityFiles, sd.SecurityFiles...)
		}
	}

	// Summary by domain
	domainSummary := make(map[string]int)
	for _, sd := range sorted {
		domainSummary[sd.Domain] += sd.FilesChanged
	}

	responseData := map[string]any{
		"from_ref":            fromRef,
		"to_ref":              toRef,
		"services_changed":    sorted,
		"total_services":      len(sorted),
		"total_files_changed": len(changes),
		"security_impacted":   securityImpacted,
		"domain_summary":      domainSummary,
	}

	if len(allSecurityFiles) > 0 {
		responseData["security_files"] = allSecurityFiles
		responseData["stig_hint"] = fmt.Sprintf("%d service(s) with security-relevant file changes. Review these for STIG reassessment impact.", securityImpacted)
	}

	return jsonResult(responseData), nil
}
```

**Step 2: Verify compilation**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go vet ./internal/tools/`
Expected: No errors

---

### Task 4: Register All Three Tools

**Files:**
- Modify: `internal/tools/tools.go:439-455`

**Step 1: Add registration calls**

Add these three lines before the closing `}` of `registerTools()`:

```go
	s.registerExplainServiceTool()
	s.registerServiceMapTool()
	s.registerDiffServicesTool()
```

**Step 2: Build and verify**

Run: `cd C:/Users/user/Documents/GitHub/codebase-memory-mcp && go vet ./internal/tools/ && CGO_ENABLED=1 go build -o bin/codebase-memory-mcp-test.exe ./cmd/codebase-memory-mcp/`
Expected: Clean build

**Step 3: Verify tools appear**

Run: `bin/codebase-memory-mcp-test.exe cli --help`
Expected: `explain_service`, `service_map`, `diff_services` in the tool list

**Step 4: Test explain_service on Corsair**

Run: `bin/codebase-memory-mcp-test.exe cli --raw explain_service '{"service": "controlsd", "project": "c-Users-user-Documents-GitHub-psm"}'`
Expected: JSON with entry_points, routes (if any), env_vars, depends_on, security_surfaces

**Step 5: Test service_map on Corsair**

Run: `bin/codebase-memory-mcp-test.exe cli --raw service_map '{"project": "c-Users-user-Documents-GitHub-psm"}'`
Expected: JSON with domains grouped, services listed per domain

**Step 6: Test diff_services on Corsair**

Run: `bin/codebase-memory-mcp-test.exe cli --raw diff_services '{"from_ref": "HEAD~20", "project": "c-Users-user-Documents-GitHub-psm"}'`
Expected: JSON with services_changed, security_files

**Step 7: Clean up test binary and replace live binary**

Run: `rm bin/codebase-memory-mcp-test.exe && CGO_ENABLED=1 go build -o C:/Users/user/bin/codebase-memory-mcp.exe ./cmd/codebase-memory-mcp/`

**Step 8: Commit and ship**

```bash
git checkout -b feat/service-understanding-tools
git add internal/tools/explain_service.go internal/tools/service_map.go internal/tools/diff_services.go internal/tools/tools.go
git commit -m "feat: add explain_service, service_map, and diff_services tools

- explain_service: comprehensive overview of a single service — entry points,
  routes, env vars, cross-service deps, security surfaces, test coverage
- service_map: domain-organized view of all services with inter-service
  communication edges (navigation, perception, autonomy, etc.)
- diff_services: service-level git diff between refs for STIG reassessment —
  which services changed, security-relevant files, domain summary

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"

git push -u origin feat/service-understanding-tools
gh pr create --title "feat: add service understanding tools" --repo redacted-org/code-graph
gh pr merge --auto --squash --delete-branch --repo redacted-org/code-graph
```
