package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type similarFunctionMatch struct {
	Name            string  `json:"name"`
	QualifiedName   string  `json:"qualified_name"`
	Label           string  `json:"label"`
	FilePath        string  `json:"file_path"`
	SimilarityScore float64 `json:"similarity_score"`
}

type similarFunctionsResult struct {
	Query      string                 `json:"query"`
	QueryNode  map[string]any         `json:"query_node,omitempty"`
	Limit      int                    `json:"limit"`
	Threshold  float64                `json:"threshold"`
	Matches    []similarFunctionMatch `json:"matches"`
	TotalFound int                    `json:"total_found"`
	Note       string                 `json:"note,omitempty"`
	Metadata   map[string]any         `json:"_metadata,omitempty"`
}

// handleFindSimilarFunctions is the find_similar_functions MCP tool.
// Given a function name (exact or substring via name_pattern resolution),
// returns the top-K functions with the highest cosine similarity on their
// Voyage embeddings. Useful for "is this function's logic duplicated
// elsewhere?" — the core refactor-candidate discovery workflow.
//
// Relies on the embedding cache populated by the embeddings pass, so it
// requires a prior `index_repository` run with VOYAGE_API_KEY set. When
// embeddings are missing for the project, returns a clear error pointing
// the caller at the reindex step.
func (s *Server) handleFindSimilarFunctions(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	name := getStringArg(args, "name")
	if name == "" {
		return errResult("name is required (function name to find similar functions to)"), nil
	}

	projectArg := getStringArg(args, "project")
	project := s.resolveProjectName(projectArg)
	if project == "" {
		return errResult("project is required (pass `project` explicitly or call from a session with a resolved project)"), nil
	}

	limit := getIntArg(args, "limit", 10)
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}

	threshold := 0.0
	if raw, ok := args["threshold"]; ok {
		if f, ok := raw.(float64); ok && f >= 0 && f <= 1 {
			threshold = f
		}
	}

	st, err := s.router.ForProject(project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	count, err := st.EmbeddingCount(project)
	if err != nil {
		return errResult(fmt.Sprintf("embedding count: %v", err)), nil
	}
	if count == 0 {
		return errResult(fmt.Sprintf(
			"No embeddings available for project %q. Reindex with VOYAGE_API_KEY set and CODE_GRAPH_SKIP_EMBEDDINGS unset: index_repository(repo_path=..., force=true).",
			project)), nil
	}

	// Resolve the function name to exactly one node. Try exact match on
	// name first (most specific); fall back to qualified-name substring.
	nodes, err := resolveFunctionByName(st, project, name)
	if err != nil {
		return errResult(err.Error()), nil
	}
	if len(nodes) == 0 {
		return errResult(fmt.Sprintf("no function named %q found in project %q — try search_graph(label=\"Function\", name_pattern=%q) to see candidates",
			name, project, name)), nil
	}
	if len(nodes) > 1 {
		hints := make([]string, 0, len(nodes))
		for i, n := range nodes {
			if i >= 5 {
				break
			}
			hints = append(hints, n.QualifiedName)
		}
		return errResult(fmt.Sprintf(
			"name %q is ambiguous (%d matches); pass the full qualified_name via the `name` arg. Candidates: %v",
			name, len(nodes), hints)), nil
	}
	query := nodes[0]

	results, err := st.FindSimilarNodes(project, query.ID, limit*2) // over-fetch to allow threshold pruning
	if err != nil {
		return errResult(fmt.Sprintf("cosine search: %v", err)), nil
	}

	matches := make([]similarFunctionMatch, 0, limit)
	for _, r := range results {
		if r.Score < threshold {
			// results are sorted desc — safe to stop
			break
		}
		matches = append(matches, similarFunctionMatch{
			Name:            r.Name,
			QualifiedName:   r.QName,
			Label:           r.Label,
			FilePath:        r.FilePath,
			SimilarityScore: r.Score,
		})
		if len(matches) >= limit {
			break
		}
	}

	out := similarFunctionsResult{
		Query:     name,
		Limit:     limit,
		Threshold: threshold,
		Matches:   matches,
		QueryNode: map[string]any{
			"name":           query.Name,
			"qualified_name": query.QualifiedName,
			"label":          query.Label,
			"file_path":      query.FilePath,
		},
		TotalFound: len(matches),
	}
	if len(matches) == 0 {
		out.Note = fmt.Sprintf(
			"No matches above threshold=%.2f. Either %q is unique in this codebase, or its embedding is in a sparse region of vector space. Try a lower threshold or limit=50 to see the nearest regardless of score.",
			threshold, name)
	}
	out.Metadata = s.stdReadGraphMetadata(project)

	body, _ := json.MarshalIndent(out, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}

// resolveFunctionByName returns the Function/Method node(s) matching
// `name` in the given project. Exact match on the Name column is tried
// first; on miss, attempts a qualified_name substring lookup so callers
// can pass either a bare name ("handle_login") or a full qualified name
// ("pkg.auth.handle_login"). Returns more than one match when the name
// is ambiguous so the caller can pick the right one.
func resolveFunctionByName(st *store.Store, project, name string) ([]*store.Node, error) {
	// Exact name match, filtered to Function/Method labels.
	all, err := st.FindNodesByName(project, name)
	if err != nil {
		return nil, fmt.Errorf("lookup by name: %w", err)
	}
	var exact []*store.Node
	for _, n := range all {
		if n.Label == "Function" || n.Label == "Method" {
			exact = append(exact, n)
		}
	}
	if len(exact) > 0 {
		return exact, nil
	}

	// Fallback: qualified_name exact match (handles fully-qualified input).
	if n, err := st.FindNodeByQN(project, name); err == nil && n != nil {
		return []*store.Node{n}, nil
	}

	return nil, nil
}
