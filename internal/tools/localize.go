package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/DeusData/codebase-memory-mcp/internal/localize"
	"github.com/DeusData/codebase-memory-mcp/internal/ranking"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type codeLocalizeResult struct {
	IssueDescription string                       `json:"issue_description"`
	Project          string                       `json:"project"`
	Depth            int                          `json:"depth"`
	TopK             int                          `json:"top_k"`
	SeedStrategy     string                       `json:"seed_strategy"`
	Matches          []localize.LocalizedEntity   `json:"matches"`
	Total            int                          `json:"total_returned"`
	Note             string                       `json:"note,omitempty"`
}

// handleCodeLocalize is the code_localize MCP tool handler. Wraps
// localize.CodeLocalize with project resolution and result formatting.
func (s *Server) handleCodeLocalize(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	issue := getStringArg(args, "issue_description")
	if issue == "" {
		return errResult("issue_description is required (the issue/question/symbol-list to localize)"), nil
	}

	projectArg := getStringArg(args, "project")
	project := s.resolveProjectName(projectArg)
	if project == "" {
		return errResult("project is required (pass `project` explicitly or call from a session with a resolved project)"), nil
	}

	depth := getIntArg(args, "depth", 3)
	topK := getIntArg(args, "top_k", 10)
	strategyArg := getStringArg(args, "seed_strategy")
	if strategyArg == "" {
		strategyArg = string(ranking.SeedStrategyHybrid)
	}
	strategy := ranking.SeedStrategy(strategyArg)

	st, err := s.router.ForProject(project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	matches, err := localize.CodeLocalizeWithStrategy(ctx, st, project, issue, depth, topK, strategy)
	if err != nil {
		return errResult(fmt.Sprintf("code_localize: %v", err)), nil
	}

	out := codeLocalizeResult{
		IssueDescription: issue,
		Project:          project,
		Depth:            depth,
		TopK:             topK,
		SeedStrategy:     string(strategy),
		Matches:          matches,
		Total:            len(matches),
	}
	if len(matches) == 0 {
		out.Note = "No nodes matched the issue. Try more specific tokens (function/class names) or expand `top_k`/`depth`."
	}

	body, _ := json.MarshalIndent(out, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}
