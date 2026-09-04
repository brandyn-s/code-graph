// Tool registration: project management and index status tools.
//
// Split from tools.go without behaviour changes.
package tools

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerProjectTools registers tools for project management.
func (s *Server) registerProjectTools() {
	s.addTool(&mcp.Tool{
		Name: "compare_project_indexes",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true, IdempotentHint: true,
			OpenWorldHint: boolPtr(false), DestructiveHint: boolPtr(false),
		},
		Description: "Compare two immutable project indexes without switching global state. Reports deterministic file-content and declaration deltas with live index identities. This is an index snapshot comparison, not git history, ACL enforcement, or a continuously managed indexing fleet.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"base_project":{"type":"string","description":"Canonical base project name from list_projects."},
				"target_project":{"type":"string","description":"Canonical target project name from list_projects."},
				"limit":{"type":"integer","description":"Maximum returned entries per delta category, 1-500; default 100."}
			},
			"required":["base_project","target_project"]
		}`),
	}, s.handleCompareProjectIndexes)

	s.addTool(&mcp.Tool{
		Name: "localize_across_projects",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true, IdempotentHint: true,
			OpenWorldHint: boolPtr(false), DestructiveHint: boolPtr(false),
		},
		Description: "Run deterministic graph localization over multiple existing project indexes without merging their databases. Results are project-balanced and tagged with canonical project identity; raw graph scores are not treated as comparable across projects. Bounded to 25 indexes and intended for organization-wide discovery, not ACL enforcement. Verify any claim with project-bound source or relationship evidence.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"query":{"type":"string","description":"Symbol or focused relationship concept to localize."},
				"projects":{"type":"array","items":{"type":"string"},"description":"Optional canonical project names. Omit to search up to 25 indexed projects."},
				"seed_strategy":{"type":"string","enum":["substring","embedding","hybrid"],"description":"Seed matching strategy; default hybrid."},
				"depth":{"type":"integer","description":"Graph expansion depth 0-5; default 2."},
				"per_project_top_k":{"type":"integer","description":"Candidates per project 1-20; default 5."},
				"top_k":{"type":"integer","description":"Total project-balanced results 1-100; default 25."}
			},
			"required":["query"]
		}`),
	}, s.handleLocalizeAcrossProjects)

	s.addTool(&mcp.Tool{
		Name: "list_projects",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "List all indexed projects with their node/edge counts, indexed_at timestamps, root paths, and database file locations. Returns all projects in a single response (no pagination). Returns an empty array if no projects are indexed.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
	}, s.handleListProjects)

	s.addTool(&mcp.Tool{
		Name: "delete_project",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(true),
		},

		Description: "Delete an indexed project and all its graph data (nodes, edges, file hashes). Removes the project's .db file. This action is irreversible. Returns error if the project does not exist.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project_name": {
					"type": "string",
					"description": "Name of the project to delete"
				}
			},
			"required": ["project_name"]
		}`),
	}, s.handleDeleteProject)

	s.addTool(&mcp.Tool{
		Name: "index_status",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Check the indexing status of a project. Returns whether the project is indexed, currently indexing, or not found. Shows last indexed timestamp, node/edge counts, and whether the index is initial or incremental. Use this to check if the graph is ready for queries.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project name to check. Defaults to the auto-detected session project."
				}
			}
		}`),
	}, s.handleIndexStatus)
}

// --- Helpers ---
