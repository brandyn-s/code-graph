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
		Description: "Analyze which files always change together based on git history (FILE_CHANGES_WITH edges). Groups couplings as 'logical' (same package — expected) or 'accidental' (cross-package — architectural smell). Accidental coupling indicates hidden dependencies the architecture doesn't express. Use for architecture reviews, refactoring planning, and detecting architectural drift.",
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

	// Get all FILE_CHANGES_WITH edges
	edges, err := st.FindEdgesByType(projName, "FILE_CHANGES_WITH")
	if err != nil {
		return errResult(fmt.Sprintf("query edges: %v", err)), nil
	}

	type coupling struct {
		File1      string  `json:"file1"`
		File2      string  `json:"file2"`
		Score      float64 `json:"coupling_score"`
		Package1   string  `json:"package1"`
		Package2   string  `json:"package2"`
		SamePackage bool   `json:"same_package"`
		Category   string  `json:"category"` // "logical" or "accidental"
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

		pkg1 := extractFileDir(src.FilePath)
		pkg2 := extractFileDir(tgt.FilePath)
		samePackage := pkg1 == pkg2

		category := "logical"
		if !samePackage {
			category = "accidental"
		}

		couplings = append(couplings, coupling{
			File1:       src.FilePath,
			File2:       tgt.FilePath,
			Score:       score,
			Package1:    pkg1,
			Package2:    pkg2,
			SamePackage: samePackage,
			Category:    category,
		})
	}

	// Sort: accidental first (higher priority), then by score descending
	sort.Slice(couplings, func(i, j int) bool {
		if couplings[i].Category != couplings[j].Category {
			return couplings[i].Category == "accidental" // accidental first
		}
		return couplings[i].Score > couplings[j].Score
	})

	if len(couplings) > limit {
		couplings = couplings[:limit]
	}

	// Count by category
	accidental, logical := 0, 0
	for _, c := range couplings {
		if c.Category == "accidental" {
			accidental++
		} else {
			logical++
		}
	}

	responseData := map[string]any{
		"couplings":          couplings,
		"total":              len(couplings),
		"accidental_count":   accidental,
		"logical_count":      logical,
		"min_score":          minScore,
	}

	if accidental > 0 {
		responseData["hint"] = fmt.Sprintf("%d accidental coupling(s) found — files in different packages that frequently change together. These indicate hidden dependencies the architecture doesn't express. Consider extracting shared logic into a common package.", accidental)
	}

	return jsonResult(responseData), nil
}

// extractFileDir extracts the directory component from a file path.
func extractFileDir(path string) string {
	// Normalize separators
	path = strings.ReplaceAll(path, "\\", "/")
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return ""
	}
	return path[:idx]
}
