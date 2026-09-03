package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Service domain classification.
//
// `service_map` groups top-level crates/packages into domains by matching
// their names against a pattern table. The table is user-configurable because
// service naming conventions are organization-specific:
//
//   - CODE_GRAPH_SERVICE_MAP=<path>            explicit JSON file
//   - <user config dir>/code-graph/service_map.json  (e.g. ~/.config/code-graph/)
//   - otherwise the small generic default below
//
// File format: {"<domain>": ["<pattern>", ...], ...}. A pattern is an exact
// name, a prefix ("api*" or bare "api"), or a suffix ("*d", "*-service").
// The most specific (longest) matching pattern wins; ties break on domain
// name so classification is deterministic. Unmatched names fall back to
// "library" for lib* and "other" otherwise. See docs/service-map.md.

// domainPattern is one compiled classification rule.
type domainPattern struct {
	domain  string
	pattern string // original pattern text, used for specificity
	exact   string
	prefix  string
	suffix  string
}

// defaultDomainPatterns is intentionally generic: naming-convention based,
// no organization-specific service names.
var defaultDomainPatterns = map[string][]string{
	"library":        {"lib*", "*-lib", "*-sdk", "*-client"},
	"service":        {"*d", "*daemon", "*-service", "*-server", "*-api", "*-worker"},
	"tooling":        {"*ctl", "*-cli", "*cli", "*-tool", "*-tools", "scripts"},
	"testing":        {"test*", "*-test", "*_test", "*-tests", "e2e*", "integration*"},
	"infrastructure": {"terraform", "infra*", "nix", "docker", "k8s", "kubernetes", "helm", "ansible", "deploy*"},
	"ui":             {"web", "web-*", "ui", "ui-*", "frontend", "*-frontend", "*-ui", "*-web"},
	"data":           {"data*", "*-data", "ml-*", "*-ml", "etl*", "*-pipeline"},
}

// ServiceMapEnv names the environment variable holding an explicit
// service-map JSON path.
const ServiceMapEnv = "CODE_GRAPH_SERVICE_MAP"

var (
	domainPatternsOnce sync.Once
	domainPatterns     []domainPattern
)

// compileDomainPatterns turns the {domain: [pattern]} table into rules with a
// stable order (domain name, then pattern text).
func compileDomainPatterns(table map[string][]string) []domainPattern {
	domains := make([]string, 0, len(table))
	for d := range table {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	var out []domainPattern
	for _, d := range domains {
		patterns := append([]string(nil), table[d]...)
		sort.Strings(patterns)
		for _, raw := range patterns {
			pat := strings.ToLower(strings.TrimSpace(raw))
			if pat == "" || pat == "*" {
				continue
			}
			dp := domainPattern{domain: d, pattern: pat}
			switch {
			case strings.HasPrefix(pat, "*") && strings.HasSuffix(pat, "*") && len(pat) > 2:
				// "*foo*" — treat as substring via prefix+suffix scan below
				dp.prefix = ""
				dp.suffix = ""
				dp.exact = ""
				dp.pattern = pat
			case strings.HasPrefix(pat, "*"):
				dp.suffix = pat[1:]
			case strings.HasSuffix(pat, "*"):
				dp.prefix = pat[:len(pat)-1]
			default:
				// Bare names match exactly or as a prefix (historical behavior).
				dp.exact = pat
				dp.prefix = pat
			}
			out = append(out, dp)
		}
	}
	return out
}

func (dp domainPattern) matches(lower string) bool {
	if dp.exact != "" && lower == dp.exact {
		return true
	}
	if dp.prefix != "" && strings.HasPrefix(lower, dp.prefix) {
		return true
	}
	if dp.suffix != "" && strings.HasSuffix(lower, dp.suffix) && lower != dp.suffix {
		return true
	}
	if strings.HasPrefix(dp.pattern, "*") && strings.HasSuffix(dp.pattern, "*") && len(dp.pattern) > 2 {
		return strings.Contains(lower, dp.pattern[1:len(dp.pattern)-1])
	}
	return false
}

// loadDomainPatterns resolves the pattern table from the environment, a user
// config file, or the built-in default. Errors reading a configured file are
// logged and fall back to the default so a bad config never breaks indexing.
func loadDomainPatterns(getenv func(string) string) []domainPattern {
	path := strings.TrimSpace(getenv(ServiceMapEnv))
	explicit := path != ""
	if !explicit {
		if dir, err := os.UserConfigDir(); err == nil {
			path = filepath.Join(dir, "code-graph", "service_map.json")
		}
	}
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			var table map[string][]string
			if jsonErr := json.Unmarshal(data, &table); jsonErr != nil {
				slog.Warn("service_map.config.invalid", "path", path, "err", jsonErr)
			} else if len(table) > 0 {
				slog.Info("service_map.config.loaded", "path", path, "domains", len(table))
				return compileDomainPatterns(table)
			}
		case explicit:
			slog.Warn("service_map.config.unreadable", "path", path, "err", err)
		}
	}
	return compileDomainPatterns(defaultDomainPatterns)
}

func activeDomainPatterns() []domainPattern {
	domainPatternsOnce.Do(func() {
		domainPatterns = loadDomainPatterns(os.Getenv)
	})
	return domainPatterns
}

// classifyDomain maps a crate/service name to a domain using the active
// pattern table. The longest matching pattern wins; unmatched lib* names are
// "library" and everything else is "other".
func classifyDomain(serviceName string) string {
	return classifyDomainWith(activeDomainPatterns(), serviceName)
}

func classifyDomainWith(patterns []domainPattern, serviceName string) string {
	lower := strings.ToLower(strings.TrimSpace(serviceName))
	if lower == "" {
		return "other"
	}
	best := ""
	bestLen := -1
	for _, dp := range patterns {
		if !dp.matches(lower) {
			continue
		}
		if l := len(dp.pattern); l > bestLen || (l == bestLen && dp.domain < best) {
			best, bestLen = dp.domain, l
		}
	}
	if best != "" {
		return best
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
		Description: "Get a domain-organized map of all services in the codebase. Groups services into domains by name pattern (default table: service, library, tooling, testing, infrastructure, ui, data; override with a CODE_GRAPH_SERVICE_MAP JSON file, see docs/service-map.md). Shows each service's size, routes, security surfaces, and which other services it communicates with. Cross-service dependencies are filtered to medium+ confidence edges only.",
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

//nolint:gocognit,cyclop,funlen // multi-phase service topology — entry points, dependencies, cross-service calls, graph construction
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
		"_metadata":      s.stdReadGraphMetadata(projName),
	}

	return jsonResult(responseData), nil
}
