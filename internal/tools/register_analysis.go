// Tool registration: rationale, graph diff, similarity, ranking, localization, reports.
//
// Split from tools.go without behaviour changes.
package tools

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerFindRationaleTool surfaces the Rationale nodes produced by
// passRationale. Primary use: compliance audits ("list every SAFETY:
// justification across the Rust code") and code-review context.
func (s *Server) registerFindRationaleTool() {
	s.addTool(&mcp.Tool{
		Name: "find_rationale",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Return WHY/NOTE/HACK/SAFETY/TODO/FIXME/IMPORTANT/XXX comment annotations extracted from source, with their enclosing Function/Method/Class subject and file:line location. Filter by kind to audit a specific marker category. Useful for compliance passes (every unsafe justification, every documented HACK) and for surfacing design rationale in code review.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"kind": {
					"type": "string",
					"description": "Filter by marker kind. One of WHY, NOTE, HACK, SAFETY, TODO, FIXME, IMPORTANT, XXX. Omit to return all kinds."
				},
				"limit": {
					"type": "integer",
					"description": "Max rationale entries to return (1-500, default 50)"
				}
			}
		}`),
	}, s.handleFindRationale)
}

// registerDiffGraphTool surfaces diff_graph — symbol-level delta
// between two arbitrary git revisions, complementing detect_changes
// (which is scoped to uncommitted / staged / branch-vs-branch flows).
func (s *Server) registerDiffGraphTool() {
	s.addTool(&mcp.Tool{
		Name: "diff_graph",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Given two git revisions, list which indexed symbols (Function/Method/Class/Struct/Interface/Trait/Enum) live in the files touched between them. Complements detect_changes (uncommitted / staged / branch) by accepting arbitrary SHAs — useful for 'what did we ship between v1.2.0 and v1.3.0?' review. Current index only: symbols deleted after to_sha cannot be surfaced.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"from_sha": {
					"type": "string",
					"description": "Starting revision: commit SHA (short or full), branch name, or HEAD~N"
				},
				"to_sha": {
					"type": "string",
					"description": "Ending revision: commit SHA (short or full), branch name, or HEAD"
				}
			},
			"required": ["from_sha", "to_sha"]
		}`),
	}, s.handleDiffGraph)
}

// registerFindSimilarFunctionsTool adds find_similar_functions — cosine
// top-K over Voyage embeddings, primary use case: "is this function's
// logic duplicated elsewhere?"
func (s *Server) registerFindSimilarFunctionsTool() {
	s.addTool(&mcp.Tool{
		Name: "find_similar_functions",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Return the top-K functions/methods most cosine-similar to a given function's embedding. Useful for finding refactor candidates (two functions solving the same problem without a shared call path) and duplicated patterns. Requires embeddings to be populated — run index_repository with an embedding provider configured (VOYAGE_API_KEY or CODE_GRAPH_EMBED_BASE_URL) first.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Function name (exact Name match) or fully-qualified name. Ambiguous names produce a picker error listing candidates."
				},
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"limit": {
					"type": "integer",
					"description": "Max number of matches to return (1-50, default 10)"
				},
				"threshold": {
					"type": "number",
					"description": "Minimum cosine similarity score (0.0-1.0) — common values: 0.85 for \"worth investigating\", 0.92 for \"probable copy-paste\". Default 0.0 (return top-K regardless of score)."
				}
			},
			"required": ["name"]
		}`),
	}, s.handleFindSimilarFunctions)
}

// registerRankByQueryTool adds rank_by_query — bidirectional weighted
// PageRank with personalization on query-matched seed nodes. Primary use
// case: agent context selection — "give me the top-20 most relevant
// entities for this issue/question" — typically reducing context tokens
// by 3-5x vs dumping the full graph. Reference: Aider repo-map pattern
// (https://aider.chat/2023/10/22/repomap.html).
func (s *Server) registerRankByQueryTool() {
	s.addTool(&mcp.Tool{
		Name: "rank_by_query",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Rank graph nodes by relevance to a query using bidirectional weighted PageRank. Best with specific symbol queries (function/class names): substring seeds match exactly, embedding seeds catch semantic neighbors. WORKS POORLY on long natural-language descriptions where surviving tokens are noise words (e.g., 'the install command runs lazily' tokenizes to 'install/command/runs/lazily', each matching dozens of unrelated symbols). For verbose issue descriptions, use code_localize_agent instead — the LLM-driven variant reasons about call paths rather than substring-matching. Algorithm: tokenize, seed via seed_strategy, run forward+reverse PageRank, return top-K by combined score. Bidirectional avoids pure-source collapse. Typical 3-5x token reduction vs dumping the full graph for context selection.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Natural-language query or symbol list. Tokens shorter than 2 chars and English stopwords (the/of/how/etc.) are dropped. At least one token must match a node Name or QualifiedName substring."
				},
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"top_k": {
					"type": "integer",
					"description": "Maximum number of ranked nodes to return (1-200, default 20). Higher values give wider context at cost of more tokens."
				},
				"seed_strategy": {
					"type": "string",
					"enum": ["substring", "embedding", "hybrid"],
					"description": "How to match query → seed nodes. 'hybrid' (default, recommended): substring + embedding seeds with embedding-dominance threshold (≥3 embedding seeds drops substring entirely). 'embedding': Voyage-embed only (requires VOYAGE_API_KEY + index with embeddings populated). 'substring': tokens word-boundary-match Name/QualifiedName. NOTE: when 'substring' is requested AND the project has embeddings, the call is auto-routed to 'hybrid' — substring on its own surfaces PageRank-propagation noise (generic types / error wrappers accumulating rank from seeded callers) that hybrid eliminates. Bare substring runs only when no embeddings exist."
				}
			},
			"required": ["query"]
		}`),
	}, s.handleRankByQuery)
}

// registerCodeLocalizeTool adds code_localize — the LocAgent-style
// graph-guided code localization primitive. Given a natural-language
// issue or question, returns the top-K code entities (Functions,
// Methods, Classes) most relevant to investigate, computed via
// bidirectional BFS from query-matched seeds over CALLS / DEFINES /
// IMPORTS / CONTAINS / MEMBER_OF / IMPLEMENTS edges. Reference:
// LocAgent, ACL 2025, arXiv 2503.09089 — published 92.7% file-level
// localization accuracy on Loc-Bench with the LLM-in-the-loop variant;
// our primitives-only variant trades some accuracy for determinism.
func (s *Server) registerCodeLocalizeTool() {
	s.addTool(&mcp.Tool{
		Name: "code_localize",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Graph-guided code localization (primitives-only LocAgent variant). Best with focused queries that name specific symbols or short error messages. WORKS POORLY on verbose multi-paragraph issue descriptions — tokens after stopword filter become noise words that substring-match thousands of unrelated symbols, and BFS amplifies the noise. For verbose Loc-Bench-style issues, use code_localize_agent — the LLM-driven variant bridges the 'issue talks about A, fix happens in B' gap that pure retrieval misses. Algorithm: match seeds via seed_strategy, BFS-expand up to `depth` hops over CALLS/DEFINES/IMPORTS/CONTAINS/MEMBER_OF/IMPLEMENTS/OVERRIDE/USES_TYPE bidirectionally, score each visited node by seed-score / 2^distance, return top-K with file:line. Reference: LocAgent (ACL 2025, arXiv 2503.09089) — published 92.7% file-level accuracy uses the LLM-in-the-loop variant; this primitives-only path trades accuracy for determinism and zero LLM cost.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"issue_description": {
					"type": "string",
					"description": "The issue/question/symbol-list to localize. Tokenized (drops 1-char tokens + English stopwords); at least one token must match a node Name or QualifiedName substring."
				},
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"depth": {
					"type": "integer",
					"description": "BFS expansion radius from each seed (0-5, default 3). Higher reaches more code but adds noise."
				},
				"top_k": {
					"type": "integer",
					"description": "Maximum number of localized entities to return (1-50, default 10)."
				},
				"seed_strategy": {
					"type": "string",
					"enum": ["substring", "embedding", "hybrid"],
					"description": "How to match issue → seed nodes. 'substring' (legacy): tokens substring-match Name/QualifiedName. 'embedding': Voyage-embed the issue, cosine-search node embeddings (requires VOYAGE_API_KEY + index with embeddings populated). 'hybrid' (default): both, deduplicated; falls back to substring if embeddings unavailable."
				}
			},
			"required": ["issue_description"]
		}`),
	}, s.handleCodeLocalize)
}

// registerCodeLocalizeAgentTool adds code_localize_agent — the LLM-driven
// LocAgent variant. Wraps our graph primitives in an iterative agent loop
// (rank_by_query → code_localize → narrow → finalize). Slower and more
// expensive per query than code_localize, but designed to match
// LocAgent's published 92.7% file-level localization on Loc-Bench by
// adding the intelligent narrowing layer that primitives-only cannot do.
//
// Requires ANTHROPIC_API_KEY. Falls back to errResult if missing.
func (s *Server) registerCodeLocalizeAgentTool() {
	s.addTool(&mcp.Tool{
		Name: "code_localize_agent",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  false, // LLM is non-deterministic
			OpenWorldHint:   boolPtr(true),
			DestructiveHint: boolPtr(false),
		},
		Description: "LLM-driven code localization (LocAgent ACL 2025 architecture). Use this for VERBOSE natural-language issue descriptions — Loc-Bench security writeups, multi-paragraph bug reports, anything where the issue text talks about A but the fix is in B. The LLM iteratively calls rank_by_query / code_localize / finalize, reasons about call paths and entry points, and returns a ranked list of entities to investigate. Demonstrably bridges the gap that primitives miss (n=1: pip Loc-Bench instance pypa__pip-13085 — primitives missed top-20, agent landed ground truth at #3). Cost: ~30-60s wall, ~$0.04-0.05 per query at Haiku 4.5 (~50K input tokens, 6 turns typical). Requires ANTHROPIC_API_KEY. For specific symbol queries (function names, exact identifiers), use code_localize instead — primitives are ~1000x faster with zero LLM cost.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"issue_description": {
					"type": "string",
					"description": "Natural-language issue/question to localize."
				},
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"top_k": {
					"type": "integer",
					"description": "Maximum number of entities to return (1-50, default 10)."
				},
				"include_transcript": {
					"type": "boolean",
					"description": "If true, include the agent's per-turn transcript (tool calls + summaries) for debugging. Default false."
				}
			},
			"required": ["issue_description"]
		}`),
	}, s.handleCodeLocalizeAgent)
}

// registerGenerateReportTool adds the generate_report MCP tool — writes
// ARCHITECTURE_REPORT.md to the repo root for always-on orientation via
// the PreToolUse hook installed by `code-graph install`.
func (s *Server) registerGenerateReportTool() {
	s.addTool(&mcp.Tool{
		Name: "generate_report",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false, // writes a file to the repo root
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Write ARCHITECTURE_REPORT.md to the repo root — a one-page orientation doc (god nodes, communities + cohesion, cross-package boundaries, 5 suggested questions) derived from the indexed graph. Auto-runs at the end of index_repository; call manually to regenerate without reindexing. Intended to be read by coding assistants before Glob/Grep on an unfamiliar codebase.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				}
			}
		}`),
	}, s.handleGenerateReport)
}
