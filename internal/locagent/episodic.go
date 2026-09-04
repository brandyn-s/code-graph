package locagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/brandyn-s/code-graph/internal/config"
	"github.com/brandyn-s/code-graph/internal/pipeline"
	"github.com/brandyn-s/code-graph/internal/store"
)

// Episodic memory retrieval — Phase C3 of the production-maturation plan.
//
// At agent-loop start, embed the issue text and cosine-search a separate
// IssueMemory project (default "episodic-memory-locbench") for the top-K
// similar past resolutions. Inject them into the initial user message
// as soft hints for the agent's first rank_by_query / code_localize call.
//
// References:
//   - RepoMem (arXiv 2510.01003): the architecture this implements
//   - Issue-corpus strategy note (2026-05-04):
//     corpus design and contamination guards
//   - bench/research/ingest_locbench_issues.py: corpus ingestion script
//
// Failure model: episodic retrieval is OPT-IN and best-effort. Any error
// (no Voyage key, missing project, embedding failure, empty corpus) logs
// a warning and returns nil hits — the agent loop proceeds without the
// section. This keeps the existing default-off code-graph behavior
// unchanged for callers that haven't run the C2 ingestion.
//
// Env vars:
//   LOCAGENT_EPISODIC_MEMORY=1   → enable. Default off.
//   LOCAGENT_EPISODIC_PROJECT=X  → override project name. Default "episodic-memory-locbench".
//   LOCAGENT_EPISODIC_TOP_K=N    → override top-K (default 3, max 10).

const (
	defaultEpisodicProject = "episodic-memory-locbench"
	defaultEpisodicTopK    = 3
	maxEpisodicTopK        = 10
	maxFilesPerHit         = 6
)

// EpisodicHit summarizes one retrieved past resolution for the prompt.
type EpisodicHit struct {
	QName        string   `json:"qualified_name"` // {org}/{repo}#{pr}
	Title        string   `json:"title"`
	ChangedFiles []string `json:"changed_files"`
	Score        float64  `json:"score"`
	MergedAt     string   `json:"merged_at,omitempty"`
}

// retrieveEpisodicMemory queries the IssueMemory project for hits similar
// to the issue text. Returns (nil, nil) when retrieval is disabled or
// silently degraded — callers should treat that as "no episodic hint."
func retrieveEpisodicMemory(ctx context.Context, issue string) ([]EpisodicHit, error) {
	if config.Get(config.LocAgentEpisodic) != "1" {
		return nil, nil
	}

	proj := config.Get(config.LocAgentEpisodicProj)
	if proj == "" {
		proj = defaultEpisodicProject
	}

	topK := defaultEpisodicTopK
	if v := config.Get(config.LocAgentEpisodicTopK); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxEpisodicTopK {
			topK = n
		}
	}

	vc := pipeline.NewVoyageClient()
	if vc == nil {
		slog.Warn("locagent.episodic.skip", "reason", "no embedding provider configured")
		return nil, nil
	}

	st, err := store.Open(proj)
	if err != nil {
		slog.Warn("locagent.episodic.skip", "reason", "open episodic store", "project", proj, "err", err)
		return nil, nil
	}
	defer st.Close()

	qvec, err := vc.EmbedSingle(ctx, issue, "query")
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	hits, err := st.CosineSearch(proj, qvec, topK)
	if err != nil {
		return nil, fmt.Errorf("cosine search: %w", err)
	}
	if len(hits) == 0 {
		return nil, nil
	}

	out := make([]EpisodicHit, 0, len(hits))
	for _, h := range hits {
		node, ferr := st.FindNodeByID(h.NodeID)
		if ferr != nil || node == nil {
			continue
		}
		out = append(out, EpisodicHit{
			QName:        h.QName,
			Title:        propString(node.Properties, "pr_title"),
			ChangedFiles: propStringSlice(node.Properties, "changed_files"),
			Score:        h.Score,
			MergedAt:     propString(node.Properties, "merged_at"),
		})
	}
	return out, nil
}

// formatEpisodicSection renders hits as a markdown section to append to
// the agent's initial user message. Empty when len(hits)==0.
func formatEpisodicSection(hits []EpisodicHit) string {
	if len(hits) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n## Similar past issues (from a corpus of resolved PRs)\n\n")
	sb.WriteString("These are HISTORICAL resolutions for related-but-different issues " +
		"in adjacent repos. Use them as soft hints — the file paths suggest where to " +
		"look in the current codebase if the naming pattern is similar. The current " +
		"issue may need different files; verify before finalizing.\n\n")
	for i, h := range hits {
		files := h.ChangedFiles
		truncated := false
		if len(files) > maxFilesPerHit {
			files = files[:maxFilesPerHit]
			truncated = true
		}
		fmt.Fprintf(&sb, "%d. **%s** — %s\n", i+1, h.QName, h.Title)
		if len(files) > 0 {
			fmt.Fprintf(&sb, "   Files changed: %s", strings.Join(files, ", "))
			if truncated {
				sb.WriteString(", ...")
			}
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "   (semantic similarity: %.3f)\n\n", h.Score)
	}
	return sb.String()
}

// propString extracts a string from a map[string]any properties bag.
// Handles the common cases (string value, missing key, non-string value).
func propString(props map[string]any, key string) string {
	if v, ok := props[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// propStringSlice extracts a string slice from properties. Properties are
// loaded via json.Unmarshal which renders JSON arrays as []any — convert
// to []string here, dropping non-string elements.
func propStringSlice(props map[string]any, key string) []string {
	v, ok := props[key]
	if !ok {
		return nil
	}
	// Most likely shape: []any of strings (from json.Unmarshal of []string).
	switch arr := v.(type) {
	case []any:
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return arr
	case json.RawMessage:
		var s []string
		if err := json.Unmarshal(arr, &s); err == nil {
			return s
		}
	}
	return nil
}
