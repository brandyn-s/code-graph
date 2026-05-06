package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type rationaleEntry struct {
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	FilePath   string `json:"file_path"`
	Line       int    `json:"line"`
	SubjectQN  string `json:"subject_qualified_name,omitempty"`
	SubjectLbl string `json:"subject_label,omitempty"`
}

type findRationaleResult struct {
	Project     string           `json:"project"`
	KindFilter  string           `json:"kind_filter,omitempty"`
	TotalFound  int              `json:"total_found"`
	Entries     []rationaleEntry `json:"entries"`
	CountByKind map[string]int   `json:"count_by_kind,omitempty"`
	Note        string           `json:"note,omitempty"`
	Metadata    map[string]any   `json:"_metadata,omitempty"`
}

// handleFindRationale serves the find_rationale MCP tool. Queries
// Rationale nodes produced by passRationale, optionally filtered by
// kind (WHY/NOTE/HACK/SAFETY/TODO/FIXME/IMPORTANT/XXX). Returns each
// rationale with its text, location, and (if resolved) the enclosing
// Function/Method/Class subject.
//
// Primary use case: compliance / code-review audits where you want
// every `SAFETY:` justification across a Rust codebase, or every
// `HACK:` workaround in a Python service, without greping N repos.
func (s *Server) handleFindRationale(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	projectArg := getStringArg(args, "project")
	project := s.resolveProjectName(projectArg)
	if project == "" {
		return errResult("project is required (pass `project` explicitly or call from a session with a resolved project)"), nil
	}

	kind := strings.ToUpper(strings.TrimSpace(getStringArg(args, "kind")))
	limit := getIntArg(args, "limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}

	st, err := s.router.ForProject(project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	// All Rationale nodes — filtered client-side so we can return the
	// per-kind counts alongside the kind-filtered result set.
	nodes, err := st.FindNodesByLabel(project, "Rationale")
	if err != nil {
		return errResult(fmt.Sprintf("query rationale nodes: %v", err)), nil
	}

	countByKind := map[string]int{}
	for _, n := range nodes {
		k, _ := n.Properties["kind"].(string)
		if k != "" {
			countByKind[k]++
		}
	}

	out := findRationaleResult{
		Project:     project,
		KindFilter:  kind,
		CountByKind: countByKind,
	}

	if len(nodes) == 0 {
		out.Note = fmt.Sprintf(
			"No rationale nodes for project %q yet. Reindex with this binary to populate: index_repository(repo_path=..., force=true). "+
				"Rationale extraction runs automatically on every index.",
			project)
		out.Metadata = s.stdReadGraphMetadata(project)
		body, _ := json.MarshalIndent(out, "", "  ")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil
	}

	out.Entries = make([]rationaleEntry, 0, limit)
	for _, n := range nodes {
		k, _ := n.Properties["kind"].(string)
		if kind != "" && k != kind {
			continue
		}
		text, _ := n.Properties["text"].(string)

		entry := rationaleEntry{
			Kind:     k,
			Text:     text,
			FilePath: n.FilePath,
			Line:     n.StartLine,
		}

		// Follow RATIONALE_FOR to the subject (enclosing Function/Method/Class).
		edges, err := st.FindEdgesBySourceAndType(n.ID, "RATIONALE_FOR")
		if err == nil && len(edges) > 0 {
			if subject, _ := st.FindNodeByID(edges[0].TargetID); subject != nil {
				entry.SubjectQN = subject.QualifiedName
				entry.SubjectLbl = subject.Label
			}
		}

		out.Entries = append(out.Entries, entry)
		if len(out.Entries) >= limit {
			break
		}
	}
	out.TotalFound = len(out.Entries)

	if out.TotalFound == 0 && kind != "" {
		known := make([]string, 0, len(countByKind))
		for k := range countByKind {
			known = append(known, k)
		}
		out.Note = fmt.Sprintf(
			"No rationale entries with kind=%q. Known kinds in this project: %v. "+
				"Omit `kind` to see all, or use one of the known values.",
			kind, known)
	}
	out.Metadata = s.stdReadGraphMetadata(project)

	body, _ := json.MarshalIndent(out, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}
