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
	"navigation":     {"anavd", "sbfd", "modspewd", "odometerd", "garminaisd", "compassd", "adsbd", "cloudadsbd"},
	"perception":     {"trackerd", "cropsd", "radd", "libfuser", "libtracker"},
	"autonomy":       {"controlsd", "planner", "appliedd", "trajd", "paddled", "ebrake"},
	"propulsion":     {"pentad", "powerctl", "powerd"},
	"safety":         {"saftd", "capnd", "alertgen"},
	"communications": {"telem-bridge", "pubmsg", "zynoh", "apid"},
	"recording":      {"doomper", "mcapestry", "mcap", "libreplay", "foxglove"},
	"management":     {"sysmanager", "configd", "stated", "logmand", "vmond", "prometheusd", "assetman", "assetdiscovery", "device-registry"},
	"calibration":    {"calibration", "mast-bringup", "septentrio", "bringup"},
	"infrastructure": {"redacted-platform-terraform", "nix", "auth-gateway", "headscale", "aws-auth"},
	"ui":             {"ship-os", "sartv"},
	"simulation":     {"libsim", "sim_core", "hitlman", "dojo"},
	"data":           {"mcs_rs", "gimme", "nominal", "torchyd"},
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

	type serviceInfo struct {
		Name           string   `json:"name"`
		Domain         string   `json:"domain"`
		NodeCount      int      `json:"nodes"`
		Functions      int      `json:"functions"`
		Routes         int      `json:"routes"`
		SecurityTagged int      `json:"security_tagged"`
		HasMain        bool     `json:"has_main"`
		DependsOn      []string `json:"depends_on,omitempty"`
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

		domain := classifyDomain(crate)
		if !hasMain && !includeLibs {
			if domain == "library" || domain == "other" {
				continue
			}
		}

		// Cross-crate dependencies (limited scan)
		crateIDs := make(map[int64]bool, len(nodes))
		for _, n := range nodes {
			crateIDs[n.ID] = true
		}
		depCrates := make(map[string]bool)
		scanned := 0
		for _, n := range nodes {
			if n.Label != "Function" && n.Label != "Method" {
				continue
			}
			if scanned > 200 {
				break
			}
			scanned++
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
			Domain:         domain,
			NodeCount:      len(nodes),
			Functions:      functions,
			Routes:         routeCount,
			SecurityTagged: secTagged,
			HasMain:        hasMain,
			DependsOn:      deps,
		})
	}

	byDomain := make(map[string][]serviceInfo)
	for _, svc := range services {
		byDomain[svc.Domain] = append(byDomain[svc.Domain], svc)
	}
	for domain := range byDomain {
		sort.Slice(byDomain[domain], func(i, j int) bool {
			return byDomain[domain][i].Name < byDomain[domain][j].Name
		})
	}

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
