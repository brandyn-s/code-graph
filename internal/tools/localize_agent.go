package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/DeusData/codebase-memory-mcp/internal/locagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type codeLocalizeAgentResult struct {
	IssueDescription string                     `json:"issue_description"`
	Project          string                     `json:"project"`
	TopK             int                        `json:"top_k"`
	Entities         []locagent.LocalizedEntity `json:"entities"`
	Turns            int                        `json:"turns"`
	StopReason       string                     `json:"stop_reason"`
	InputTokens      int                        `json:"input_tokens"`
	OutputTokens     int                        `json:"output_tokens"`
	Transcript       []locagent.TranscriptEntry `json:"transcript,omitempty"`
	Note             string                     `json:"note,omitempty"`
	Metadata         map[string]any             `json:"_metadata,omitempty"`
}

// stopReasonToBand maps the agent's terminal stop_reason to a confidence
// band. "finalized" (the agent voluntarily called finalize) is the high-
// confidence outcome; "max_turns" / "no_finalize" indicate the agent
// either ran out of budget or returned text without finalizing — low-
// confidence outcomes whose entity lists may be empty or partial.
func stopReasonToBand(stop string) string {
	switch stop {
	case "finalized":
		return "high"
	case "max_turns", "no_finalize":
		return "low"
	default:
		return "medium"
	}
}

// envOrDefault returns the env var value if set, otherwise fallback.
// Used to thread the configured ANTHROPIC_MODEL into provenance metadata
// without re-reading env in multiple call sites.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// handleCodeLocalizeAgent runs the LLM-driven LocAgent loop on top of
// our graph primitives. The agent calls rank_by_query and code_localize
// internally, iterating until it returns a finalized entity list.
func (s *Server) handleCodeLocalizeAgent(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	issue := getStringArg(args, "issue_description")
	if issue == "" {
		return errResult("issue_description is required (the issue/question to localize)"), nil
	}

	projectArg := getStringArg(args, "project")
	project := s.resolveProjectName(projectArg)
	if project == "" {
		return errResult("project is required (pass `project` explicitly or call from a session with a resolved project)"), nil
	}

	topK := getIntArg(args, "top_k", 10)
	includeTranscript := getBoolArg(args, "include_transcript")

	st, err := s.router.ForProject(project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	res, err := locagent.Run(ctx, st, project, issue, topK)
	if err != nil {
		return errResult(fmt.Sprintf("code_localize_agent: %v", err)), nil
	}

	out := codeLocalizeAgentResult{
		IssueDescription: issue,
		Project:          project,
		TopK:             topK,
		Entities:         res.Entities,
		Turns:            res.Turns,
		StopReason:       res.StopReason,
		InputTokens:      res.InputTokens,
		OutputTokens:     res.OutputTokens,
	}
	if includeTranscript {
		out.Transcript = res.Transcript
	}
	if res.StopReason == "max_turns" {
		out.Note = fmt.Sprintf("Agent hit %d-turn cap without finalizing. Output may be empty or partial.", res.Turns)
	} else if res.StopReason == "no_finalize" {
		out.Note = "Agent returned text without calling finalize(). Output is empty."
	}

	// Build a richer metadata block: standard read-graph freshness/provenance
	// PLUS the LLM's model name and a stop-reason-derived confidence band.
	// The band lets consumers tell "agent voluntarily finalized" (high) from
	// "agent ran out of budget" (low) without parsing the stop_reason string.
	indexedAt := ""
	if proj, _ := st.GetProject(project); proj != nil {
		indexedAt = proj.IndexedAt
	}
	out.Metadata = NewMetadataBuilder().
		WithFreshness(freshnessStateFromIndexedAt(indexedAt), indexedAt).
		WithProvenance("", "graph_db").
		WithModel(envOrDefault("ANTHROPIC_MODEL", "claude-haiku-4-5-20251001")).
		WithConfidence(stopReasonToBand(res.StopReason), "stop_reason="+res.StopReason).
		Build()

	body, _ := json.MarshalIndent(out, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}
