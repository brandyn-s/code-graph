package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stigToRoles maps NIST/STIG control IDs to the security roles that provide evidence.
var stigToRoles = map[string][]string{
	"AC-3":  {"auth_boundary"},
	"AC-6":  {"auth_boundary", "privilege_escalation"},
	"AU-2":  {"audit_logging"},
	"AU-3":  {"audit_logging"},
	"IA-2":  {"auth_boundary", "privilege_escalation"},
	"IA-5":  {"crypto_operation"},
	"SC-8":  {"crypto_operation"},
	"SC-13": {"crypto_operation"},
	"SC-23": {"session_management"},
	"SI-10": {"input_entry_point", "sensitive_sink"},
	"SI-11": {"sensitive_sink"},
}

func (s *Server) registerSTIGEvidenceTool() {
	s.addTool(&mcp.Tool{
		Name: "query_stig_evidence",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Map a STIG or NIST control ID directly to code evidence from the graph. Accepts a control ID (e.g. 'AC-3', 'SI-10') and returns all graph nodes whose security_role matches that control's requirements. Supports prefix matching: 'AC-3' also matches 'AC-3(4)'. Eliminates the manual step of mapping controls to security roles before calling query_security_surfaces. If the control ID is not recognized, returns the list of supported controls.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"control_id": {
					"type": "string",
					"description": "STIG or NIST control ID (e.g. 'AC-3', 'SI-10', 'SC-13'). Case-insensitive. Supports prefix matching: 'AC-3' matches 'AC-3(4)'."
				},
				"project": {
					"type": "string",
					"description": "Project to query. Defaults to session project."
				},
				"limit": {
					"type": "integer",
					"description": "Max results per security role (default 20)"
				}
			},
			"required": ["control_id"]
		}`),
	}, s.handleSTIGEvidence)
}

func (s *Server) handleSTIGEvidence(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	controlID := strings.ToUpper(strings.TrimSpace(getStringArg(args, "control_id")))
	if controlID == "" {
		return errResult("control_id is required"), nil
	}

	// Look up roles: exact match first, then prefix match
	roles := stigToRoles[controlID]
	if roles == nil {
		// Try prefix matching: "AC-3" should match "AC-3(4)" entries
		for key, val := range stigToRoles {
			if strings.HasPrefix(key, controlID) || strings.HasPrefix(controlID, key) {
				roles = append(roles, val...)
			}
		}
	}

	// Deduplicate roles from prefix matching
	if len(roles) > 0 {
		seen := make(map[string]bool, len(roles))
		unique := make([]string, 0, len(roles))
		for _, r := range roles {
			if !seen[r] {
				seen[r] = true
				unique = append(unique, r)
			}
		}
		roles = unique
	}

	// If still no roles found, return the supported controls list
	if len(roles) == 0 {
		supported := make([]string, 0, len(stigToRoles))
		for k := range stigToRoles {
			supported = append(supported, k)
		}
		sort.Strings(supported)
		return jsonResult(map[string]any{
			"error":              fmt.Sprintf("control ID %q not recognized", controlID),
			"supported_controls": supported,
		}), nil
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

	limit := getIntArg(args, "limit", 20)

	type evidenceEntry struct {
		Name          string `json:"name"`
		QualifiedName string `json:"qualified_name"`
		Label         string `json:"label"`
		FilePath      string `json:"file_path"`
		SecurityRole  string `json:"security_role"`
		Callers       int    `json:"callers"`
		Callees       int    `json:"callees"`
	}

	var evidence []evidenceEntry
	rolesSearched := make([]string, 0, len(roles))

	for _, role := range roles {
		rolesSearched = append(rolesSearched, role)
		nodes, findErr := st.FindNodesByProperty(projName, "", "security_role", role)
		if findErr != nil {
			continue
		}
		for i, n := range nodes {
			if i >= limit {
				break
			}
			callers, callees := st.NodeDegree(n.ID)
			evidence = append(evidence, evidenceEntry{
				Name:          n.Name,
				QualifiedName: n.QualifiedName,
				Label:         n.Label,
				FilePath:      n.FilePath,
				SecurityRole:  role,
				Callers:       callers,
				Callees:       callees,
			})
		}
	}

	return jsonResult(map[string]any{
		"control_id":     controlID,
		"roles_searched": rolesSearched,
		"evidence_count": len(evidence),
		"evidence":       evidence,
		"_metadata":      s.stdReadGraphMetadata(projName),
	}), nil
}
