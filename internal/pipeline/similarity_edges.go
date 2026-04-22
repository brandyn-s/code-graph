package pipeline

import (
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

	slog.Info("pass.similarity.start",
		"embedded_nodes", len(embeddedIDs),
		"threshold", threshold,
		"top_k", topK,
		"skip_hops", skipHops,
		"note", "O(N^2) cosine — expect ~100s per 1000 embedded nodes on a modern CPU; skip_hops filter adds BFS cost")

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

			if withinHops, err := p.pairWithinStructuralHops(sourceID, c.NodeID, skipHops); err != nil {
				// Treat graph-walk failure as "do not emit" rather than blocking
				// the whole pass — a best-effort filter.
				slog.Debug("pass.similarity.walk.err", "err", err)
				continue
			} else if withinHops {
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

// pairWithinStructuralHops returns true iff there is a path of length
// <= maxHops between a and b along any edge direction, using the existing
// CALLS / IMPORTS / USAGE / DEFINES / DEFINES_METHOD / MEMBER_OF edges.
// Small BFS on the already-populated edges table; maxHops is small
// (default 3) so total node visits are bounded.
func (p *Pipeline) pairWithinStructuralHops(a, b int64, maxHops int) (bool, error) {
	if a == b {
		return true, nil
	}
	if maxHops <= 0 {
		return false, nil
	}

	structuralTypes := []string{
		"CALLS", "ASYNC_CALLS",
		"IMPORTS", "USAGE",
		"DEFINES", "DEFINES_METHOD",
		"MEMBER_OF",
	}

	visited := map[int64]bool{a: true}
	frontier := []int64{a}

	for depth := 0; depth < maxHops; depth++ {
		if len(frontier) == 0 {
			break
		}

		// Pull edges outbound from the current frontier (we accept either
		// direction for the structural-connectivity check, since we only
		// want to exclude "already discoverable" pairs — directionality
		// doesn't matter for that).
		nextFrontier := frontier[:0:0]
		byID, err := p.Store.FindEdgesBySourceIDs(frontier, structuralTypes)
		if err != nil {
			return false, err
		}
		for _, edges := range byID {
			for _, e := range edges {
				if e.TargetID == b {
					return true, nil
				}
				if !visited[e.TargetID] {
					visited[e.TargetID] = true
					nextFrontier = append(nextFrontier, e.TargetID)
				}
			}
		}

		// Also pull inbound edges — catches "these two functions share a
		// common caller" as a short-path connection, which we want to
		// count as structurally related.
		byTarget, err := p.Store.FindEdgesByTargetIDs(frontier, structuralTypes)
		if err != nil {
			return false, err
		}
		for _, edges := range byTarget {
			for _, e := range edges {
				if e.SourceID == b {
					return true, nil
				}
				if !visited[e.SourceID] {
					visited[e.SourceID] = true
					nextFrontier = append(nextFrontier, e.SourceID)
				}
			}
		}

		frontier = nextFrontier
	}

	return false, nil
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
