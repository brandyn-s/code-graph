// Package locagent implements a LocAgent-style LLM-driven agent loop on
// top of code-graph's structural primitives. The published LocAgent
// (ACL 2025, arXiv 2503.09089) achieves 92.7% file-level localization
// on Loc-Bench by letting an LLM iteratively call graph-traversal tools
// (search_entity, explore_graph_structure, read_code_file).
//
// This package provides the equivalent in-process: an LLM session with
// tool-use enabled, where the tool implementations directly call into
// our internal/store + internal/ranking + internal/localize packages
// without round-tripping through MCP. The agent runs in the MCP server
// process; results return as a single MCP response.
//
// Tradeoffs vs the primitives-only code_localize tool:
//   - Adds an LLM call dependency (ANTHROPIC_API_KEY) at query time
//   - Higher latency per query (multi-turn, ~5-15 turns typical)
//   - Should achieve LocAgent's published F1 lift over substring-only
//     primitives because the LLM does intelligent narrowing the
//     primitives can't do alone
//
// Use code_localize for fast/deterministic primitives and
// code_localize_agent for accuracy-prioritized localization.
package locagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/anthropic"
	"github.com/DeusData/codebase-memory-mcp/internal/localize"
	"github.com/DeusData/codebase-memory-mcp/internal/ranking"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

const (
	// maxTurns caps the agent loop. 10 matches the LocAgent paper's
	// reported typical depth (most issues converge in 3-7 turns).
	maxTurns = 10

	// systemPrompt frames the agent's task and constrains the output.
	systemPrompt = `You are a code-localization agent. Your task: given an issue description,
identify the top code entities (functions, methods, classes) most relevant
to investigate or modify to address the issue.

You have these tools:
- rank_by_query(query, top_k): bidirectional PageRank, hybrid seed strategy.
- code_localize(issue, depth, top_k): BFS expansion from query-matched seeds.
- finalize(entities): YOU MUST call this when done. Pass {qualified_name, file_path, reason}.

CRITICAL: BUDGET — you have at most 5 useful turns. Finalize AGGRESSIVELY.

Strategy:
1. Turn 1: call rank_by_query with the issue description (or key symbols).
2. Turn 2: if the first call surfaced a likely entry-point class/function,
   call code_localize with that name to get the structural cluster.
3. Turn 3-4: ONE more refinement at most, only if results are clearly off-target.
4. FINALIZE by turn 5 at the latest. If you found an entry-point class
   (e.g. "InstallCommand"), include it AND its parent file — the user can
   look up specific methods themselves. Do NOT keep hunting for a specific
   method via rank_by_query — that tool ranks by name match, not method
   ownership. Stopping at the class is the correct answer.

If you've found a class or module that is a plausible site for the fix,
FINALIZE NOW. Over-exploration yields worse results than partial answers.`
)

// LocalizedEntity is the agent's final output entry. Format mirrors
// localize.LocalizedEntity for caller compatibility.
type LocalizedEntity struct {
	QualifiedName string `json:"qualified_name"`
	FilePath      string `json:"file_path"`
	Reason        string `json:"reason,omitempty"`
}

// Result is the full agent run result, including a transcript of tool
// calls for auditability.
type Result struct {
	Entities    []LocalizedEntity `json:"entities"`
	Turns       int               `json:"turns"`
	StopReason  string            `json:"stop_reason"` // "finalized", "max_turns", "no_finalize", "error"
	Transcript  []TranscriptEntry `json:"transcript,omitempty"`
	InputTokens int               `json:"input_tokens"`
	OutputTokens int              `json:"output_tokens"`
}

// TranscriptEntry is one step of the agent's execution.
type TranscriptEntry struct {
	Turn     int    `json:"turn"`
	Kind     string `json:"kind"` // "tool_call" | "tool_result" | "text" | "finalize"
	ToolName string `json:"tool_name,omitempty"`
	Summary  string `json:"summary"` // human-readable summary of input/output
}

// Run executes the agent loop. Returns the final entity list (capped to
// topK) along with a transcript for debuggability.
func Run(ctx context.Context, st *store.Store, project, issue string, topK int) (*Result, error) {
	if topK < 1 {
		topK = 10
	}
	if topK > 50 {
		topK = 50
	}

	client := anthropic.NewClient()
	if client == nil {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY not set; code_localize_agent requires an Anthropic credential")
	}

	tools := []anthropic.Tool{
		{
			Name:        "rank_by_query",
			Description: "PageRank ranking with hybrid seed matching. Returns top_k nodes most relevant to query.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string"},
					"top_k": {"type": "integer", "default": 20}
				},
				"required": ["query"]
			}`),
		},
		{
			Name:        "code_localize",
			Description: "BFS expansion from query-matched seeds. Returns top_k nodes with file:line. Use after rank_by_query identifies a probable entry point.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"issue": {"type": "string"},
					"depth": {"type": "integer", "default": 3},
					"top_k": {"type": "integer", "default": 10}
				},
				"required": ["issue"]
			}`),
		},
		{
			Name:        "finalize",
			Description: "Submit the final ranked list. MUST be called before agent terminates.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"entities": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"qualified_name": {"type": "string"},
								"file_path": {"type": "string"},
								"reason": {"type": "string"}
							},
							"required": ["qualified_name", "file_path"]
						}
					}
				},
				"required": ["entities"]
			}`),
		},
	}

	// Initial user message: the issue + topK budget.
	userInitial := fmt.Sprintf("Issue:\n%s\n\nReturn at most %d entities ranked best-first.", issue, topK)
	messages := []anthropic.Message{
		{
			Role:    "user",
			Content: []anthropic.ContentBlock{{Type: "text", Text: userInitial}},
		},
	}

	result := &Result{Transcript: make([]TranscriptEntry, 0, maxTurns*2)}

	for turn := 1; turn <= maxTurns; turn++ {
		resp, err := client.CreateMessage(ctx, anthropic.MessagesRequest{
			System:   systemPrompt,
			Messages: messages,
			Tools:    tools,
		})
		if err != nil {
			result.StopReason = "error"
			return result, fmt.Errorf("turn %d: %w", turn, err)
		}
		result.InputTokens += resp.Usage.InputTokens
		result.OutputTokens += resp.Usage.OutputTokens
		result.Turns = turn

		// Append assistant response to messages.
		messages = append(messages, anthropic.Message{
			Role:    "assistant",
			Content: resp.Content,
		})

		// Process content blocks: collect tool_use calls, execute, build
		// a single user message with tool_result blocks for the next turn.
		var toolResults []anthropic.ContentBlock
		finalized := false
		for _, block := range resp.Content {
			switch block.Type {
			case "text":
				result.Transcript = append(result.Transcript, TranscriptEntry{
					Turn:    turn,
					Kind:    "text",
					Summary: truncate(block.Text, 200),
				})
			case "tool_use":
				summary := fmt.Sprintf("%s(%s)", block.Name, truncate(string(block.Input), 120))
				result.Transcript = append(result.Transcript, TranscriptEntry{
					Turn:     turn,
					Kind:     "tool_call",
					ToolName: block.Name,
					Summary:  summary,
				})
				if block.Name == "finalize" {
					if err := handleFinalize(block.Input, topK, result); err != nil {
						// Don't terminate — let the model retry. Send an error result back.
						toolResults = append(toolResults, anthropic.ContentBlock{
							Type:              "tool_result",
							ToolUseID:         block.ID,
							IsError:           true,
							ToolResultContent: string(jsonMust(map[string]string{"error": err.Error()})),
						})
						continue
					}
					finalized = true
					result.StopReason = "finalized"
					result.Transcript = append(result.Transcript, TranscriptEntry{
						Turn:    turn,
						Kind:    "finalize",
						Summary: fmt.Sprintf("%d entities", len(result.Entities)),
					})
					break
				}
				toolOut, err := dispatchTool(ctx, st, project, block.Name, block.Input)
				if err != nil {
					toolResults = append(toolResults, anthropic.ContentBlock{
						Type:              "tool_result",
						ToolUseID:         block.ID,
						IsError:           true,
						ToolResultContent: string(jsonMust(map[string]string{"error": err.Error()})),
					})
					result.Transcript = append(result.Transcript, TranscriptEntry{
						Turn:     turn,
						Kind:     "tool_result",
						ToolName: block.Name,
						Summary:  "ERROR: " + err.Error(),
					})
					continue
				}
				resultJSON, _ := json.Marshal(toolOut)
				toolResults = append(toolResults, anthropic.ContentBlock{
					Type:              "tool_result",
					ToolUseID:         block.ID,
					ToolResultContent: string(resultJSON),
				})
				result.Transcript = append(result.Transcript, TranscriptEntry{
					Turn:     turn,
					Kind:     "tool_result",
					ToolName: block.Name,
					Summary:  truncate(string(resultJSON), 200),
				})
			}
		}

		if finalized {
			return result, nil
		}

		// If no tool calls were made and stop_reason is end_turn, the model
		// chose to stop without finalizing. Treat as no_finalize and exit.
		if resp.StopReason == "end_turn" && len(toolResults) == 0 {
			result.StopReason = "no_finalize"
			return result, nil
		}

		// Feed tool results into the next turn.
		if len(toolResults) > 0 {
			messages = append(messages, anthropic.Message{
				Role:    "user",
				Content: toolResults,
			})
		}
	}

	// Hit the turn cap. Fill in whatever we have.
	if result.StopReason == "" {
		result.StopReason = "max_turns"
	}
	return result, nil
}

func handleFinalize(input json.RawMessage, topK int, result *Result) error {
	var args struct {
		Entities []LocalizedEntity `json:"entities"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return fmt.Errorf("parse finalize args: %w", err)
	}
	if len(args.Entities) == 0 {
		return fmt.Errorf("finalize called with empty entities array")
	}
	if len(args.Entities) > topK {
		args.Entities = args.Entities[:topK]
	}
	result.Entities = args.Entities
	return nil
}

// dispatchTool runs the named tool against the store. Returns a JSON-
// serializable value the LLM will see as the tool result.
func dispatchTool(ctx context.Context, st *store.Store, project, name string, input json.RawMessage) (any, error) {
	switch name {
	case "rank_by_query":
		var args struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, fmt.Errorf("parse rank_by_query args: %w", err)
		}
		if args.TopK == 0 {
			args.TopK = 20
		}
		ranked, err := ranking.RankByQueryWithStrategy(ctx, st, project, args.Query, args.TopK, ranking.SeedStrategyHybrid)
		if err != nil {
			return nil, err
		}
		return ranked, nil

	case "code_localize":
		var args struct {
			Issue string `json:"issue"`
			Depth int    `json:"depth"`
			TopK  int    `json:"top_k"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, fmt.Errorf("parse code_localize args: %w", err)
		}
		if args.Depth == 0 {
			args.Depth = 3
		}
		if args.TopK == 0 {
			args.TopK = 10
		}
		hits, err := localize.CodeLocalizeWithStrategy(ctx, st, project, args.Issue, args.Depth, args.TopK, ranking.SeedStrategyHybrid)
		if err != nil {
			return nil, err
		}
		return hits, nil

	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func jsonMust(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"error":"json marshal failed"}`)
	}
	return b
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
