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
	"os"
	"strconv"
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

// embeddingSeedMinCosineDefault is the default minimum cosine similarity
// required for a node to qualify as an embedding seed.
//
// History:
//   - PR #84 set this to 0.65 based on a single Loc-Bench observation
//     (pypa__pip-13085) where 0.50-0.65 hits were structural noise.
//   - The 2026-04-25 ablation (cosine=0.65 vs 0.0 on n=16) showed the
//     threshold filters out at least one legitimate seed that would
//     have lifted hybrid-primitives by 6pp file / 7pp func. Agent mode
//     was a wash. The original n=1 calibration did not generalize.
//   - Default lowered to 0.0 (effectively no threshold). The
//     EMBEDDING_SEED_MIN_COSINE env var keeps the knob for users who
//     observe noise on their workload.
const embeddingSeedMinCosineDefault = 0.0

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

	// Filter by minimum cosine threshold. Below ~0.65 the hits tend to be
	// structural noise (containers, generic helpers) rather than
	// semantically-related code; including them as seeds amplifies through
	// BFS into long lists of unrelated nodes.
	threshold := embeddingSeedMinCosineDefault
	if env := os.Getenv("EMBEDDING_SEED_MIN_COSINE"); env != "" {
		if v, perr := strconv.ParseFloat(env, 64); perr == nil && v >= 0 && v <= 1 {
			threshold = v
		}
	}
	out := make([]*store.Node, 0, len(hits))
	ids := make([]int64, 0, len(hits))
	for _, h := range hits {
		if h.Score < threshold {
			continue
		}
		ids = append(ids, h.NodeID)
	}
	if len(ids) == 0 {
		return nil, nil
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

// hybridEmbeddingDominanceThreshold is the number of embedding seeds
// at which hybrid mode drops the substring seeds entirely. Rationale
// (D1, 2026-05-07): when embedding produces ≥3 high-cosine matches,
// the query has a strong intent signal and substring seeds primarily
// add noise via incidental name overlap (PSM example: query
// "cradlepoint router WAN failover priority" — embedding picks the
// real handlers; substring also adds dozens of nodes whose QNs
// contain "router" as a substring, which then pollute the top-10
// via PageRank propagation). Below 3 embedding seeds, the embedding
// signal is weak and substring seeds remain useful as a fallback.
const hybridEmbeddingDominanceThreshold = 3

// MatchSeedNodesHybrid runs both substring and embedding matching and
// merges the results.
//
// Behavior (D1, 2026-05-07):
//   - If embedding returns ≥hybridEmbeddingDominanceThreshold seeds,
//     drop substring seeds entirely. The strong embedding signal is
//     a better intent match; substring at that point is mostly noise.
//   - Otherwise, merge: substring seeds first (preserving exact-
//     identifier match priority), embedding seeds appended.
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

	// Strong-embedding-signal short-circuit: drop substring entirely.
	if len(embeds) >= hybridEmbeddingDominanceThreshold {
		return embeds, nil
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
//
// Phase B (2026-05-07): when a caller explicitly requests
// SeedStrategySubstring AND embeddings are available (project has a
// non-zero embedding count), we route to hybrid instead. Substring on
// a project with embeddings consistently surfaces PageRank-propagation
// noise (`Result`, `IntoHandlerError`, `AsCheckOpResult` from
// cradlepoint-seeded callers) — D1 word-boundary closed seed pollution
// but did not address propagation pollution. Hybrid's embedding-
// dominance threshold (>=3 embeddings → drop substring entirely)
// was measured 10/10 relevant on the same query that produced 4/10
// noise on substring. Routing substring callers through hybrid when
// embeddings are present is strictly an improvement; substring stays
// available as the explicit fallback when no embeddings exist.
func MatchSeedNodesByStrategy(ctx context.Context, st *store.Store, project, query string, strategy SeedStrategy) ([]*store.Node, error) {
	switch strategy {
	case SeedStrategySubstring:
		// If embeddings are available for this project, route to hybrid
		// — it dominates substring on noise floor when both are usable.
		// If embeddings are unavailable (no API key OR project has no
		// embeddings), fall through to bare substring as the only option.
		if st != nil && project != "" {
			if count, err := st.EmbeddingCount(project); err == nil && count > 0 {
				return MatchSeedNodesHybrid(ctx, st, project, query)
			}
		}
		return MatchSeedNodes(st, project, query)
	case SeedStrategyEmbedding:
		return MatchSeedNodesByEmbedding(ctx, st, project, query)
	case SeedStrategyHybrid, "":
		return MatchSeedNodesHybrid(ctx, st, project, query)
	default:
		return nil, fmt.Errorf("unknown seed_strategy %q (allowed: substring, embedding, hybrid)", strategy)
	}
}
