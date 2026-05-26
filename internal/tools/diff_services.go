package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/safegit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerDiffServicesTool() {
	s.addTool(&mcp.Tool{
		Name: "diff_services",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Compare two git refs and report which services changed, how many files per service, and which security-tagged files were modified. Designed for STIG reassessment: 'what changed since the last version we assessed?' Returns services grouped by change magnitude, plus a list of security-relevant changes.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"from_ref": {
					"type": "string",
					"description": "Starting git ref (tag, branch, or commit SHA). Example: 'v1.0.0', 'main~50', 'abc1234'"
				},
				"to_ref": {
					"type": "string",
					"description": "Ending git ref (default: 'HEAD')"
				},
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				}
			},
			"required": ["from_ref"]
		}`),
	}, s.handleDiffServices)
}

func (s *Server) handleDiffServices(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	fromRef := getStringArg(args, "from_ref")
	if fromRef == "" {
		return errResult("missing required 'from_ref' parameter"), nil
	}
	toRef := getStringArg(args, "to_ref")
	if toRef == "" {
		toRef = "HEAD"
	}

	for _, ref := range []string{fromRef, toRef} {
		if strings.ContainsAny(ref, ";|&$`\\\"'<>(){}") {
			return errResult(fmt.Sprintf("invalid ref: %q", ref)), nil
		}
	}

	project := getStringArg(args, "project")
	effectiveProject := s.resolveProjectName(project)

	_, repoPath, _, resolveErr := s.resolveDetectRepo(effectiveProject)
	if resolveErr != nil {
		return resolveErr, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := safegit.Command(ctx, "diff", "--name-status", fromRef+".."+toRef) //nolint:gosec // refs are from trusted MCP tool input, not user-facing web input
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return errResult(fmt.Sprintf("git diff failed: %v (check that %q is a valid ref)", err, fromRef)), nil
	}

	type fileChange struct {
		Status string
		Path   string
	}

	var changes []fileChange
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			changes = append(changes, fileChange{Status: parts[0], Path: parts[1]})
		}
	}

	type serviceDiff struct {
		Name          string   `json:"name"`
		Domain        string   `json:"domain"`
		FilesChanged  int      `json:"files_changed"`
		FilesAdded    int      `json:"files_added"`
		FilesDeleted  int      `json:"files_deleted"`
		IsNew         bool     `json:"is_new,omitempty"`
		SecurityFiles []string `json:"security_files,omitempty"`
	}

	serviceChanges := make(map[string]*serviceDiff)
	securityPatterns := []string{"auth", "crypto", "tls", "cert", "security", "password", "token", "session", "permission", "opa", "policy"}

	for _, ch := range changes {
		crate := extractTopLevelCrate(ch.Path)
		if crate == "" {
			continue
		}
		if serviceChanges[crate] == nil {
			serviceChanges[crate] = &serviceDiff{
				Name:   crate,
				Domain: classifyDomain(crate),
			}
		}
		sd := serviceChanges[crate]
		sd.FilesChanged++
		switch ch.Status {
		case "A":
			sd.FilesAdded++
		case "D":
			sd.FilesDeleted++
		}

		lower := strings.ToLower(ch.Path)
		for _, pat := range securityPatterns {
			if strings.Contains(lower, pat) {
				sd.SecurityFiles = append(sd.SecurityFiles, ch.Path)
				break
			}
		}
	}

	var sorted []serviceDiff
	for _, sd := range serviceChanges {
		sorted = append(sorted, *sd)
	}
	// FilesChanged desc, then Name asc. Service Name is the upstream
	// `serviceChanges map[string]*serviceDiff` key, so it's a strict total
	// order. Without the tiebreaker, services with equal change counts
	// (very common) land in random order across runs.
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].FilesChanged != sorted[j].FilesChanged {
			return sorted[i].FilesChanged > sorted[j].FilesChanged
		}
		return sorted[i].Name < sorted[j].Name
	})

	for i := range sorted {
		if sorted[i].FilesAdded == sorted[i].FilesChanged && sorted[i].FilesChanged > 0 {
			sorted[i].IsNew = true
		}
	}

	securityImpacted := 0
	var allSecurityFiles []string
	for _, sd := range sorted {
		if len(sd.SecurityFiles) > 0 {
			securityImpacted++
			allSecurityFiles = append(allSecurityFiles, sd.SecurityFiles...)
		}
	}

	domainSummary := make(map[string]int)
	for _, sd := range sorted {
		domainSummary[sd.Domain] += sd.FilesChanged
	}

	responseData := map[string]any{
		"from_ref":            fromRef,
		"to_ref":              toRef,
		"services_changed":    sorted,
		"total_services":      len(sorted),
		"total_files_changed": len(changes),
		"security_impacted":   securityImpacted,
		"domain_summary":      domainSummary,
	}

	if len(allSecurityFiles) > 0 {
		responseData["security_files"] = allSecurityFiles
		responseData["stig_hint"] = fmt.Sprintf("%d service(s) with security-relevant file changes. Review these for STIG reassessment impact.", securityImpacted)
	}

	return jsonResult(responseData), nil
}
