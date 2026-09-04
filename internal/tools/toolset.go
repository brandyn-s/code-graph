package tools

import (
	"sort"
	"strings"

	"github.com/brandyn-s/code-graph/internal/config"
)

// Toolsets control how many tools the MCP server advertises. Every tool is
// always registered internally (the CLI and the schema snapshot see all of
// them); the toolset only decides what tools/list returns to a client. Fewer
// advertised tools means less schema in every request and better tool
// selection by agents, which is why "core" is the default.
const (
	// ToolsetCore advertises the tools the plugin skills, the benchmark arm
	// contracts, and the agent-effectiveness battery rely on, plus the
	// indexing, status, and evidence essentials.
	ToolsetCore = "core"
	// ToolsetFull advertises every registered tool.
	ToolsetFull = "full"
)

// coreToolNames is the "core" toolset. Membership is derived from three
// consumers and must stay a superset of each:
//   - codebase-search-plugin skills (skills/*/SKILL.md, references/*.md)
//   - codebase-search-plugin bench/compare ARM_CONTRACTS allowlists
//   - bench/research/agent-effectiveness category-6 battery
//
// plus index_health as the status essential. Adding a tool here requires
// regenerating the schema snapshot (generate_schemas.py) so CI sees the
// change.
var coreToolNames = map[string]bool{
	// Indexing, status, project lifecycle.
	"index_repository":        true,
	"index_status":            true,
	"index_health":            true,
	"list_projects":           true,
	"delete_project":          true,
	"compare_project_indexes": true,
	// Discovery and reading.
	"search_graph":     true,
	"search_code":      true,
	"query_graph":      true,
	"get_graph_schema": true,
	"get_code_snippet": true,
	"degree_filter":    true,
	// Relationships, evidence, impact.
	"trace_call_path":           true,
	"trace_data_flow":           true,
	"get_relationship_evidence": true,
	"detect_changes":            true,
	"get_architecture":          true,
	// Localization.
	"code_localize":            true,
	"localize_across_projects": true,
	// Security and compliance evidence.
	"query_security_surfaces": true,
	"query_stig_evidence":     true,
	// Benchmark-arm contract members.
	"generate_report": true,
	"ingest_traces":   true,
	"manage_adr":      true,
}

// ActiveToolset returns the toolset selected by CODE_GRAPH_TOOLSET, defaulting
// to core. Unknown values fall back to core so a typo never silently exposes
// the full surface.
func ActiveToolset() string {
	switch strings.ToLower(strings.TrimSpace(config.Get(config.Toolset))) {
	case ToolsetFull:
		return ToolsetFull
	default:
		return ToolsetCore
	}
}

// toolsetIncludes reports whether a tool is advertised under the given toolset.
func toolsetIncludes(toolset, name string) bool {
	if toolset == ToolsetFull {
		return true
	}
	return coreToolNames[name]
}

// CoreToolNames returns the sorted core toolset.
func CoreToolNames() []string {
	names := make([]string, 0, len(coreToolNames))
	for name := range coreToolNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
