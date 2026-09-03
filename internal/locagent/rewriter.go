// rewriter.go — optional question-rewriting pre-step.
//
// Why this exists: the eval harness passes the issue's first paragraph
// to the agent. For some Loc-Bench instances that paragraph is
// descriptive prose without specific symbols, e.g.
//
//   "When users upload files via the chat interface, the server
//   currently doesn't enforce per-tenant size limits before passing
//   to storage. This causes occasional disk pressure on shared..."
//
// rank_by_query and code_localize need symbols (function names, class
// names, error messages) to seed-match against. Generic prose tokens
// either get filtered as stopwords or substring-match too many unrelated
// nodes.
//
// The rewriter calls a small Haiku turn that pulls likely-relevant
// identifiers out of the issue text — same role a human reader plays
// when paraphrasing "find where size validation should live" into
// "look for upload_file, size_limit, max_upload_size".
//
// Gated by LOCAGENT_REWRITE=1 so it stays opt-in. Failure is non-fatal:
// the original issue text is used if rewriting errors out.

package locagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brandyn-s/code-graph/internal/anthropic"
)

const rewriterSystemPrompt = `You extract code-search keywords from issue descriptions. Output a SHORT
list of likely-relevant identifiers and phrases that would appear in
the source code addressing this issue.

Output format: a single line with comma-separated terms, no other text.
Limit to 10 terms maximum. Prefer:
  - Specific function/class/method names if mentioned
  - Error messages or log strings if quoted
  - Domain nouns that map to code (e.g. "upload", "permissions",
    "session", "validate")
  - Module/file path components if mentioned

Avoid:
  - Generic English words ("issue", "bug", "code", "function")
  - Punctuation, articles, conjunctions
  - Long phrases — single words or 2-word phrases preferred`

// RewriteIssue calls the LLM to extract a focused list of search terms
// from a verbose issue description. Returns the rewritten query plus
// the input/output tokens consumed (so the caller can attribute cost).
//
// On any error, returns the original issue with a non-nil error. Caller
// is expected to fall back to the original on error rather than failing
// the whole run.
func RewriteIssue(ctx context.Context, client *anthropic.Client, issue string) (string, int, int, error) {
	if client == nil {
		return issue, 0, 0, fmt.Errorf("nil anthropic client")
	}
	prompt := fmt.Sprintf("Issue:\n%s\n\nKeywords (comma-separated, single line):", strings.TrimSpace(issue))
	req := anthropic.MessagesRequest{
		System:    rewriterSystemPrompt,
		MaxTokens: 200,
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: []anthropic.ContentBlock{
					{Type: "text", Text: prompt},
				},
			},
		},
	}
	resp, err := client.CreateMessage(ctx, req)
	if err != nil {
		return issue, 0, 0, fmt.Errorf("rewriter create_message: %w", err)
	}
	// Extract the first text block. Tool calls / refusals → fall back to
	// the original.
	var text string
	for _, b := range resp.Content {
		if b.Type == "text" {
			text = strings.TrimSpace(b.Text)
			break
		}
	}
	if text == "" {
		return issue, resp.Usage.InputTokens, resp.Usage.OutputTokens,
			fmt.Errorf("rewriter returned no text")
	}
	// Defensive: if the LLM ignored format and returned multi-line, take
	// the first line.
	if newline := strings.IndexByte(text, '\n'); newline > 0 {
		text = strings.TrimSpace(text[:newline])
	}
	// Defensive: if the LLM returned something tiny or returned the
	// original issue verbatim, fall back. The rewrite should be ≤ ~150
	// chars typically.
	if len(text) < 5 || len(text) > 500 {
		return issue, resp.Usage.InputTokens, resp.Usage.OutputTokens,
			fmt.Errorf("rewriter output length %d out of range [5, 500]", len(text))
	}
	return text, resp.Usage.InputTokens, resp.Usage.OutputTokens, nil
}

// rewriteResult is what RewriteIssue logs into the transcript so the
// audit trail shows the input → output transformation.
type rewriteResult struct {
	OriginalLen  int    `json:"original_len"`
	Rewritten    string `json:"rewritten"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// jsonForTranscript is a small helper for adding a structured rewrite
// summary to the agent transcript.
func (r rewriteResult) jsonForTranscript() string {
	b, _ := json.Marshal(r)
	return string(b)
}
