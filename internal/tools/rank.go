package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/DeusData/codebase-memory-mcp/internal/ranking"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type rankByQueryResult struct {
	Query        string               `json:"query"`
	Project      string               `json:"project"`
	TopK         int                  `json:"top_k"`
	SeedStrategy string               `json:"seed_strategy"`
	Matches      []ranking.RankedNode `json:"matches"`
	Total        int                  `json:"total_returned"`
	Note         string               `json:"note,omitempty"`
	Metadata     map[string]any       `json:"_metadata,omitempty"`
}

// handleRankByQuery is the rank_by_query MCP tool. Wraps
// ranking.RankByQuery with project resolution and result formatting.
func (s *Server) handleRankByQuery(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	query := getStringArg(args, "query")
	if query == "" {
		return errResult("query is required (natural-language query or symbol list)"), nil
	}

	projectArg := getStringArg(args, "project")
	project := s.resolveProjectName(projectArg)
	if project == "" {
		return errResult("project is required (pass `project` explicitly or call from a session with a resolved project)"), nil
	}

	topK := getIntArg(args, "top_k", 20)
	strategyArg := getStringArg(args, "seed_strategy")
	if strategyArg == "" {
		strategyArg = string(ranking.SeedStrategyHybrid)
	}
	strategy := ranking.SeedStrategy(strategyArg)

	st, err := s.router.ForProject(project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	matches, err := ranking.RankByQueryWithStrategy(ctx, st, project, query, topK, strategy)
	if err != nil {
		return errResult(fmt.Sprintf("rank_by_query: %v", err)), nil
	}

	out := rankByQueryResult{
		Query:        query,
		Project:      project,
		TopK:         topK,
		SeedStrategy: string(strategy),
		Matches:      matches,
		Total:        len(matches),
	}
	if len(matches) == 0 {
		out.Note = "No nodes matched the query. Try more specific tokens (function/class names) or expand `top_k`."
	}
	out.Metadata = s.stdReadGraphMetadata(project)

	body, _ := json.MarshalIndent(out, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}
