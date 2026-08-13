// Package localize provides graph-guided code localization — given a
// natural-language issue or question, returns the top-K code entities
// most relevant to investigate.
//
// This is a primitives-only implementation of the LocAgent pattern
// (ACL 2025, arXiv 2503.09089). Where LocAgent runs an LLM agent loop
// that selects multi-hop expansion steps, this package executes a
// deterministic graph-traversal pipeline:
//
//  1. Match seeds via the existing ranking.RankByQuery PageRank scorer
//     (handles tokenization, seed matching, and bidirectional ranking).
//  2. BFS from each seed up to `depth` steps over allowed edge types.
//  3. Score visited nodes by seed_score / (1 + distance) and aggregate
//     across paths (a node reached from multiple seeds wins).
//  4. Return top-K by combined score with file_path + qualified_name
//     for each entry so callers (Opus, Cursor, etc.) can fetch source
//     directly via get_code_snippet.
//
// LocAgent's published F1 (92.7% file-level localization) comes from
// the full LLM-in-the-loop variant. The primitives-only variant here
// puts the structural-traversal layer in code-graph; client LLMs do
// the final relevance judgment when consuming the ranked output. This
// trades some accuracy for determinism, latency, and not requiring an
// LLM credential inside the MCP server.
package localize

import (
	"context"
	"fmt"
	"sort"

	"github.com/DeusData/codebase-memory-mcp/internal/ranking"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// LocalizedEntity is one entry in the CodeLocalize result.
type LocalizedEntity struct {
	ID            int64    `json:"id"`
	Label         string   `json:"label"`
	Name          string   `json:"name"`
	QualifiedName string   `json:"qualified_name"`
	FilePath      string   `json:"file_path"`
	StartLine     int      `json:"start_line"`
	EndLine       int      `json:"end_line"`
	Score         float64  `json:"score"`
	Distance      int      `json:"distance"`    // shortest distance from any seed (0 = seed itself)
	ReachedVia    []string `json:"reached_via"` // edge types traversed to reach this node
}

// AllowedEdgeTypes lists the edge types BFS will traverse. Restricted to
// the relationships that "localization" cares about; broader edges
// (TESTS, FILE_CHANGES_WITH, HTTP_CALLS) are intentionally excluded to
// avoid bringing in test files and infrastructure noise.
var AllowedEdgeTypes = map[string]bool{
	"CALLS":          true,
	"DEFINES":        true,
	"DEFINES_METHOD": true,
	"IMPORTS":        true,
	"CONTAINS":       true,
	"MEMBER_OF":      true,
	"USES_TYPE":      true,
	"IMPLEMENTS":     true,
	"OVERRIDE":       true,
}

// Default values matching the MCP tool schema.
const (
	defaultDepth     = 3
	defaultTopK      = 10
	maxDepth         = 5
	maxTopK          = 50
	maxSeedsExpanded = 20 // BFS from at most N seeds (tradeoff: precision vs recall)
)

// CodeLocalize takes a natural-language issue/question and returns the
// top-K graph entities most relevant to investigate. depth controls the
// BFS expansion radius from seed nodes; topK is clamped to [1, 50].
//
// Uses substring seed matching for backward compatibility. Use
// CodeLocalizeWithStrategy to choose substring / embedding / hybrid.
func CodeLocalize(st *store.Store, project, issue string, depth, topK int) ([]LocalizedEntity, error) {
	return CodeLocalizeWithStrategy(context.Background(), st, project, issue, depth, topK, ranking.SeedStrategySubstring)
}

// CodeLocalizeWithStrategy is CodeLocalize with explicit seed-strategy
// selection. Hybrid is recommended for natural-language issues — it
// merges substring (catches identifier-exact queries) with embedding
// (catches intent-style queries that don't share substrings with names).
func CodeLocalizeWithStrategy(ctx context.Context, st *store.Store, project, issue string, depth, topK int, strategy ranking.SeedStrategy) ([]LocalizedEntity, error) {
	if st == nil {
		return nil, fmt.Errorf("nil store")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if issue == "" {
		return nil, fmt.Errorf("issue description is required")
	}
	if depth < 0 {
		depth = defaultDepth
	}
	if depth > maxDepth {
		depth = maxDepth
	}
	if topK < 1 {
		topK = defaultTopK
	}
	if topK > maxTopK {
		topK = maxTopK
	}

	// Step 1: resolve seed nodes via the requested strategy. For natural-
	// language issues, hybrid combines substring matches (identifier-exact)
	// with embedding matches (intent-similar) — the empirical Loc-Bench
	// gap on substring-only motivated this option.
	seedNodes, err := ranking.MatchSeedNodesByStrategy(ctx, st, project, issue, strategy)
	if err != nil {
		return nil, fmt.Errorf("seed match (strategy=%s): %w", strategy, err)
	}
	if len(seedNodes) == 0 {
		return nil, fmt.Errorf("no nodes matched issue with strategy=%s — try a different query or seed_strategy", strategy)
	}
	// Cap the number of seeds we expand from to avoid combinatorial blowup
	// on highly-ambiguous queries.
	if len(seedNodes) > maxSeedsExpanded {
		seedNodes = seedNodes[:maxSeedsExpanded]
	}
	seeds := make([]ranking.RankedNode, 0, len(seedNodes))
	for _, n := range seedNodes {
		seeds = append(seeds, ranking.RankedNode{
			ID:            n.ID,
			Label:         n.Label,
			Name:          n.Name,
			QualifiedName: n.QualifiedName,
			FilePath:      n.FilePath,
			Score:         ranking.SeedMatchScore(n, issue),
		})
	}

	// Step 2: load only the endpoint columns and relationship types BFS can
	// traverse. Loading every edge used to materialize unrelated relationship
	// families and decode their JSON properties even though localization only
	// needs topology; that dominates latency and memory on million-edge graphs.
	edgeTypes := make([]string, 0, len(AllowedEdgeTypes))
	for edgeType := range AllowedEdgeTypes {
		edgeTypes = append(edgeTypes, edgeType)
	}
	sort.Strings(edgeTypes)
	allEdges, err := st.FindEdgeEndpointsByTypes(project, edgeTypes)
	if err != nil {
		return nil, fmt.Errorf("load localization edges: %w", err)
	}
	outAdj := make(map[int64][]edgeRef, len(allEdges))
	inAdj := make(map[int64][]edgeRef, len(allEdges))
	for _, e := range allEdges {
		outAdj[e.SourceID] = append(outAdj[e.SourceID], edgeRef{other: e.TargetID, etype: e.Type})
		inAdj[e.TargetID] = append(inAdj[e.TargetID], edgeRef{other: e.SourceID, etype: e.Type})
	}
	for id := range outAdj {
		sortEdgeRefs(outAdj[id])
	}
	for id := range inAdj {
		sortEdgeRefs(inAdj[id])
	}

	// Step 3: BFS from each seed, accumulating min-distance and
	// per-node aggregate score.
	visited := make(map[int64]*localizedAccumulator)
	for _, seed := range seeds {
		bfsExpand(seed, depth, outAdj, inAdj, visited)
	}

	// Step 4: load node details for every visited node. Single-shot
	// FindNodesByIDs to avoid N+1 queries.
	ids := make([]int64, 0, len(visited))
	for id := range visited {
		ids = append(ids, id)
	}
	nodeMap, err := st.FindNodesByIDs(ids)
	if err != nil {
		return nil, fmt.Errorf("load node details: %w", err)
	}

	// Step 5: rank and return top-K.
	results := make([]LocalizedEntity, 0, len(visited))
	for id, acc := range visited {
		node, ok := nodeMap[id]
		if !ok {
			continue
		}
		// BFS reaches Community pseudo-nodes (via MEMBER_OF) and external
		// stubs (via CALLS_EXTERNAL); neither is an openable code location.
		if !store.IsSurfaceableCodeNode(node.Label, node.FilePath) {
			continue
		}
		results = append(results, LocalizedEntity{
			ID:            node.ID,
			Label:         node.Label,
			Name:          node.Name,
			QualifiedName: node.QualifiedName,
			FilePath:      node.FilePath,
			StartLine:     node.StartLine,
			EndLine:       node.EndLine,
			Score:         acc.score,
			Distance:      acc.distance,
			ReachedVia:    edgeTypesUnique(acc.edgeTypes),
		})
	}

	sortByScoreDesc(results)
	if topK > len(results) {
		topK = len(results)
	}
	return results[:topK], nil
}

// localizedAccumulator tracks the running state for a node across BFS expansions.
type localizedAccumulator struct {
	score     float64
	distance  int             // min distance from any seed
	edgeTypes map[string]bool // edge types traversed across all paths reaching this node
}

type edgeRef struct {
	other int64
	etype string
}

func sortEdgeRefs(edges []edgeRef) {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].other != edges[j].other {
			return edges[i].other < edges[j].other
		}
		return edges[i].etype < edges[j].etype
	})
}

// bfsExpand walks `depth` steps out from the seed in both directions
// (forward and reverse), accumulating score and edge-type provenance.
// The seed itself is included with distance=0.
func bfsExpand(seed ranking.RankedNode, depth int, outAdj, inAdj map[int64][]edgeRef, visited map[int64]*localizedAccumulator) {
	type frontier struct {
		id       int64
		dist     int
		edgeUsed string // edge type that reached this node ("" for seed)
	}

	updateAcc := func(id int64, dist int, edgeType string, scoreContrib float64) {
		acc, ok := visited[id]
		if !ok {
			acc = &localizedAccumulator{
				distance:  dist,
				edgeTypes: make(map[string]bool),
			}
			visited[id] = acc
		}
		acc.score += scoreContrib
		if dist < acc.distance {
			acc.distance = dist
		}
		if edgeType != "" {
			acc.edgeTypes[edgeType] = true
		}
	}

	// Seed itself: full score, distance 0.
	updateAcc(seed.ID, 0, "", seed.Score)

	queue := []frontier{{id: seed.ID, dist: 0}}
	seenInThisBFS := map[int64]bool{seed.ID: true}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.dist >= depth {
			continue
		}

		// Decay factor: each hop halves the score contribution.
		nextScore := seed.Score / float64(int(1)<<uint(cur.dist+1))

		// Forward edges.
		for _, e := range outAdj[cur.id] {
			if seenInThisBFS[e.other] {
				continue
			}
			seenInThisBFS[e.other] = true
			updateAcc(e.other, cur.dist+1, e.etype, nextScore)
			queue = append(queue, frontier{id: e.other, dist: cur.dist + 1, edgeUsed: e.etype})
		}
		// Reverse edges (so BFS finds upstream callers, not just downstream callees).
		for _, e := range inAdj[cur.id] {
			if seenInThisBFS[e.other] {
				continue
			}
			seenInThisBFS[e.other] = true
			updateAcc(e.other, cur.dist+1, e.etype, nextScore)
			queue = append(queue, frontier{id: e.other, dist: cur.dist + 1, edgeUsed: e.etype})
		}
	}
}

func edgeTypesUnique(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	// Stable sort for determinism in tests and snapshot diffs.
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func sortByScoreDesc(results []LocalizedEntity) {
	sort.Slice(results, func(i, j int) bool {
		left, right := results[i], results[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if left.Distance != right.Distance {
			return left.Distance < right.Distance
		}
		if left.FilePath != right.FilePath {
			return left.FilePath < right.FilePath
		}
		if left.StartLine != right.StartLine {
			return left.StartLine < right.StartLine
		}
		if left.EndLine != right.EndLine {
			return left.EndLine < right.EndLine
		}
		if left.QualifiedName != right.QualifiedName {
			return left.QualifiedName < right.QualifiedName
		}
		if left.Label != right.Label {
			return left.Label < right.Label
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.ID < right.ID
	})
}
