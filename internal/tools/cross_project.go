package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/localize"
	"github.com/DeusData/codebase-memory-mcp/internal/ranking"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxCrossProjectIndexes = 25

type crossProjectLocalizedEntity struct {
	Project     string `json:"project"`
	ProjectRank int    `json:"project_rank"`
	GlobalRank  int    `json:"global_rank"`
	localize.LocalizedEntity
}

func stringSliceArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		text, ok := value.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" {
			return nil, fmt.Errorf("%s must contain only non-empty strings", key)
		}
		if !seen[text] {
			seen[text] = true
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Server) handleLocalizeAcrossProjects(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}
	query := strings.TrimSpace(getStringArg(args, "query"))
	if query == "" {
		return errResult("query is required"), nil
	}
	requested, err := stringSliceArg(args, "projects")
	if err != nil {
		return errResult(err.Error()), nil
	}
	requestedSet := make(map[string]bool, len(requested))
	for _, project := range requested {
		requestedSet[project] = true
	}

	strategyName := getStringArg(args, "seed_strategy")
	if strategyName == "" {
		strategyName = string(ranking.SeedStrategyHybrid)
	}
	strategy := ranking.SeedStrategy(strategyName)
	if strategy != ranking.SeedStrategySubstring && strategy != ranking.SeedStrategyEmbedding && strategy != ranking.SeedStrategyHybrid {
		return errResult("seed_strategy must be 'substring', 'embedding', or 'hybrid'"), nil
	}
	depth := getIntArg(args, "depth", 2)
	if depth < 0 {
		depth = 0
	}
	if depth > 5 {
		depth = 5
	}
	perProjectTopK := getIntArg(args, "per_project_top_k", 5)
	if perProjectTopK < 1 {
		perProjectTopK = 1
	}
	if perProjectTopK > 20 {
		perProjectTopK = 20
	}
	topK := getIntArg(args, "top_k", 25)
	if topK < 1 {
		topK = 1
	}
	if topK > 100 {
		topK = 100
	}

	infos, err := s.router.ListProjects()
	if err != nil {
		return errResult(fmt.Sprintf("list projects: %v", err)), nil
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	available := make(map[string]bool, len(infos))
	for _, info := range infos {
		available[info.Name] = true
	}
	for _, project := range requested {
		if !available[project] {
			return errResult(fmt.Sprintf("project %q not found; use list_projects for canonical names", project)), nil
		}
	}

	perProject := make(map[string][]localize.LocalizedEntity)
	projectContexts := make(map[string]any)
	projectErrors := make(map[string]string)
	attempted := 0
	truncatedProjects := false
	for _, info := range infos {
		if strings.HasPrefix(info.Name, "_") || (len(requestedSet) > 0 && !requestedSet[info.Name]) {
			continue
		}
		if attempted == maxCrossProjectIndexes {
			truncatedProjects = true
			break
		}
		attempted++
		st, release, openErr := s.router.AcquireStore(info.Name)
		if openErr != nil {
			projectErrors[info.Name] = openErr.Error()
			continue
		}
		matches, localizeErr := localize.CodeLocalizeWithStrategy(ctx, st, info.Name, query, depth, perProjectTopK, strategy)
		contextFields := map[string]any{"root_path": info.RootPath}
		s.addLiveIndexIdentity(contextFields, st, info.Name, info.RootPath)
		projectContexts[info.Name] = contextFields
		release()
		if localizeErr != nil {
			if strings.Contains(localizeErr.Error(), "no nodes matched") {
				perProject[info.Name] = []localize.LocalizedEntity{}
				continue
			}
			projectErrors[info.Name] = localizeErr.Error()
			continue
		}
		perProject[info.Name] = matches
	}

	projectNames := make([]string, 0, len(perProject))
	for project := range perProject {
		projectNames = append(projectNames, project)
	}
	sort.Strings(projectNames)
	results := make([]crossProjectLocalizedEntity, 0, topK)
	for rank := 0; len(results) < topK; rank++ {
		added := false
		for _, project := range projectNames {
			matches := perProject[project]
			if rank >= len(matches) {
				continue
			}
			results = append(results, crossProjectLocalizedEntity{
				Project: project, ProjectRank: rank + 1,
				GlobalRank: len(results) + 1, LocalizedEntity: matches[rank],
			})
			added = true
			if len(results) == topK {
				break
			}
		}
		if !added {
			break
		}
	}
	projectsWithMatches := 0
	for _, matches := range perProject {
		if len(matches) > 0 {
			projectsWithMatches++
		}
	}

	return jsonResult(map[string]any{
		"query":                          query,
		"projects_attempted":             attempted,
		"projects_with_matches":          projectsWithMatches,
		"projects_truncated":             truncatedProjects,
		"ranking_policy":                 "project_balanced_round_robin",
		"cross_project_score_comparable": false,
		"result_scope":                   "discovery_only; verify claims with project-bound source or relationship evidence",
		"results":                        results,
		"project_contexts":               projectContexts,
		"project_errors":                 projectErrors,
		"_metadata":                      s.stdStatusToolMetadata(),
	}), nil
}
