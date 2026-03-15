package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerSecurityTools() {
	s.addTool(&mcp.Tool{
		Name:        "query_security_surfaces",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Query security-tagged code elements for compliance evidence. Returns functions classified as auth_boundary (authentication/authorization enforcement), input_entry_point (HTTP handlers, CLI entry points), sensitive_sink (database writes, file I/O, subprocess exec), or crypto_operation (encryption, hashing, signing). Use for STIG/compliance evidence: AC-3 -> auth_boundary, SI-10 -> input_entry_point + sensitive_sink, SC-13 -> crypto_operation. Pass role to filter, or omit for all roles.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"role": {
					"type": "string",
					"enum": ["auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation"],
					"description": "Filter by security role. Omit for all roles."
				},
				"project": {
					"type": "string",
					"description": "Project to query. Defaults to session project."
				},
				"limit": {
					"type": "integer",
					"description": "Max results per role (default 20)"
				}
			}
		}`),
	}, s.handleQuerySecuritySurfaces)
}

func (s *Server) handleQuerySecuritySurfaces(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	roleFilter := getStringArg(args, "role")
	limit := getIntArg(args, "limit", 20)

	roles := []string{"auth_boundary", "input_entry_point", "sensitive_sink", "crypto_operation"}
	if roleFilter != "" {
		roles = []string{roleFilter}
	}

	type surfaceEntry struct {
		Name          string `json:"name"`
		QualifiedName string `json:"qualified_name"`
		Label         string `json:"label"`
		FilePath      string `json:"file_path"`
		SecurityRole  string `json:"security_role"`
		Callers       int    `json:"callers"`
		Callees       int    `json:"callees"`
	}

	results := make(map[string][]surfaceEntry)
	totalCount := 0

	for _, role := range roles {
		nodes, findErr := st.FindNodesByProperty(projName, "", "security_role", role)
		if findErr != nil {
			continue
		}
		entries := make([]surfaceEntry, 0, len(nodes))
		for i, n := range nodes {
			if i >= limit {
				break
			}
			callers, callees := st.NodeDegree(n.ID)
			entries = append(entries, surfaceEntry{
				Name:          n.Name,
				QualifiedName: n.QualifiedName,
				Label:         n.Label,
				FilePath:      n.FilePath,
				SecurityRole:  role,
				Callers:       callers,
				Callees:       callees,
			})
		}
		if len(entries) > 0 {
			results[role] = entries
			totalCount += len(nodes)
		}
	}

	responseData := map[string]any{
		"surfaces":    results,
		"total_count": totalCount,
		"stig_hints": map[string]string{
			"AC-3":  "Check auth_boundary nodes enforce access control on all input_entry_point paths",
			"SI-10": "Verify input_entry_point nodes validate input before reaching sensitive_sink nodes",
			"SC-13": "Confirm crypto_operation nodes use FIPS-approved algorithms",
		},
	}

	return jsonResult(responseData), nil
}
