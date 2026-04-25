// embedding_seeds.go — semantic-similarity seed matching.
//
// The original MatchSeedNodes uses substring on Name/QualifiedName, which
// fails when the query phrase doesn't literally appear as substrings in
// node identifiers (validated empirically: Loc-Bench pypa__pip-13085 missed
// `InstallCommand.run` despite the issue being about pip install). This
// file adds an embedding-based path: embed the query via Voyage, cosine-
// search against indexed node embeddings, return top-K node IDs as seeds.
//
// Hybrid mode merges substring + embedding seeds with deduplication —
// recommended default because substring catches identifier-exact queries
// (e.g., "AsyncPublisher") while embedding catches intent-style queries
// (e.g., "where install command runs").

package ranking

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// SeedStrategy controls how query → seed-node matching is performed.
type SeedStrategy string

const (
	// SeedStrategySubstring is the original behavior: tokens substring-match
	// node Name/QualifiedName. Cheap, no API call, deterministic. Best for
	// queries that contain known identifiers.
	SeedStrategySubstring SeedStrategy = "substring"
	// SeedStrategyEmbedding embeds the query via Voyage and cosine-searches
	// against pre-computed node embeddings. Requires VOYAGE_API_KEY at query
	// time and embeddings populated at index time. Best for natural-language
	// queries that describe intent rather than name symbols.
	SeedStrategyEmbedding SeedStrategy = "embedding"
	// SeedStrategyHybrid runs both and merges the results, deduplicated. The
	// default; gives the union of identifier-exact and intent-similar matches.
	SeedStrategyHybrid SeedStrategy = "hybrid"
)

// embeddingSeedTopK is the number of top cosine matches to return when
// using embedding-based seeding. Kept small because each seed costs a
// downstream BFS expansion.
const embeddingSeedTopK = 10

// MatchSeedNodesByEmbedding returns the top-K nodes whose embeddings are
// most cosine-similar to the embedded query. Returns an error wrapping
// ErrEmbeddingsUnavailable if VOYAGE_API_KEY is not set or the project
// has no embeddings populated.
func MatchSeedNodesByEmbedding(ctx context.Context, st *store.Store, project, query string) ([]*store.Node, error) {
	if st == nil {
		return nil, fmt.Errorf("nil store")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}

	count, err := st.EmbeddingCount(project)
	if err != nil {
		return nil, fmt.Errorf("embedding count: %w", err)
	}
	if count == 0 {
		return nil, fmt.Errorf("project %q has no embeddings; reindex with VOYAGE_API_KEY set", project)
	}

	vc := pipeline.NewVoyageClient()
	if vc == nil {
		return nil, fmt.Errorf("VOYAGE_API_KEY not set; cannot embed query")
	}

	// Voyage embedding of the query. inputType="query" is the documented
	// hint for asymmetric retrieval (documents indexed with type="document",
	// queries with type="query") which improves cosine match quality.
	embedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	queryVec, err := vc.EmbedSingle(embedCtx, query, "query")
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	hits, err := st.CosineSearch(project, queryVec, embeddingSeedTopK)
	if err != nil {
		return nil, fmt.Errorf("cosine search: %w", err)
	}
	if len(hits) == 0 {
		return nil, nil
	}

	out := make([]*store.Node, 0, len(hits))
	ids := make([]int64, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.NodeID)
	}
	nodeMap, err := st.FindNodesByIDs(ids)
	if err != nil {
		return nil, fmt.Errorf("resolve nodes: %w", err)
	}
	for _, id := range ids {
		if n, ok := nodeMap[id]; ok {
			out = append(out, n)
		}
	}
	return out, nil
}

// MatchSeedNodesHybrid runs both substring and embedding matching and
// merges the results, deduplicated by NodeID. Substring seeds appear
// first (preserving exact-identifier match priority) followed by any
// embedding seeds not already present.
//
// If embedding match fails (e.g., no VOYAGE_API_KEY or no embeddings),
// returns substring-only results with no error — graceful degradation
// is more useful than a hard failure for callers who don't know the
// project's embedding state in advance.
func MatchSeedNodesHybrid(ctx context.Context, st *store.Store, project, query string) ([]*store.Node, error) {
	subs, err := MatchSeedNodes(st, project, query)
	if err != nil {
		// Substring path failed for a real reason (nil store, bad project).
		return nil, err
	}

	embeds, embErr := MatchSeedNodesByEmbedding(ctx, st, project, query)
	if embErr != nil {
		// Embeddings unavailable — fall back to substring-only.
		return subs, nil
	}

	// Merge with dedup. Substring seeds first (caller relied on that
	// ordering historically); embedding seeds appended if not present.
	seen := make(map[int64]bool, len(subs)+len(embeds))
	merged := make([]*store.Node, 0, len(subs)+len(embeds))
	for _, n := range subs {
		if !seen[n.ID] {
			seen[n.ID] = true
			merged = append(merged, n)
		}
	}
	for _, n := range embeds {
		if !seen[n.ID] {
			seen[n.ID] = true
			merged = append(merged, n)
		}
	}
	return merged, nil
}

// MatchSeedNodesByStrategy dispatches to the requested seed strategy.
// Empty/unknown strategy defaults to hybrid.
func MatchSeedNodesByStrategy(ctx context.Context, st *store.Store, project, query string, strategy SeedStrategy) ([]*store.Node, error) {
	switch strategy {
	case SeedStrategySubstring:
		return MatchSeedNodes(st, project, query)
	case SeedStrategyEmbedding:
		return MatchSeedNodesByEmbedding(ctx, st, project, query)
	case SeedStrategyHybrid, "":
		return MatchSeedNodesHybrid(ctx, st, project, query)
	default:
		return nil, fmt.Errorf("unknown seed_strategy %q (allowed: substring, embedding, hybrid)", strategy)
	}
}
