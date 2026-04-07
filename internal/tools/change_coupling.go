package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerChangeCouplingTool() {
	s.addTool(&mcp.Tool{
		Name: "get_change_coupling",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Analyze which files always change together based on git history (FILE_CHANGES_WITH edges). Classifies couplings using the top-level crate/package (first path segment): 'logical' if both files are in the same crate, 'accidental' if they're in different crates. Accidental cross-crate coupling indicates hidden dependencies. Use for architecture reviews, refactoring planning, and detecting architectural drift.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				},
				"min_score": {
					"type": "number",
					"description": "Minimum coupling score to include (0.0-1.0, default 0.3). Higher values show stronger coupling."
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of couplings to return (default 30)"
				},
				"cross_crate_only": {
					"type": "boolean",
					"description": "Only show cross-crate (accidental) couplings (default true). Set false to include same-crate couplings."
				}
			}
		}`),
	}, s.handleChangeCoupling)
}

func (s *Server) handleChangeCoupling(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	st, err := s.resolveStore(getStringArg(args, "project"))
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	projName := s.resolveProjectName(getStringArg(args, "project"))
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	minScore := getFloatArg(args, "min_score", 0.3)
	limit := getIntArg(args, "limit", 30)
	crossCrateOnly := true
	if v, ok := args["cross_crate_only"]; ok {
		if b, ok := v.(bool); ok {
			crossCrateOnly = b
		}
	}

	edges, err := st.FindEdgesByType(projName, "FILE_CHANGES_WITH")
	if err != nil {
		return errResult(fmt.Sprintf("query edges: %v", err)), nil
	}

	type coupling struct {
		File1     string  `json:"file1"`
		File2     string  `json:"file2"`
		Score     float64 `json:"coupling_score"`
		Crate1    string  `json:"crate1"`
		Crate2    string  `json:"crate2"`
		SameCrate bool    `json:"same_crate"`
		Category  string  `json:"category"`
	}

	var couplings []coupling

	for _, e := range edges {
		score := 0.0
		if e.Properties != nil {
			if s, ok := e.Properties["coupling_score"].(float64); ok {
				score = s
			}
		}
		if score < minScore {
			continue
		}

		src, _ := st.FindNodeByID(e.SourceID)
		tgt, _ := st.FindNodeByID(e.TargetID)
		if src == nil || tgt == nil {
			continue
		}

		crate1 := extractTopLevelCrate(src.FilePath)
		crate2 := extractTopLevelCrate(tgt.FilePath)
		sameCrate := crate1 == crate2

		if crossCrateOnly && sameCrate {
			continue
		}

		category := "logical"
		if !sameCrate {
			category = "accidental"
		}

		couplings = append(couplings, coupling{
			File1:     src.FilePath,
			File2:     tgt.FilePath,
			Score:     score,
			Crate1:    crate1,
			Crate2:    crate2,
			SameCrate: sameCrate,
			Category:  category,
		})
	}

	// Sort: accidental first, then by score descending
	sort.Slice(couplings, func(i, j int) bool {
		if couplings[i].Category != couplings[j].Category {
			return couplings[i].Category == "accidental"
		}
		return couplings[i].Score > couplings[j].Score
	})

	if len(couplings) > limit {
		couplings = couplings[:limit]
	}

	accidental, logical := 0, 0
	for _, c := range couplings {
		if c.Category == "accidental" {
			accidental++
		} else {
			logical++
		}
	}

	// Summarize cross-crate pairs
	cratePairs := make(map[string]int)
	for _, c := range couplings {
		if c.Category == "accidental" {
			pair := c.Crate1 + " <-> " + c.Crate2
			if c.Crate1 > c.Crate2 {
				pair = c.Crate2 + " <-> " + c.Crate1
			}
			cratePairs[pair]++
		}
	}
	type cratePairSummary struct {
		Pair  string `json:"pair"`
		Count int    `json:"file_pairs"`
	}
	var pairSummary []cratePairSummary
	for pair, count := range cratePairs {
		pairSummary = append(pairSummary, cratePairSummary{Pair: pair, Count: count})
	}
	sort.Slice(pairSummary, func(i, j int) bool {
		return pairSummary[i].Count > pairSummary[j].Count
	})

	responseData := map[string]any{
		"couplings":        couplings,
		"total":            len(couplings),
		"accidental_count": accidental,
		"logical_count":    logical,
		"cross_crate_only": crossCrateOnly,
		"min_score":        minScore,
	}

	if len(pairSummary) > 0 {
		responseData["crate_pairs"] = pairSummary
		responseData["hint"] = fmt.Sprintf("%d cross-crate coupling(s) across %d crate pairs. These are files in different top-level packages that frequently change together.", accidental, len(pairSummary))
	}

	return jsonResult(responseData), nil
}

// extractTopLevelCrate extracts the first path segment (top-level crate/package name).
// E.g., "doomper/src/recorder.rs" -> "doomper"
//       "ship-os/src/components/layout/Navigation.tsx" -> "ship-os"
//       "redacted-platform-terraform/core/modules/environment/main.tf" -> "redacted-platform-terraform"
func extractTopLevelCrate(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	idx := strings.Index(path, "/")
	if idx < 0 {
		return path
	}
	return path[:idx]
}
