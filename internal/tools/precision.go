package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

const (
	graphPrecisionHeuristic = "heuristic"
	graphPrecisionSCIP      = "scip"
)

type graphPrecisionSelection struct {
	Tier   string
	Path   string
	Source string
}

func (s *Server) resolveGraphPrecision(args map[string]any, project, repoRoot string) (graphPrecisionSelection, error) {
	_, tierProvided := args["precision_tier"]
	_, pathProvided := args["scip_index_path"]
	if pathProvided && !tierProvided {
		return graphPrecisionSelection{}, fmt.Errorf("scip_index_path requires precision_tier='scip'")
	}

	tier := graphPrecisionHeuristic
	path := ""
	if tierProvided {
		tier = getStringArg(args, "precision_tier")
		if tier != graphPrecisionHeuristic && tier != graphPrecisionSCIP {
			return graphPrecisionSelection{}, fmt.Errorf("precision_tier must be 'heuristic' or 'scip'")
		}
		if tier == graphPrecisionSCIP {
			path = getStringArg(args, "scip_index_path")
			if path == "" {
				path = filepath.Join(repoRoot, "index.scip")
			} else if !filepath.IsAbs(path) {
				path = filepath.Join(repoRoot, path)
			}
			path = filepath.Clean(path)
		}
		if s.config != nil {
			if err := s.config.Set(store.ConfigGraphPrecisionTierPrefix+project, tier); err != nil {
				return graphPrecisionSelection{}, fmt.Errorf("persist graph precision tier: %w", err)
			}
			if tier == graphPrecisionSCIP {
				if err := s.config.Set(store.ConfigGraphSCIPPathPrefix+project, path); err != nil {
					return graphPrecisionSelection{}, fmt.Errorf("persist SCIP index path: %w", err)
				}
			} else if err := s.config.Delete(store.ConfigGraphSCIPPathPrefix + project); err != nil {
				return graphPrecisionSelection{}, fmt.Errorf("clear SCIP index path: %w", err)
			}
		}
	} else if s.config != nil {
		tier = s.config.Get(store.ConfigGraphPrecisionTierPrefix+project, graphPrecisionHeuristic)
		if tier == graphPrecisionSCIP {
			path = s.config.Get(store.ConfigGraphSCIPPathPrefix+project, filepath.Join(repoRoot, "index.scip"))
		}
	}

	source := "project-preference:heuristic"
	if tier == graphPrecisionSCIP {
		source = "project-preference:index.scip"
		if filepath.Base(path) != "index.scip" {
			source = "project-preference:custom"
		}
	}
	return graphPrecisionSelection{Tier: tier, Path: path, Source: source}, nil
}

func configurePipelinePrecision(p *pipeline.Pipeline, selection graphPrecisionSelection) {
	if selection.Tier == graphPrecisionSCIP {
		p.ConfigureSCIP(selection.Path, selection.Source)
		return
	}
	p.ConfigureSCIP("", selection.Source)
}

func (s *Server) configureStoredGraphPrecision(p *pipeline.Pipeline, project, repoRoot string) (graphPrecisionSelection, error) {
	selection, err := s.resolveGraphPrecision(map[string]any{}, project, repoRoot)
	if err != nil {
		return graphPrecisionSelection{}, err
	}
	configurePipelinePrecision(p, selection)
	return selection, nil
}

func graphPrecisionResult(selection graphPrecisionSelection, status pipeline.SCIPIngestStatus) map[string]any {
	effective := graphPrecisionHeuristic
	if selection.Tier == graphPrecisionSCIP && status.State == "applied" {
		effective = graphPrecisionSCIP
	}
	return map[string]any{
		"requested_tier": selection.Tier,
		"effective_tier": effective,
		"scip_status":    status,
		"analysis_scope": "CALLS edges for compiler-index-covered functions; other edges and uncovered files remain heuristic",
	}
}

func (s *Server) persistGraphPrecision(project string, selection graphPrecisionSelection, status pipeline.SCIPIngestStatus) {
	if s.config == nil {
		return
	}
	payload, err := json.Marshal(graphPrecisionResult(selection, status))
	if err != nil {
		return
	}
	_ = s.config.Set(store.ConfigGraphPrecisionStatusPrefix+project, string(payload))
}

func (s *Server) storedGraphPrecision(project string) map[string]any {
	if s.config == nil {
		return graphPrecisionResult(
			graphPrecisionSelection{Tier: graphPrecisionHeuristic},
			pipeline.SCIPIngestStatus{State: "unknown"},
		)
	}
	raw := s.config.Get(store.ConfigGraphPrecisionStatusPrefix+project, "")
	var result map[string]any
	if raw != "" && json.Unmarshal([]byte(raw), &result) == nil {
		return result
	}
	tier := s.config.Get(store.ConfigGraphPrecisionTierPrefix+project, graphPrecisionHeuristic)
	return graphPrecisionResult(
		graphPrecisionSelection{Tier: tier},
		pipeline.SCIPIngestStatus{State: "unknown"},
	)
}

func graphPrecisionIsDegraded(precision map[string]any) bool {
	return precision["requested_tier"] == graphPrecisionSCIP && precision["effective_tier"] != graphPrecisionSCIP
}
