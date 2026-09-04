// Tool registration: search and Cypher query tools.
//
// Split from tools.go without behaviour changes.
package tools

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerSearchTools() {
	s.registerSemanticSearchTool()
	s.registerSearchGraphTool()
	s.registerSearchCodeTool()
}

func (s *Server) registerSemanticSearchTool() {
	s.addTool(&mcp.Tool{
		Name: "search_code_semantic",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Semantic code search using the configured embedding provider (Voyage or any OpenAI-compatible endpoint). Find code by natural language description — 'authentication middleware', 'GPS parsing logic', 'battery monitoring'. Unlike search_code (grep) and search_graph (structural), this understands meaning. Requires an embedding provider (VOYAGE_API_KEY or CODE_GRAPH_EMBED_BASE_URL) and a prior index_repository run. Returns functions, classes, structs ranked by semantic similarity. Use file_pattern and label filters to narrow scope.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Natural language search query. Describe what the code does, not exact keywords."
				},
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				},
				"limit": {
					"type": "integer",
					"description": "Maximum results (default 10, max 50)"
				},
				"file_pattern": {
					"type": "string",
					"description": "Glob pattern to filter files (e.g. '*.rs', 'src/**')"
				},
				"label": {
					"type": "string",
					"description": "Node label filter: Function, Method, Class, Struct, Interface, Trait, Enum, Module, Type"
				}
			},
			"required": ["query"]
		}`),
	}, s.handleSearchCodeSemantic)
}

func (s *Server) registerSearchGraphTool() {
	s.addTool(&mcp.Tool{
		Name: "search_graph",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Search the code knowledge graph for functions, classes, modules, routes, and other code elements. Case-insensitive by default. Use regex alternatives for broad matching: 'handler|hdlr|ctrl'. Returns nodes with connectivity info (in/out degree), sorted by relevance. Supports filters: label, name_pattern (regex), qn_pattern, file_pattern (glob), relationship/direction/degree. Use max_degree=0 with exclude_entry_points=true for dead code detection. Returns 10 results per page (offset to paginate, has_more flag). Note: relationship filter counts edges (degree filtering) but does not return edges - use query_graph with Cypher for edge listings.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				},
				"label": {
					"type": "string",
					"description": "Node label filter: Function, Class, Module, Method, Interface, Enum, Type, File, Package, Folder, Route"
				},
				"name_pattern": {
					"type": "string",
					"description": "Regex pattern matched against the short node name. Case-insensitive by default. Supports full Go regex: '.*Handler$' (suffix), 'get|set|delete' (alternatives — no backslash before pipe), '^on[A-Z]' (prefix+char class). Best practice: include word variations in alternatives — 'auth|authenticate|authorization' (word forms), 'handler|hdlr|ctrl' (abbreviations), 'create|new|init' (synonyms). One regex with | replaces multiple separate searches."
				},
				"qn_pattern": {
					"type": "string",
					"description": "Regex pattern matched against the qualified name (full module path). Case-insensitive by default. Use to scope searches to directories/modules: '.*services\\.order\\..*' (order service), '.*tests\\..*' (test files only), '.*controller.*\\.handle.*' (handler methods in controllers). Combine with name_pattern for precise cross-cutting queries."
				},
				"file_pattern": {
					"type": "string",
					"description": "Glob pattern for file path within the project. Use to filter by directory ('**/services/**'), file extension ('*.py', '*.yaml'), or filename ('**/Makefile'). Essential for shared-repo projects where multiple languages coexist — e.g., use '*.html' to find only HTML files in a JavaScript project."
				},
				"relationship": {
					"type": "string",
					"description": "Filter by relationship type: CALLS, HTTP_CALLS, ASYNC_CALLS, IMPORTS, DEFINES, DEFINES_METHOD, HANDLES, CONTAINS_FILE, CONTAINS_FOLDER, CONTAINS_PACKAGE, IMPLEMENTS"
				},
				"direction": {
					"type": "string",
					"description": "Edge direction for degree filters: 'inbound', 'outbound', or 'any'",
					"enum": ["inbound", "outbound", "any"]
				},
				"min_degree": {
					"type": "integer",
					"description": "Minimum edge count (e.g. 10 for high fan-out functions)"
				},
				"max_degree": {
					"type": "integer",
					"description": "Maximum edge count (e.g. 0 for dead code detection)"
				},
				"min_complexity": {
					"type": "integer",
					"description": "Minimum cyclomatic complexity (e.g. 10 to surface gnarly functions for documentation/refactor focus). Nodes without a complexity property are excluded when this filter is set."
				},
				"max_complexity": {
					"type": "integer",
					"description": "Maximum cyclomatic complexity (e.g. 5 to surface simple candidates for early documentation). Nodes without a complexity property are excluded when this filter is set."
				},
				"exclude_entry_points": {
					"type": "boolean",
					"description": "Exclude entry points (route handlers, main(), framework-registered functions) from results. Use with max_degree=0 for accurate dead code detection."
				},
				"limit": {
					"type": "integer",
					"description": "Max results per page (default: 10). Use small limits and paginate with offset — response includes has_more flag."
				},
				"offset": {
					"type": "integer",
					"description": "Skip N results for pagination (default: 0). Check has_more in response to know if more pages exist."
				},
				"include_connected": {
					"type": "boolean",
					"description": "Include connected node names in results (default: false). Expensive — only enable when you need to see neighbor names."
				},
				"exclude_labels": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Labels to exclude from results. Community nodes are excluded by default — pass [] to include them."
				},
				"sort_by": {
					"type": "string",
					"enum": ["relevance", "name", "degree"],
					"description": "Sort order. Default: relevance (exact match first, prefix match second, then by connectivity)"
				},
				"case_sensitive": {
					"type": "boolean",
					"description": "Match patterns case-sensitively. Default: false (case-insensitive). Set true for exact case matching."
				},
				"include_source": {
					"type": "boolean",
					"description": "Inline source code for functions/classes under 50 lines. Eliminates follow-up get_code_snippet calls. Default: false. Also includes node properties (signature, return_type, decorators) in results."
				}
			}
		}`),
	}, s.handleSearchGraph)
}

func (s *Server) registerSearchCodeTool() {
	s.addTool(&mcp.Tool{
		Name: "search_code",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Search for text in source code files (like grep, scoped to indexed project). Case-insensitive by default. With regex=true, use alternatives for broad matching: 'TODO|FIXME|HACK'. Returns matching lines with file path, line number, and surrounding context. Returns 10 matches per page (offset to paginate, has_more flag). Use for string literals, error messages, TODO comments, config values, import statements. Prefer search_graph for finding functions/classes by structural name.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Text to search for. Case-insensitive by default. Literal string match unless regex=true. With regex=true: Go regex syntax (no backslash before pipe). Best practice: use alternatives for word form variance — 'deprecat|obsolete|legacy' catches 'deprecated', 'deprecating', 'obsolete', etc. A partial stem with alternatives is more effective than an exact word."
				},
				"file_pattern": {
					"type": "string",
					"description": "Glob pattern to filter files (e.g. '*.go', '*.py', '*.toml'). Use to focus search on specific file types or directories."
				},
				"regex": {
					"type": "boolean",
					"description": "Treat pattern as a regular expression (default: false)"
				},
				"max_results": {
					"type": "integer",
					"minimum": 1,
					"maximum": 1000,
					"description": "Max matches per page (default: 10, max: 1000). Response includes has_more flag for pagination."
				},
				"offset": {
					"type": "integer",
					"minimum": 0,
					"maximum": 1000000,
					"description": "Skip N matches for pagination (default: 0). Check has_more in response."
				},
				"case_sensitive": {
					"type": "boolean",
					"description": "Match case-sensitively. Default: false (case-insensitive). Set true for exact case matching."
				},
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				}
			},
			"required": ["pattern"]
		}`),
	}, s.handleSearchCode)
}

func (s *Server) registerQueryTool() {
	s.addTool(&mcp.Tool{
		Name: "query_graph",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Execute a Cypher-like graph query (read-only subset). String matching is case-sensitive; use =~ '(?i)pattern' for case-insensitive regex. Supports MATCH, WHERE, RETURN, ORDER BY, LIMIT, DISTINCT, variable-length paths (*1..3). WHERE comparison operators: =, <>, <, >, <=, >=, =~ (regex), STARTS WITH, ENDS WITH, CONTAINS, IS NULL, IS NOT NULL, IN [list], AND, OR (one boolean operator per WHERE clause — mixed AND/OR is rejected; no precedence or parentheses). Aggregations: COUNT(*) (count all rows), COUNT(var) (count non-null bindings of a variable), COUNT(DISTINCT var) (count unique bindings), COUNT(DISTINCT var.prop) (count unique property values). Built-in functions: labels(node) returns the node's label as a single-element array. Write keywords (CREATE, DELETE, SET, MERGE, REMOVE) are rejected at parse with a clear error — read-only is enforced at parse time. DEFAULT ROW CAP IS 200 — pass max_rows (up to 10000) to raise it. Response includes 'effective_cap' always and 'capped: true' when results were truncated (check this before trusting totals on large result sets). Best for relationship patterns, filtered joins, path queries, and edge property filtering. Filterable edge properties: r.confidence, r.url_path, r.method, r.confidence_band, r.validated_by_trace, r.coupling_score. Edge types: CALLS, HTTP_CALLS, ASYNC_CALLS, IMPORTS, DEFINES, IMPLEMENTS, OVERRIDE, USAGE, FILE_CHANGES_WITH. Always use LIMIT. KNOWN ACCURACY BANDS (measured via bench/accuracy/, 2026-04-24, PyCG/Jedi/syn/go-ast oracles; ±35% oracle-class uncertainty per Jedi-vs-PyCG comparison): Python CALLS scope-aligned F1 ~0.54-0.99 (highly fixture-dependent — top-level packages score high, nested src/ layouts lower). Rust CALLS scope-aligned F1 ~0.82-0.91 across 3 fixtures (services, trait-heavy lib, utility lib). Go CALLS scope-aligned F1 ~0.54-0.68 across 3 fixtures (self-host, cobra, gin — non-self-hosted fixtures run ~10pp lower). IMPORTS: Python 0.94-0.96 after nested-package resolver fix; Rust sparse (resolver gap on `use crate::...` paths). Indirect calls (closures, fn pointers, trait objects) are NOT in the graph — code-graph's extractor doesn't emit CALLS edges for higher-order dispatch.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Cypher query, e.g. MATCH (f:Function)-[:CALLS]->(g:Function) WHERE f.name = 'main' RETURN g.name, g.qualified_name LIMIT 20"
				},
				"project": {
					"type": "string",
					"description": "Project to query. Defaults to session project."
				},
				"max_rows": {
					"type": "integer",
					"description": "Maximum result rows (default 200, max 10000). Overrides the internal row cap. Use higher values for COUNT/aggregation queries on large codebases."
				}
			},
			"required": ["query"]
		}`),
	}, s.handleQueryGraph)
}
