// Tool registration: indexing, tracing, schema, and snippet tools.
//
// Split from tools.go without behaviour changes.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerIndexAndTraceTool() {
	s.addTool(&mcp.Tool{
		Name: "index_repository",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Index a repository into the code graph. Parses source files with tree-sitter, extracts functions/classes/modules, resolves call relationships (CALLS), HTTP/async cross-service links, and git change coupling (FILE_CHANGES_WITH). Supports incremental reindex via content hashing. Auto-sync keeps the graph fresh after initial indexing. If repo_path is omitted, uses the session project root. Use mode='fast' for large repos (>50K files) - skips generated code, test fixtures, and large files (>512KB) for faster indexing at the cost of coverage. Returns error if repo_path does not exist or contains no parseable source files.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"repo_path": {
					"type": "string",
					"description": "Absolute path to the repository to index. If omitted, uses the auto-detected session project root."
				},
				"mode": {
					"type": "string",
					"enum": ["full", "fast"],
					"description": "Indexing mode. 'full' (default): parse all supported files. 'fast': aggressive filtering — skips generated code, test fixtures, docs, large files (>512KB), and non-source assets for faster indexing of large repos."
				},
				"force": {
					"type": "boolean",
					"description": "Force full re-index, ignoring cached file hashes. Use after deploying new enrichment features to ensure all post-flush passes run. Default: false."
				},
				"write_report": {
					"type": "boolean",
					"description": "Write an ARCHITECTURE_REPORT.md orientation doc after indexing. Default: false. The report is written under the code-graph cache directory (<cache>/reports/<project>/) so the indexed checkout is never modified; set report_path to place it elsewhere. The choice is remembered per project."
				},
				"skip_report": {
					"type": "boolean",
					"description": "Legacy inverse of write_report, kept for compatibility: skip_report=false requests a report. Default: true (no report). Prefer write_report."
				},
				"report_path": {
					"type": "string",
					"description": "Where to write the report when one is requested. Default: <cache>/reports/<project>/ARCHITECTURE_REPORT.md. A relative path resolves under repo_path and an absolute path must be inside repo_path or the cache report directory; writing into the checkout is an explicit choice and makes the checkout differ from the indexed state until the file is committed or ignored."
				},
				"precision_tier": {
					"type": "string",
					"enum": ["heuristic", "scip"],
					"description": "Persistent per-project graph precision tier. 'heuristic' uses tree-sitter/static resolution. 'scip' replaces CALLS edges for compiler-index-covered functions using a SCIP index and reports exact coverage/drift telemetry. Omit to inherit the project's recorded choice; default heuristic."
				},
				"scip_index_path": {
					"type": "string",
					"description": "SCIP index path for precision_tier='scip'. Relative paths resolve under repo_path. Defaults to <repo>/index.scip."
				}
			}
		}`),
	}, s.handleIndexRepository)

	s.addTool(&mcp.Tool{
		Name: "trace_call_path",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: fmt.Sprintf(
			"Trace the call path of a function (who calls it, what it calls). Requires exact function name. Returns hop-by-hop callees/callers over the default call-like edge types (%s). USAGE and OVERRIDE are supported as opt-in non-call relationships through edge_types. Call-resolution confidence is calibrated only for unfiltered CALLS-only traces (edge_types=[CALLS], min_confidence=0); positive thresholds and every other edge selection report confidence as unknown/not applicable. If not found, returns similar name suggestions - use the qualified_name from suggestions to retry. Use depth=1 first, increase only if needed. Use direction='both' for full cross-service context - HTTP_CALLS from other services appear as inbound edges, so direction='outbound' alone misses them.",
			strings.Join(traceDefaultEdgeTypes[:], ", "),
		),
		InputSchema: withTraceEdgeTypes(json.RawMessage(`{
			"type": "object",
			"properties": {
				"function_name": {
					"type": "string",
					"description": "Name of the function to trace (e.g. 'ProcessOrder')"
				},
				"depth": {
					"type": "integer",
					"description": "Maximum BFS depth (1-5, default 3)"
				},
				"direction": {
					"type": "string",
					"description": "Traversal direction: 'outbound' (what it calls), 'inbound' (what calls it), or 'both'",
					"enum": ["outbound", "inbound", "both"]
				},
				"risk_labels": {
					"type": "boolean",
					"description": "Add risk classification (CRITICAL/HIGH/MEDIUM/LOW) based on hop depth. Hop 1=CRITICAL, 2=HIGH, 3=MEDIUM, 4+=LOW. Includes impact_summary with counts. Default false."
				},
				"min_confidence": {
					"type": "number",
					"minimum": 0,
					"maximum": 1,
					"description": "Minimum confidence threshold (0.0-1.0) for all selected edge types. Filters out low-confidence fuzzy matches. Edges with missing or null confidence remain traversable; an explicit numeric zero is filtered when the threshold is positive. Bands: high (>=0.7), medium (>=0.45), speculative (<0.45). Default 0.45 — filters speculative cross-crate name-only matches that frequently resolve to wrong-crate same-named methods. Pass 0 explicitly to disable filtering and see the full unfiltered trace. confidence_band is unknown whenever min_confidence is positive because the resolved-edge numerator is filtered while unresolved_call_count is not."
				},
				"project": {
					"type": "string",
					"description": "Project to trace in. Defaults to session project."
				},
				"include_source": {
					"type": "boolean",
					"description": "Inline source code for the root node and hop nodes under 50 lines. Makes trace results self-contained without follow-up get_code_snippet calls. Default: false."
				}
			},
			"required": ["function_name"]
		}`)),
	}, s.handleTraceCallPath)
}

func (s *Server) registerSchemaAndSnippetTools() {
	s.addTool(&mcp.Tool{
		Name: "get_graph_schema",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Return the schema of the indexed code graph: node label counts, edge type counts, relationship patterns (e.g. Function-CALLS->Function), and sample function/class names. Use to understand what's in the graph before querying.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project to get schema for. Defaults to session project."
				}
			}
		}`),
	}, s.handleGetGraphSchema)

	s.addTool(&mcp.Tool{
		Name: "get_code_snippet",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Retrieve source code for a function/class by name. Single mode: pass qualified_name (string). Batch mode: pass qualified_names (array of up to 10 strings) to fetch multiple snippets in one call - eliminates round trips after search_graph or trace_call_path. Returns source code, signature, return type, complexity, decorators, docstring, and caller/callee counts. Returns status='ambiguous' with suggestions when multiple matches found.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"qualified_name": {
					"type": "string",
					"description": "Name or qualified name of the function/class (single mode). Exact QN for precision, short name for discovery."
				},
				"qualified_names": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Array of names to resolve in batch (max 10). Each entry is resolved independently. Use after search_graph or trace_call_path to read multiple functions in one call."
				},
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				},
				"auto_resolve": {
					"type": "boolean",
					"description": "When true and <=2 ambiguous candidates exist, auto-pick the best match (highest degree, prefer non-test). Default: false."
				},
				"include_neighbors": {
					"type": "boolean",
					"description": "When true, include caller_names and callee_names arrays (up to 10 each) alongside the counts. Default: false."
				}
			}
		}`),
	}, s.handleGetCodeSnippet)
}
