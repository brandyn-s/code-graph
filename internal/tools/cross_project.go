package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/localize"
	"github.com/DeusData/codebase-memory-mcp/internal/ranking"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxCrossProjectIndexes = 25

type crossProjectLocalizedEntity struct {
	Project     string `json:"project"`
	ProjectRank int    `json:"project_rank"`
	GlobalRank  int    `json:"global_rank"`
	localize.LocalizedEntity
}

type crossProjectLocalizationRequest struct {
	query          string
	requested      []string
	strategy       ranking.SeedStrategy
	depth          int
	perProjectTopK int
	topK           int
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

func boundedIntArg(args map[string]any, key string, fallback, minimum, maximum int) int {
	value := getIntArg(args, key, fallback)
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func parseCrossProjectLocalizationRequest(req *mcp.CallToolRequest) (crossProjectLocalizationRequest, error) {
	args, err := parseArgs(req)
	if err != nil {
		return crossProjectLocalizationRequest{}, err
	}
	query := strings.TrimSpace(getStringArg(args, "query"))
	if query == "" {
		return crossProjectLocalizationRequest{}, fmt.Errorf("query is required")
	}
	requested, err := stringSliceArg(args, "projects")
	if err != nil {
		return crossProjectLocalizationRequest{}, err
	}
	strategyName := getStringArg(args, "seed_strategy")
	if strategyName == "" {
		strategyName = string(ranking.SeedStrategyHybrid)
	}
	strategy := ranking.SeedStrategy(strategyName)
	switch strategy {
	case ranking.SeedStrategySubstring, ranking.SeedStrategyEmbedding, ranking.SeedStrategyHybrid:
	default:
		return crossProjectLocalizationRequest{}, fmt.Errorf("seed_strategy must be 'substring', 'embedding', or 'hybrid'")
	}
	return crossProjectLocalizationRequest{
		query: query, requested: requested, strategy: strategy,
		depth:          boundedIntArg(args, "depth", 2, 0, 5),
		perProjectTopK: boundedIntArg(args, "per_project_top_k", 5, 1, 20),
		topK:           boundedIntArg(args, "top_k", 25, 1, 100),
	}, nil
}

func selectedCrossProjectInfos(infos []*store.ProjectInfo, requested []string) ([]*store.ProjectInfo, bool, error) {
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	available := make(map[string]bool, len(infos))
	for _, info := range infos {
		available[info.Name] = true
	}
	for _, project := range requested {
		if !available[project] {
			return nil, false, fmt.Errorf("project %q not found; use list_projects for canonical names", project)
		}
	}
	requestedSet := make(map[string]bool, len(requested))
	for _, project := range requested {
		requestedSet[project] = true
	}
	selected := make([]*store.ProjectInfo, 0, min(len(infos), maxCrossProjectIndexes))
	truncated := false
	for _, info := range infos {
		if strings.HasPrefix(info.Name, "_") || (len(requestedSet) > 0 && !requestedSet[info.Name]) {
			continue
		}
		if len(selected) == maxCrossProjectIndexes {
			truncated = true
			break
		}
		selected = append(selected, info)
	}
	return selected, truncated, nil
}

func (s *Server) localizeSelectedProjects(
	ctx context.Context,
	request *crossProjectLocalizationRequest,
	infos []*store.ProjectInfo,
) (
	perProject map[string][]localize.LocalizedEntity,
	projectContexts map[string]any,
	projectErrors map[string]string,
) {
	perProject = make(map[string][]localize.LocalizedEntity)
	projectContexts = make(map[string]any)
	projectErrors = make(map[string]string)
	for _, info := range infos {
		st, release, err := s.router.AcquireStore(info.Name)
		if err != nil {
			projectErrors[info.Name] = err.Error()
			continue
		}
		matches, localizeErr := localize.CodeLocalizeWithStrategy(
			ctx, st, info.Name, request.query, request.depth, request.perProjectTopK, request.strategy,
		)
		contextFields := map[string]any{"root_path": info.RootPath}
		s.addLiveIndexIdentity(contextFields, st, info.Name, info.RootPath)
		projectContexts[info.Name] = contextFields
		release()
		if localizeErr == nil {
			perProject[info.Name] = matches
			continue
		}
		if strings.Contains(localizeErr.Error(), "no nodes matched") {
			perProject[info.Name] = []localize.LocalizedEntity{}
			continue
		}
		projectErrors[info.Name] = localizeErr.Error()
	}
	return perProject, projectContexts, projectErrors
}

func balanceCrossProjectResults(perProject map[string][]localize.LocalizedEntity, topK int) []crossProjectLocalizedEntity {
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
	return results
}

func (s *Server) handleLocalizeAcrossProjects(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	request, err := parseCrossProjectLocalizationRequest(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	infos, err := s.router.ListProjects()
	if err != nil {
		return errResult(fmt.Sprintf("list projects: %v", err)), nil
	}
	selected, truncatedProjects, err := selectedCrossProjectInfos(infos, request.requested)
	if err != nil {
		return errResult(err.Error()), nil
	}
	perProject, projectContexts, projectErrors := s.localizeSelectedProjects(ctx, &request, selected)
	results := balanceCrossProjectResults(perProject, request.topK)
	projectsWithMatches := 0
	for _, matches := range perProject {
		if len(matches) > 0 {
			projectsWithMatches++
		}
	}

	return jsonResult(map[string]any{
		"query":                          request.query,
		"projects_attempted":             len(selected),
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
