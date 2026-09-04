// Tool registration: architecture and graph tools.
//
// Split from tools.go without behaviour changes.
package tools

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerArchitectureTools() {
	s.addTool(&mcp.Tool{
		Name: "get_architecture",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Get codebase architecture overview computed from the code graph. Call with aspects=['all'] for full orientation or select specific aspects. Available aspects: languages, packages (fan-in/out), entry_points, routes (HTTP endpoints), hotspots (most-called), boundaries (cross-package calls), services (cross-service links), layers (heuristic), clusters (Louvain community detection), file_tree, adr (stored Architecture Decision Record). Recommended first call when exploring an unfamiliar codebase.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"aspects": {
					"type": "array",
					"items": {"type": "string", "enum": ["all", "languages", "packages", "entry_points", "routes", "hotspots", "boundaries", "services", "layers", "clusters", "file_tree", "adr"]},
					"description": "Which architecture aspects to return. Default: ['all']. Use specific aspects to reduce output: ['languages', 'packages'] for quick orientation, ['hotspots', 'boundaries'] for dependency analysis, ['clusters'] for community detection across CALLS/HTTP/ASYNC edges."
				},
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				}
			}
		}`),
	}, s.handleGetArchitecture)

	s.addTool(&mcp.Tool{
		Name: "manage_adr",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(true),
		},

		Description: "Manage the Architecture Decision Record (ADR) for a project. This tool writes ADR files into the repository checkout by design and is the only code-graph tool that modifies the checkout without an explicit path argument; the change shows up in git status and in the index identity until committed. CRUD operations for a persistent, section-based architectural summary. Modes: get (retrieve, optional include filter), store (create/replace - all 6 sections required), update (patch sections, unmentioned preserved), delete (remove ADR - this is irreversible), auto (compute from indexed graph and store - no content arg needed). Fixed sections: PURPOSE, STACK, ARCHITECTURE, PATTERNS, TRADEOFFS, PHILOSOPHY. Max 8000 chars. Validation: store rejects missing sections; update rejects non-canonical keys. Use include=['STACK','PATTERNS'] with get to reduce token usage.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"mode": {
					"type": "string",
					"enum": ["get", "store", "update", "delete", "auto"],
					"description": "Operation: 'get' retrieves ADR, 'store' creates/replaces (all 6 sections required), 'update' patches sections (canonical keys only), 'delete' removes, 'auto' computes from indexed graph and stores (no content needed)."
				},
				"project": {
					"type": "string",
					"description": "Project name. Defaults to session project."
				},
				"content": {
					"type": "string",
					"description": "Full ADR markdown (required for mode='store'). Must contain all 6 ## SECTION headers: PURPOSE, STACK, ARCHITECTURE, PATTERNS, TRADEOFFS, PHILOSOPHY. Missing sections will be rejected."
				},
				"sections": {
					"type": "object",
					"additionalProperties": {"type": "string"},
					"description": "Section updates (required for mode='update'). Keys must be canonical section names (PURPOSE, STACK, ARCHITECTURE, PATTERNS, TRADEOFFS, PHILOSOPHY). Non-canonical keys are rejected. Values are new content. Unmentioned sections preserved."
				},
				"include": {
					"type": "array",
					"items": {"type": "string", "enum": ["PURPOSE", "STACK", "ARCHITECTURE", "PATTERNS", "TRADEOFFS", "PHILOSOPHY"]},
					"description": "Section filter for mode='get'. Returns only the listed sections instead of the full ADR. Example: ['STACK', 'PATTERNS'] returns ~800 chars instead of ~8000. Omit to get all sections."
				}
			},
			"required": ["mode"]
		}`),
	}, s.handleManageADR)
}

// registerGraphTools registers tools for graph querying, searching, and tracing.
func (s *Server) registerGraphTools() {
	s.registerIndexAndTraceTool()
	s.registerSchemaAndSnippetTools()
	s.registerSearchTools()
	s.registerQueryTool()
}
