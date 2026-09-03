package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/brandyn-s/code-graph/internal/pipeline"
	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type semanticMatch struct {
	File          string  `json:"file"`
	Name          string  `json:"name"`
	QualifiedName string  `json:"qualified_name"`
	Label         string  `json:"label"`
	Score         float64 `json:"score"`
}

// handleSearchCodeSemantic handles the search_code_semantic MCP tool.
// Uses Voyage AI embeddings for natural language code search.
func (s *Server) handleSearchCodeSemantic(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	query := getStringArg(args, "query")
	if query == "" {
		return errResult("query is required"), nil
	}

	limit := getIntArg(args, "limit", 10)
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}

	filePattern := getStringArg(args, "file_pattern")
	labelFilter := getStringArg(args, "label")
	projectName := getStringArg(args, "project")

	st, err := s.resolveStore(projectName)
	if err != nil {
		return errResult(err.Error()), nil
	}
	project := s.resolveProjectName(projectName)

	// Check if embeddings are available
	count, err := st.EmbeddingCount(project)
	if err != nil || count == 0 {
		return errResult(fmt.Sprintf(
			"No embeddings available for project %q. "+
				"Ensure VOYAGE_API_KEY is set and reindex with: index_repository(force=true). "+
				"Embeddings are generated automatically during indexing when the API key is present.",
			project)), nil
	}

	// Embed the query via Voyage API
	vc := pipeline.NewVoyageClient()
	if vc == nil {
		return errResult("VOYAGE_API_KEY not set. Cannot perform semantic search."), nil
	}

	queryVec, err := vc.EmbedSingle(ctx, query, "query")
	if err != nil {
		slog.Warn("semantic_search.embed.err", "err", err)
		return errResult(fmt.Sprintf("Failed to embed query: %v", err)), nil
	}

	// Search
	results, err := st.CosineSearch(project, queryVec, limit*3) // Over-fetch for filtering
	if err != nil {
		slog.Warn("semantic_search.search.err", "err", err)
		return errResult(fmt.Sprintf("Search failed: %v", err)), nil
	}

	// Apply filters
	var filtered []store.EmbeddingResult
	for _, r := range results {
		if filePattern != "" {
			matched, _ := matchGlob(filePattern, r.FilePath)
			if !matched {
				continue
			}
		}
		if labelFilter != "" && !strings.EqualFold(r.Label, labelFilter) {
			continue
		}
		filtered = append(filtered, r)
		if len(filtered) >= limit {
			break
		}
	}

	// Format output
	matches := make([]semanticMatch, len(filtered))
	for i, r := range filtered {
		matches[i] = semanticMatch{
			File:          r.FilePath,
			Name:          r.Name,
			QualifiedName: r.QName,
			Label:         r.Label,
			Score:         r.Score,
		}
	}

	indexedAt := ""
	if proj, _ := st.GetProject(project); proj != nil {
		indexedAt = proj.IndexedAt
	}
	metadata := NewMetadataBuilder().
		WithFreshness(freshnessStateFromIndexedAt(indexedAt), indexedAt).
		WithProvenance("", "graph_db").
		// Report the model actually used for the query embedding — the
		// stored document embeddings' model is upserted alongside them and
		// has always been this client's model. A hardcoded "voyage-4-large"
		// here shipped false provenance (real default: voyage-code-3).
		WithModel(vc.Model()).
		Build()

	out, _ := json.MarshalIndent(struct {
		Results    []semanticMatch `json:"results"`
		Count      int             `json:"count"`
		Embeddings int             `json:"embeddings_indexed"`
		Metadata   map[string]any  `json:"_metadata,omitempty"`
	}{
		Results:    matches,
		Count:      len(matches),
		Embeddings: count,
		Metadata:   metadata,
	}, "", "  ")

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
	}, nil
}

// matchGlob does simple glob matching (supports * and **).
func matchGlob(pattern, path string) (bool, error) {
	// Normalize separators
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	path = strings.ReplaceAll(path, "\\", "/")

	// Simple patterns
	if pattern == "*" {
		return true, nil
	}

	// Handle *.ext patterns
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(path, pattern[1:]), nil
	}

	// Handle dir/** patterns
	if strings.HasSuffix(pattern, "/**") {
		prefix := pattern[:len(pattern)-3]
		return strings.HasPrefix(path, prefix+"/") || path == prefix, nil
	}

	// Handle dir/* patterns
	if strings.HasSuffix(pattern, "/*") {
		prefix := pattern[:len(pattern)-2]
		return strings.HasPrefix(path, prefix+"/") && !strings.Contains(path[len(prefix)+1:], "/"), nil
	}

	// Exact match
	return path == pattern, nil
}
