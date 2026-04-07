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
	"navigation":     {"anavd", "sbfd", "sbfmon", "modspewd", "odometerd", "garminaisd", "compassd", "adsbd", "cloudadsbd", "nmeaconvd", "seastated"},
	"perception":     {"trackerd", "cropsd", "radd", "libfuser", "libtracker", "fuserd", "cameractl", "procrustesd"},
	"autonomy":       {"controlsd", "planner", "appliedd", "trajd", "paddled", "ebrake", "missiond", "emcond", "emconctl", "fthrottle", "gandropd"},
	"propulsion":     {"pentad", "powerctl", "powerd", "xcpwrctl", "pdmctl"},
	"safety":         {"saftd", "capnd", "alertgen", "battmond"},
	"communications": {"telem-bridge", "pubmsg", "zynoh", "apid", "mithril-apid", "takd", "submsg", "snarfd", "canstatd"},
	"recording":      {"doomper", "mcapestry", "mcap", "libreplay", "foxglove", "luplog", "replayer"},
	"management":     {"sysmanager", "configd", "stated", "logmand", "vmond", "vmon", "prometheusd", "assetman", "assetdiscovery", "device-registry", "hvacd", "netmand", "reloadd", "swarmd", "cluster-swarmd", "sysman-sidecar", "redacted-platform-manager", "remediated", "sysdiag"},
	"calibration":    {"calibration", "mast-bringup", "septentrio", "bringup", "ball-bringup", "ball-api", "registration-helper", "vitesse-switch", "thales_vlink_flasher"},
	"infrastructure": {"redacted-platform-terraform", "terraform", "nix", "nxb", "auth-gateway", "headscale", "aws-auth", "ssh-cert", "release-tools", "release-image-store", "isengard"},
	"ui":             {"ship-os", "sartv", "mirror-galadriel", "voyeur", "uxv"},
	"simulation":     {"libsim", "sim_core", "hitlman", "dojo", "simd", "seaval", "hitl-tests"},
	"data":           {"mcs_rs", "gimme", "nominal", "torchyd", "torchy-compare", "torchy-regression", "ml-data", "ml-datasets", "ml-embedding", "ml-train", "boat-tokenizer", "timemachine"},
	"testing":        {"test-reader", "test-writer", "test-scenarios"},
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
		Description: "Get a domain-organized map of all services in the codebase. Groups services into domains (navigation, perception, autonomy, propulsion, safety, communications, recording, management, calibration, data, simulation, infrastructure, ui, testing). Shows each service's size, routes, security surfaces, and which other services it communicates with. Cross-service dependencies are filtered to medium+ confidence edges only.",
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

//nolint:gocognit,cyclop // multi-phase service topology — entry points, dependencies, cross-service calls, graph construction
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

		// Cross-crate dependencies (limited scan, confidence-filtered + blocklist)
		const svcMapMinConf = 0.3

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
					if edgeConfidence(e) < svcMapMinConf {
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
		"simulation", "infrastructure", "ui", "testing", "library", "other"}

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
