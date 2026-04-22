package pipeline

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// Defaults chosen by auditing pairs on mcp-servers at multiple thresholds
// and walking the precision curve. Overridable by env var so we can tune
// without re-releasing.
const (
	defaultSimilarityThreshold = 0.85
	defaultSimilarityTopK      = 5
	defaultSimilaritySkipHops  = 3
)

// passSimilarityEdges emits SEMANTICALLY_SIMILAR_TO edges between Function
// and Method nodes whose Voyage embeddings are close in cosine space AND
// that are NOT already connected via a short structural path
// (CALLS/IMPORTS/USAGE within defaultSimilaritySkipHops hops). The latter
// filter is what makes the edges useful: a function that calls its helper
// in the next file is trivially related and already discoverable via the
// structural graph; a function in a distant package that solves the same
// problem without any shared call path is the interesting signal — a
// refactor candidate or a duplicated pattern worth surfacing.
//
// Gate (all must hold):
//   - ENABLE_SIMILARITY_EDGES is truthy (off by default: the pass is O(N²)
//     cosine + a short graph walk per candidate pair and can add noticeable
//     indexing time on 10k+ embedded nodes).
//   - The project has at least one embedded node (the embeddings pass ran
//     successfully with VOYAGE_API_KEY set).
//
// Each emitted edge carries:
//   - Type:            SEMANTICALLY_SIMILAR_TO
//   - confidence_tier: INFERRED  (per PR 1's edge taxonomy)
//   - similarity_score: the cosine value, useful for Cypher filtering
//     (e.g. WHERE r.similarity_score > 0.92 for "probable copy-paste")
//
// Env overrides:
//
//	CODE_GRAPH_SIMILARITY_THRESHOLD  (float, default 0.85)
//	CODE_GRAPH_SIMILARITY_TOPK       (int, default 5)
//	CODE_GRAPH_SIMILARITY_SKIP_HOPS  (int, default 3)
func (p *Pipeline) passSimilarityEdges() {
	if !similarityEdgesEnabled() {
		slog.Info("pass.similarity.skip", "reason", "ENABLE_SIMILARITY_EDGES not set")
		return
	}

	threshold := envFloat("CODE_GRAPH_SIMILARITY_THRESHOLD", defaultSimilarityThreshold)
	topK := envInt("CODE_GRAPH_SIMILARITY_TOPK", defaultSimilarityTopK)
	skipHops := envInt("CODE_GRAPH_SIMILARITY_SKIP_HOPS", defaultSimilaritySkipHops)

	embeddedIDs, err := p.Store.IterEmbeddedNodeIDs(p.ProjectName)
	if err != nil {
		slog.Warn("pass.similarity.iter.err", "err", err)
		return
	}
	if len(embeddedIDs) == 0 {
		slog.Info("pass.similarity.skip", "reason", "no embeddings; run with VOYAGE_API_KEY to populate first")
		return
	}

	// Pre-build an in-memory structural adjacency map ONCE for the entire
	// pass. Before this change the hop filter hit SQLite twice per hop per
	// candidate pair (FindEdgesBySourceIDs + FindEdgesByTargetIDs): on
	// mcp-servers (1637 embedded nodes, topK*3=15 candidates each) that
	// was ~150k SQL queries in the hot path, dominating runtime at ~5-12
	// minutes. With the map in hand, withinHopsFromMap is a pure in-memory
	// BFS bounded by maxHops * average_degree — microseconds per pair.
	var adjacency map[int64]map[int64]struct{}
	if skipHops > 0 {
		adjacency, err = p.buildStructuralAdjacency()
		if err != nil {
			slog.Warn("pass.similarity.adjacency.err", "err", err, "hint", "falling back to skip_hops=0 (no structural filter)")
			skipHops = 0
		}
	}

	slog.Info("pass.similarity.start",
		"embedded_nodes", len(embeddedIDs),
		"threshold", threshold,
		"top_k", topK,
		"skip_hops", skipHops,
		"adjacency_nodes", len(adjacency),
		"note", "O(N^2) cosine dominates runtime; in-memory BFS adds negligible per-pair cost")

	// Deduplicate edges across the unordered pair (a,b) == (b,a). Track
	// written pairs by sorted IDs so a batch of N similarity queries from
	// N nodes doesn't double-insert.
	type pair struct{ a, b int64 }
	written := make(map[pair]bool)
	emitted := 0

	for scanned, sourceID := range embeddedIDs {
		// Heartbeat every 100 source nodes so a stuck pass is observable
		// and users can estimate progress on large projects.
		if scanned > 0 && scanned%100 == 0 {
			slog.Info("pass.similarity.progress",
				"scanned", scanned,
				"total", len(embeddedIDs),
				"edges_emitted", emitted)
		}
		// Scan a candidate pool larger than topK so filters can prune
		// without starving the final result.
		candidates, err := p.Store.FindSimilarNodes(p.ProjectName, sourceID, topK*3)
		if err != nil {
			slog.Warn("pass.similarity.query.err", "source_id", sourceID, "err", err)
			continue
		}

		accepted := 0
		for _, c := range candidates {
			if accepted >= topK {
				break
			}
			if c.Score < threshold {
				// Candidates come back sorted desc; once we drop below
				// threshold no later candidate can pass either.
				break
			}

			lo, hi := sourceID, c.NodeID
			if lo > hi {
				lo, hi = hi, lo
			}
			key := pair{lo, hi}
			if written[key] {
				continue
			}

			if skipHops > 0 && withinHopsFromMap(adjacency, sourceID, c.NodeID, skipHops) {
				continue // already structurally connected — uninteresting
			}

			_, err := p.Store.InsertEdge(&store.Edge{
				Project:  p.ProjectName,
				SourceID: sourceID,
				TargetID: c.NodeID,
				Type:     "SEMANTICALLY_SIMILAR_TO",
				Properties: map[string]any{
					"similarity_score": c.Score,
					"confidence_tier":  store.ConfidenceInferred,
				},
			})
			if err != nil {
				slog.Debug("pass.similarity.insert.err", "err", err)
				continue
			}
			written[key] = true
			accepted++
			emitted++
		}
	}

	slog.Info("pass.similarity.done",
		"edges_emitted", emitted,
		"source_nodes_considered", len(embeddedIDs))
}

// buildStructuralAdjacency materializes an in-memory undirected
// adjacency map over the structural edge types, used by the similarity
// pass's "already-connected within N hops?" filter. Loaded once at
// pass start so per-pair BFS is a pure in-memory walk.
//
// Memory footprint: on mcp-servers' 6,222 nodes × ~4 average structural
// neighbors ≈ 25k map entries ≈ <1 MB. Negligible vs the embedding cache.
func (p *Pipeline) buildStructuralAdjacency() (map[int64]map[int64]struct{}, error) {
	structuralTypes := []string{
		"CALLS", "ASYNC_CALLS",
		"IMPORTS", "USAGE",
		"DEFINES", "DEFINES_METHOD",
		"MEMBER_OF",
	}

	adj := make(map[int64]map[int64]struct{})
	add := func(a, b int64) {
		if adj[a] == nil {
			adj[a] = make(map[int64]struct{})
		}
		adj[a][b] = struct{}{}
	}

	for _, t := range structuralTypes {
		edges, err := p.Store.FindEdgesByType(p.ProjectName, t)
		if err != nil {
			return nil, fmt.Errorf("load %s edges: %w", t, err)
		}
		for _, e := range edges {
			add(e.SourceID, e.TargetID)
			add(e.TargetID, e.SourceID) // undirected for this filter
		}
	}
	return adj, nil
}

// withinHopsFromMap returns true iff there is a path of length <= maxHops
// between a and b in the in-memory adjacency map. Bounded BFS; runs in
// microseconds per pair, replacing 6 SQL queries per hop that the old
// pairWithinStructuralHops did.
func withinHopsFromMap(adj map[int64]map[int64]struct{}, a, b int64, maxHops int) bool {
	if a == b {
		return true
	}
	if maxHops <= 0 || adj == nil {
		return false
	}
	visited := map[int64]struct{}{a: {}}
	frontier := []int64{a}
	for hop := 0; hop < maxHops && len(frontier) > 0; hop++ {
		var next []int64
		for _, cur := range frontier {
			for neighbor := range adj[cur] {
				if neighbor == b {
					return true
				}
				if _, seen := visited[neighbor]; seen {
					continue
				}
				visited[neighbor] = struct{}{}
				next = append(next, neighbor)
			}
		}
		frontier = next
	}
	return false
}

// similarityEdgesEnabled returns true when the opt-in env var is set to
// a truthy value. Accepts the usual suspects to avoid the "but I set it
// to true" footgun.
func similarityEdgesEnabled() bool {
	v := strings.TrimSpace(os.Getenv("ENABLE_SIMILARITY_EDGES"))
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			return f
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
