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
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/anthropic"
	"github.com/DeusData/codebase-memory-mcp/internal/localize"
	"github.com/DeusData/codebase-memory-mcp/internal/ranking"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

const (
	// maxTurnsDefault caps the agent loop. The LocAgent paper reports
	// typical convergence in 3-7 turns; the cap allows extended
	// exploration on hard cases. Override per-run via the LOCAGENT_MAX_TURNS
	// env var.
	maxTurnsDefault = 20

	// systemPromptAggressive is the original prompt: telegraphs a tight
	// 5-turn budget and pushes the agent to finalize quickly. Good when
	// the seed-matching is already on target; bad when the issue text
	// doesn't surface specific symbols and exploration is needed.
	systemPromptAggressive = `You are a code-localization agent. Your task: given an issue description,
identify the top code entities (functions, methods, classes) most relevant
to investigate or modify to address the issue.

You have these tools:
- rank_by_query(query, top_k): bidirectional PageRank, hybrid seed strategy.
- code_localize(issue, depth, top_k): BFS expansion from query-matched seeds.
- read_file(path, start_line, end_line): read a slice of a source file under
  the indexed project root. Use to verify a candidate when graph metadata
  alone is ambiguous (e.g. confirm a class actually has the method named
  in the issue).
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

	// systemPromptOpen encourages using read_file to verify candidates
	// while keeping a soft turn budget. Adopts LocAgent's 4-step CoT
	// structure (paper Figure 8): categorize → link → trace → locate.
	//
	// Tuning history: an earlier draft told the agent to "finalize when
	// confident, not on a turn budget" — under that prompt the agent
	// explored exhaustively (20 turns) and ran out of budget without
	// finalizing. Soft budget restored.
	systemPromptOpen = `You are a code-localization agent. Given a GitHub issue description,
your objective is to localize the specific files, classes, or functions
that need modification to resolve the issue.

Tools:
- rank_by_query(query, top_k): graph PageRank seeded on query tokens + embeddings.
  Returns entities most relevant to the query.
- code_localize(issue, depth, top_k): BFS expansion over CALLS / DEFINES /
  IMPORTS / MEMBER_OF edges from query-matched seeds. Multi-hop reasoning.
- read_file(path, start_line, end_line): read a slice of a source file
  (paths relative to project root, line range capped at 200). Use to
  CONFIRM a candidate when the graph alone can't tell sibling methods
  apart, or to recover entities the extractor missed.
- finalize(entities): MUST be called when done. {qualified_name, file_path, reason}.

Follow these 4 steps to localize the issue:

## Step 1: Categorize and Extract Key Problem Information
- Classify the issue: bug report, feature request, performance, security.
- Identify modules/symbols mentioned in the issue: function names, class
  names, error messages, file paths, configuration keys.
- Note BOTH explicit references (function names quoted in the issue) AND
  implicit references (the conceptual area the issue describes).

## Step 2: Locate Referenced Modules
- Call rank_by_query with the most specific identifiers from Step 1.
- If the issue mentions a class or function name directly, that's a
  high-priority seed.
- If the issue is prose-only, extract the most distinctive terms and
  query with those.

## Step 3: Reconstruct the Execution Flow
- Identify the entry point (the user-facing API, command, or entry
  function the issue describes).
- Trace function calls and dependencies via code_localize from the entry
  point. Multi-hop traversal: depth=4 default reaches most call chains.
- For unexpected-behavior issues: focus on modules that contain the buggy
  logic. For feature requests: identify where new behavior plugs in.

## Step 4: Locate Areas for Modification
- Pinpoint the suspicious entities (file, class, function) that need
  modification.
- If certainty is low, use read_file (sparingly — max 2-3 calls) to
  confirm a class actually has the method named in the issue, or to
  inspect call sites.
- Rank the entities by relevance: most-likely-to-need-change first.
- Call finalize with the ranked list.

BUDGET: aim to finalize within 8 turns. read_file calls count against
this. If two ranking calls agree on the file, that's your answer —
confirm and finalize. Partial answers (the right file + class, even if
the specific method is unclear) beat over-exploration.

DO NOT: read more than 3 files, hunt for documentation pages, or keep
calling rank_by_query with paraphrased queries hoping for better hits.`
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
//
// Iterations holds per-iteration entity lists when the agent runs in
// multi-iteration mode (LOCAGENT_ITERATIONS>=2). Iterations[i] is the
// finalized entity list of the i-th independent agent run BEFORE MRR
// aggregation. Empty for single-shot runs (iter=1) and for legacy
// callers that don't need per-iteration data. Surfaced for the Plan 4
// Loc-Bench failure-audit pipeline so the audit can distinguish:
//   - "rescued by iter 2" (entity appears only in Iterations[1])
//   - "iter 1 was sufficient" (entity appears in Iterations[0] at high rank)
//   - "iter 2 inconsistent with iter 1" (top-1 differs across iterations
//     — signal that the case is on the boundary of agent capability).
//
// The protocol is independent-sampling-with-MRR-aggregation: each
// iteration calls runOnce() with identical args (no conditioning on
// prior iteration results); aggregateByMRR(Iterations, topK) produces
// Entities. See runWithConsistency for the implementation.
type Result struct {
	Entities     []LocalizedEntity   `json:"entities"`
	Iterations   [][]LocalizedEntity `json:"iterations,omitempty"`
	Turns        int                 `json:"turns"`
	StopReason   string              `json:"stop_reason"` // "finalized", "max_turns", "no_finalize", "error"
	Transcript   []TranscriptEntry   `json:"transcript,omitempty"`
	InputTokens  int                 `json:"input_tokens"`
	OutputTokens int                 `json:"output_tokens"`
}

// TranscriptEntry is one step of the agent's execution.
type TranscriptEntry struct {
	Turn     int    `json:"turn"`
	Kind     string `json:"kind"` // "tool_call" | "tool_result" | "text" | "finalize"
	ToolName string `json:"tool_name,omitempty"`
	Summary  string `json:"summary"` // human-readable summary of input/output
}

// Run executes the agent. With LOCAGENT_ITERATIONS=N (default 2,
// max 3), runs the agent N times at temperature 1.0 (Anthropic API
// default) and aggregates results by mean reciprocal rank (MRR),
// matching the LocAgent paper's self-consistency strategy (Section 3.2,
// "Confidence Estimation Based on Consistency"). With N=1, behaves as
// a single-iteration agent (legacy behavior).
//
// MRR aggregation: for each iteration, an entity at rank R contributes
// 1/(R+1) to its score. Final score = sum across iterations. Ties broken
// by iteration count (entities seen in more iterations rank higher).
//
// Cost: scales linearly with N — 2 iterations = ~2x tokens.
func Run(ctx context.Context, st *store.Store, project, issue string, topK int) (*Result, error) {
	iters := 2
	if env := os.Getenv("LOCAGENT_ITERATIONS"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n >= 1 && n <= 3 {
			iters = n
		}
	}
	if iters == 1 {
		r, err := runOnce(ctx, st, project, issue, topK)
		// Plan 4 T1: even at iter=1, expose Iterations[0] for symmetry
		// with multi-iter runs. Downstream audit code can then assume
		// Iterations is always present and non-empty on success.
		if r != nil && err == nil && len(r.Entities) > 0 {
			r.Iterations = [][]LocalizedEntity{append([]LocalizedEntity(nil), r.Entities...)}
		}
		return r, err
	}
	return runWithConsistency(ctx, st, project, issue, topK, iters)
}

// runWithConsistency runs the agent N times and aggregates results by
// MRR. If an iteration errors mid-run, returns the aggregate of
// successful iterations with stop_reason="partial_consistency". If the
// first iteration errors, returns the error directly.
func runWithConsistency(ctx context.Context, st *store.Store, project, issue string, topK, iters int) (*Result, error) {
	var iterations []*Result
	aggregate := &Result{
		Transcript: make([]TranscriptEntry, 0, iters*16),
		StopReason: "consistency",
	}
	for i := 0; i < iters; i++ {
		r, err := runOnce(ctx, st, project, issue, topK)
		if r != nil {
			aggregate.InputTokens += r.InputTokens
			aggregate.OutputTokens += r.OutputTokens
			aggregate.Turns += r.Turns
			for _, te := range r.Transcript {
				te.Summary = fmt.Sprintf("[iter %d] %s", i+1, te.Summary)
				aggregate.Transcript = append(aggregate.Transcript, te)
			}
		}
		if err != nil {
			if i == 0 {
				return aggregate, err
			}
			aggregate.StopReason = "partial_consistency"
			break
		}
		if r != nil && len(r.Entities) > 0 {
			iterations = append(iterations, r)
		}
	}
	if len(iterations) == 0 {
		aggregate.StopReason = "no_finalize"
		return aggregate, nil
	}
	aggregate.Entities = aggregateByMRR(iterations, topK)
	// Plan 4 T1: surface per-iteration entity lists for failure-audit
	// pipeline. Each Iterations[i] is the entity list of the i-th
	// independent runOnce() call BEFORE MRR aggregation. Allows
	// downstream tooling to distinguish "rescued-by-iter-2" cases from
	// "iter-1-was-sufficient" cases without re-running the agent.
	aggregate.Iterations = make([][]LocalizedEntity, 0, len(iterations))
	for _, iter := range iterations {
		aggregate.Iterations = append(aggregate.Iterations, iter.Entities)
	}
	return aggregate, nil
}

// aggregateByMRR aggregates ranked entity lists from multiple iterations
// using mean reciprocal rank scoring. Entity identity is determined by
// qualified_name (falls back to file_path+reason if QN is empty).
func aggregateByMRR(iterations []*Result, topK int) []LocalizedEntity {
	type scored struct {
		entity LocalizedEntity
		score  float64
		seen   int
	}
	bucket := make(map[string]*scored)
	for _, iter := range iterations {
		for rank, e := range iter.Entities {
			key := e.QualifiedName
			if key == "" {
				key = e.FilePath + "::" + e.Reason
			}
			s := 1.0 / float64(rank+1)
			if existing, ok := bucket[key]; ok {
				existing.score += s
				existing.seen++
			} else {
				bucket[key] = &scored{entity: e, score: s, seen: 1}
			}
		}
	}
	ranked := make([]*scored, 0, len(bucket))
	for _, s := range bucket {
		ranked = append(ranked, s)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].seen > ranked[j].seen
	})
	out := make([]LocalizedEntity, 0, topK)
	for _, r := range ranked {
		out = append(out, r.entity)
		if len(out) >= topK {
			break
		}
	}
	return out
}

// runOnce executes a single agent loop iteration. Returns the final
// entity list (capped to topK) along with a transcript for debuggability.
func runOnce(ctx context.Context, st *store.Store, project, issue string, topK int) (*Result, error) {
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

	// Resolve project root for read_file safety check. If the project
	// row doesn't have a root_path (legacy index), read_file is disabled
	// — the agent still works without it, just at LocAgent-paper feature
	// parity minus read.
	var projectRoot string
	if proj, err := st.GetProject(project); err == nil && proj != nil {
		projectRoot = proj.RootPath
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
			Name:        "read_file",
			Description: "Read a slice of a source file under the indexed project root. Use to confirm a candidate (e.g. verify a class actually has the method named in the issue) or to recover entities the extractor missed. Path is relative to project root. Line range capped at 200 lines.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Source file path relative to project root (forward slashes)."},
					"start_line": {"type": "integer", "description": "1-indexed start line. Defaults to 1."},
					"end_line": {"type": "integer", "description": "1-indexed end line (inclusive). Defaults to start_line + 199."}
				},
				"required": ["path"]
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

	// Pick the system prompt variant. Default is "open" (the LocAgent-
	// style read-file-encouraged variant) — measured to lift n=16
	// hybrid-agent from 81%/31%/44% to 88%/44%/81% (PR #92), and combined
	// with the extractor fix (PR #94) to 94%/50%/88%. Set
	// LOCAGENT_PROMPT_VARIANT=aggressive to revert to the tighter
	// 5-turn-budget prompt. Unknown values fall back to open.
	systemPrompt := systemPromptOpen
	if os.Getenv("LOCAGENT_PROMPT_VARIANT") == "aggressive" {
		systemPrompt = systemPromptAggressive
	}

	maxTurns := maxTurnsDefault
	if env := os.Getenv("LOCAGENT_MAX_TURNS"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 && n <= 100 {
			maxTurns = n
		}
	}
	result := &Result{Transcript: make([]TranscriptEntry, 0, maxTurns*2)}

	// Optional question-rewriting pre-step (LOCAGENT_REWRITE=1). One
	// extra Haiku turn extracts focused search terms from prose-heavy
	// issues. Falls back to the original on any error. Cost is logged
	// against the run.
	rewrittenQuery := ""
	if os.Getenv("LOCAGENT_REWRITE") == "1" {
		rw, inTok, outTok, rerr := RewriteIssue(ctx, client, issue)
		result.InputTokens += inTok
		result.OutputTokens += outTok
		summary := rewriteResult{
			OriginalLen:  len(issue),
			Rewritten:    rw,
			InputTokens:  inTok,
			OutputTokens: outTok,
		}
		if rerr == nil && rw != issue && rw != "" {
			rewrittenQuery = rw
			result.Transcript = append(result.Transcript, TranscriptEntry{
				Turn:    0,
				Kind:    "rewrite",
				Summary: truncate(summary.jsonForTranscript(), 250),
			})
		} else {
			result.Transcript = append(result.Transcript, TranscriptEntry{
				Turn:    0,
				Kind:    "rewrite_fallback",
				Summary: truncate(summary.jsonForTranscript(), 250),
			})
		}
	}

	// Episodic memory retrieval (Phase C3, opt-in via LOCAGENT_EPISODIC_MEMORY=1).
	// Best-effort: any failure logs and continues without the section.
	episodicHits, eerr := retrieveEpisodicMemory(ctx, issue)
	if eerr != nil {
		// Soft-fail — don't break the agent loop on episodic retrieval issues.
		// The transcript records the failure for debuggability.
		result.Transcript = append(result.Transcript, TranscriptEntry{
			Turn:    0,
			Kind:    "episodic_error",
			Summary: truncate("retrieve failed: "+eerr.Error(), 200),
		})
	} else if len(episodicHits) > 0 {
		hitNames := make([]string, len(episodicHits))
		for i, h := range episodicHits {
			hitNames[i] = h.QName
		}
		result.Transcript = append(result.Transcript, TranscriptEntry{
			Turn:    0,
			Kind:    "episodic",
			Summary: fmt.Sprintf("retrieved %d hits: %s", len(episodicHits), strings.Join(hitNames, ", ")),
		})
	}
	episodicSection := formatEpisodicSection(episodicHits)

	// Initial user message: the issue + topK budget. If rewritten, append
	// the focused terms so the agent uses them in early rank_by_query
	// calls but still has the original prose for context. Episodic memory
	// section (when enabled) is appended between the issue/terms and the
	// "return at most N" instruction.
	var userInitial string
	if rewrittenQuery != "" {
		userInitial = fmt.Sprintf(
			"Issue:\n%s\n\nFocused search terms (extracted from the issue):\n%s%s\n\nReturn at most %d entities ranked best-first.",
			issue, rewrittenQuery, episodicSection, topK,
		)
	} else {
		userInitial = fmt.Sprintf("Issue:\n%s%s\n\nReturn at most %d entities ranked best-first.", issue, episodicSection, topK)
	}
	messages := []anthropic.Message{
		{
			Role:    "user",
			Content: []anthropic.ContentBlock{{Type: "text", Text: userInitial}},
		},
	}

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
				toolOut, err := dispatchTool(ctx, st, project, projectRoot, block.Name, block.Input)
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
func dispatchTool(ctx context.Context, st *store.Store, project, projectRoot, name string, input json.RawMessage) (any, error) {
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
			// Default depth=4 matches LocAgent's depth-4 BFS reach and
			// was measured (PR #92) to lift class+func hits over depth=3
			// without meaningfully increasing latency. LOCAGENT_BFS_DEPTH
			// overrides for ablations.
			args.Depth = 4
			if env := os.Getenv("LOCAGENT_BFS_DEPTH"); env != "" {
				if d, perr := strconv.Atoi(env); perr == nil && d >= 1 && d <= 6 {
					args.Depth = d
				}
			}
		}
		if args.TopK == 0 {
			args.TopK = 10
		}
		hits, err := localize.CodeLocalizeWithStrategy(ctx, st, project, args.Issue, args.Depth, args.TopK, ranking.SeedStrategyHybrid)
		if err != nil {
			return nil, err
		}
		return hits, nil

	case "read_file":
		var args readFileArgs
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, fmt.Errorf("parse read_file args: %w", err)
		}
		if projectRoot == "" {
			return map[string]string{
				"error": "project root not available; agent cannot read files (legacy index without root_path)",
			}, nil
		}
		return readFile(projectRoot, args)

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
