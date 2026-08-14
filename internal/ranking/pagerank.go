// Package ranking provides query-weighted PageRank over the code graph.
//
// Motivation: structural search (search_graph) and semantic search
// (find_similar_functions) surface candidates, but they don't rank by
// relevance to a query the way an agent needs for context assembly.
// PageRank with query-seeded personalization fills that gap — given a
// natural-language query, it returns the top-K graph entities most
// relevant to feed into an LLM's context window, typically reducing
// context tokens by 3-5x vs dumping the full graph.
//
// Algorithm: bidirectional weighted PageRank with personalization.
//
//   - Seed nodes are matched from the query via name + qualified-name tokens
//     (simple tokenizer; embedding-augmented seeds are a future extension).
//   - Forward PageRank: column-stochastic transition matrix over outbound
//     edges; propagates rank from seeds to nodes they reference.
//   - Reverse PageRank: same graph with edges reversed; propagates rank
//     from seeds back to nodes that reference them.
//   - Final score: sum of forward + reverse. Bidirectional fixes the
//     pure-source-collapse behavior that single-direction PageRank
//     exhibits (sources with no inbound personalization go to 0).
//
// Reference: Aider's repo-map (https://aider.chat/2023/10/22/repomap.html)
// pioneered PageRank over tree-sitter tags as an agent-context primitive.
// code-review-graph (github.com/tirth8205/code-review-graph) reports
// 6.8x token reduction with the same pattern.
package ranking

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// Default PageRank iteration count. 50 is conservative overkill — most
// graphs converge in 20-30 iterations at d=0.85.
const defaultIterations = 50

// Damping factor (teleport probability = 1 - damping). 0.85 is the
// original Brin/Page value; standard across PageRank implementations.
const defaultDamping = 0.85

// RankedNode is one entry in the RankByQuery result.
type RankedNode struct {
	ID            int64   `json:"id"`
	Label         string  `json:"label"`
	Name          string  `json:"name"`
	QualifiedName string  `json:"qualified_name"`
	FilePath      string  `json:"file_path"`
	Score         float64 `json:"score"`
}

// RankByQuery computes query-weighted bidirectional PageRank over the
// project graph and returns the top-K nodes by relevance, using the
// substring seed strategy (legacy default — kept for backward compat).
//
// New callers should prefer RankByQueryWithStrategy for the choice of
// substring / embedding / hybrid seed matching.
//
// The topK parameter is clamped to [1, 200]. The returned slice is
// sorted by descending score.
func RankByQuery(st *store.Store, project, query string, topK int) ([]RankedNode, error) {
	return RankByQueryWithStrategy(context.Background(), st, project, query, topK, SeedStrategySubstring)
}

// RankByQueryWithStrategy is RankByQuery with explicit seed-strategy
// selection (substring, embedding, or hybrid). Hybrid is the recommended
// default — it merges substring + embedding seeds, falling back to
// substring-only if embeddings are unavailable.
func RankByQueryWithStrategy(ctx context.Context, st *store.Store, project, query string, topK int, strategy SeedStrategy) ([]RankedNode, error) {
	if st == nil {
		return nil, fmt.Errorf("nil store")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if topK < 1 {
		topK = 1
	}
	if topK > 200 {
		topK = 200
	}

	nodes, err := st.AllNodes(project)
	if err != nil {
		return nil, fmt.Errorf("load nodes: %w", err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("project %q has no nodes; run index_repository first", project)
	}

	edges, err := st.AllEdges(project)
	if err != nil {
		return nil, fmt.Errorf("load edges: %w", err)
	}

	// Resolve seed nodes via the requested strategy. The seed-node IDs are
	// then translated to the compact graph indices used by the PageRank
	// kernel (matchSeeds returns indices, but for embedding/hybrid we get
	// nodes back and need to convert).
	seedNodes, err := MatchSeedNodesByStrategy(ctx, st, project, query, strategy)
	if err != nil {
		return nil, fmt.Errorf("seed match: %w", err)
	}
	if len(seedNodes) == 0 {
		return nil, fmt.Errorf("no nodes matched query %q with strategy=%s — try a different query or seed_strategy", query, strategy)
	}

	// Build nodeID -> compact index map upfront for both seed translation
	// and the PageRank kernel.
	idxOf := make(map[int64]int, len(nodes))
	for i, node := range nodes {
		idxOf[node.ID] = i
	}

	seedIdx := make([]int, 0, len(seedNodes))
	for _, sn := range seedNodes {
		if i, ok := idxOf[sn.ID]; ok {
			seedIdx = append(seedIdx, i)
		}
	}
	if len(seedIdx) == 0 {
		return nil, fmt.Errorf("seed nodes from strategy=%s did not map to project graph (stale store?)", strategy)
	}

	n := len(nodes)
	// Forward ranks (outbound edges as-is).
	forward := runPageRank(n, edges, idxOf, seedIdx, false)
	// Reverse ranks (edges flipped). Addresses pure-source-collapse:
	// nodes that only propagate outward would have 0 forward rank, but
	// they receive inbound rank from the reverse pass when seeds lead
	// into them.
	reverse := runPageRank(n, edges, idxOf, seedIdx, true)

	// Sum scores. Indices [0, n) align with `nodes`. Community pseudo-nodes
	// and external stubs (empty file_path) are dropped from the ranked
	// output — they accumulate PageRank from seeded callers but are not
	// openable code (trace.go's findSimilarNodes already excludes Community
	// for the same reason).
	combined := make([]RankedNode, 0, n)
	for i := 0; i < n; i++ {
		if !store.IsSurfaceableCodeNode(nodes[i].Label, nodes[i].FilePath) {
			continue
		}
		combined = append(combined, RankedNode{
			ID:            nodes[i].ID,
			Label:         nodes[i].Label,
			Name:          nodes[i].Name,
			QualifiedName: nodes[i].QualifiedName,
			FilePath:      nodes[i].FilePath,
			Score:         forward[i] + reverse[i],
		})
	}

	sortRankedNodes(combined)
	if topK > len(combined) {
		topK = len(combined)
	}
	return combined[:topK], nil
}

func sortRankedNodes(nodes []RankedNode) {
	sort.Slice(nodes, func(i, j int) bool {
		left, right := nodes[i], nodes[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if left.FilePath != right.FilePath {
			return left.FilePath < right.FilePath
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

// MatchSeedNodes returns the seed nodes for a query — the subset of
// project nodes whose Name exactly matches any query token (case-
// insensitive) or whose QualifiedName contains any token (case-
// insensitive substring). Used by callers that need just the seeds
// without running the full PageRank propagation (e.g., the localize
// package's BFS-from-seeds pattern).
//
// Returns the seed nodes themselves, not their indices. Empty slice if
// no node matched any token.
func MatchSeedNodes(st *store.Store, project, query string) ([]*store.Node, error) {
	if st == nil {
		return nil, fmt.Errorf("nil store")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	tokens := tokenize(query)
	qualifiedNameTokens := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if len(token) >= 3 {
			qualifiedNameTokens = append(qualifiedNameTokens, token)
		}
	}
	nodes, err := st.FindNodeSeedCandidates(project, tokens, qualifiedNameTokens)
	if err != nil {
		return nil, fmt.Errorf("load seed candidates: %w", err)
	}
	seedIdx := matchSeeds(nodes, query)
	out := make([]*store.Node, 0, len(seedIdx))
	for _, i := range seedIdx {
		out = append(out, nodes[i])
	}
	sortSeedNodes(out, query)
	return out, nil
}

type seedMatchQuality struct {
	exactNames     int
	qualifiedNames int
}

func seedQuality(node *store.Node, tokens []string, tokenRes []*regexp.Regexp) seedMatchQuality {
	var quality seedMatchQuality
	nameLower := strings.ToLower(node.Name)
	qnLower := strings.ToLower(node.QualifiedName)
	for index, token := range tokens {
		if nameLower == token {
			quality.exactNames++
		}
		if re := tokenRes[index]; re != nil && re.MatchString(qnLower) {
			quality.qualifiedNames++
		}
	}
	return quality
}

// SeedMatchScore preserves lexical seed quality when downstream graph
// traversal assigns its personalization weights. Exact-name matches retain
// precedence over qualified-name-only matches, while nodes matching more
// independent query tokens receive more weight. Embedding-only seeds receive
// the neutral score 1.
func SeedMatchScore(node *store.Node, query string) float64 {
	if node == nil {
		return 1
	}
	tokens := tokenize(query)
	tokenRes := compileTokenBoundaryRegexes(tokens)
	quality := seedQuality(node, tokens, tokenRes)
	if quality.exactNames == 0 && quality.qualifiedNames == 0 {
		return 1
	}
	return float64(quality.exactNames*(len(tokens)+1) + quality.qualifiedNames)
}

func sortSeedNodes(nodes []*store.Node, query string) {
	tokens := tokenize(query)
	tokenRes := compileTokenBoundaryRegexes(tokens)
	quality := make(map[int64]seedMatchQuality, len(nodes))
	for _, node := range nodes {
		quality[node.ID] = seedQuality(node, tokens, tokenRes)
	}

	sort.Slice(nodes, func(i, j int) bool {
		left, right := nodes[i], nodes[j]
		leftQuality, rightQuality := quality[left.ID], quality[right.ID]
		if leftQuality.exactNames != rightQuality.exactNames {
			return leftQuality.exactNames > rightQuality.exactNames
		}
		if leftQuality.qualifiedNames != rightQuality.qualifiedNames {
			return leftQuality.qualifiedNames > rightQuality.qualifiedNames
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

// matchSeeds returns the compact indices of nodes whose Name exactly
// matches any query token (case-insensitive) or whose QualifiedName
// matches any token at a word boundary (case-insensitive).
//
// Word-boundary matching (D1, 2026-05-07) replaces the prior bare
// substring match. Bare substring caused over-matching on common
// roots: query "router" matched `RouterMode`, `router_handler`, AND
// `routermanager` (where "router" is an internal substring). The
// PSM 2026-05-07 baseline showed `Result` / `IntoHandlerError` /
// `AsCheckOpResult` polluting the top-10 of `rank_by_query
// "cradlepoint router WAN failover priority"` — many came from
// substring-loose seeds amplified through PageRank.
//
// The boundary is Go's `\b` (word-char ↔ non-word-char transition).
// Lowercased QNs in this graph use `.`, `/`, `_`, and digits as
// separators between identifier segments — all non-word-char, so
// `\brouter\b` matches `cradlepoint.api.router.mode` but not
// `routermanager`. CamelCase remains literal for now (lowercased
// `RouterMode` → `routermode` has no boundary inside); D1 trades
// some CamelCase recall for substantial precision lift, with
// embedding-seed fallback covering CamelCase via cosine.
//
// Tokens with len < 3 (rare — only the 2-char shortGoMethodAllow
// entries `ok`, `io`, `id`) skip the QN regex entirely and fall back
// to exact-name match, since `\bok\b` against any QN containing the
// letters "ok" would still over-match.
func matchSeeds(nodes []*store.Node, query string) []int {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil
	}
	tokenRes := compileTokenBoundaryRegexes(tokens)
	seen := make(map[int]bool)
	var seeds []int
	for i, node := range nodes {
		nameLower := strings.ToLower(node.Name)
		qnLower := strings.ToLower(node.QualifiedName)
		for ti, tok := range tokens {
			if nameLower == tok {
				if !seen[i] {
					seen[i] = true
					seeds = append(seeds, i)
				}
				break
			}
			re := tokenRes[ti]
			if re != nil && re.MatchString(qnLower) {
				if !seen[i] {
					seen[i] = true
					seeds = append(seeds, i)
				}
				break
			}
		}
	}
	return seeds
}

// tokenBoundaryRegexCache caches compiled \btoken\b regexes across
// matchSeeds calls. Tokens are lowercased + bounded by tokenize, so
// the cache key is just the lowered token text. Cache is unbounded
// in count but tokens are tiny strings; on a long-running server,
// the universe of practical query tokens is small.
var (
	tokenBoundaryRegexCache   = sync.Map{} // tok string -> *regexp.Regexp
	tokenBoundaryRegexNilSent = (*regexp.Regexp)(nil)
)

// compileTokenBoundaryRegexes returns one regex per token (or nil for
// tokens shorter than 3 chars, for which boundary matching is unsafe).
// regexp.MustCompile would panic on a malformed token, but tokenize
// has already trimmed punctuation so QuoteMeta is sufficient defense.
func compileTokenBoundaryRegexes(tokens []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(tokens))
	for i, tok := range tokens {
		if len(tok) < 3 {
			out[i] = tokenBoundaryRegexNilSent
			continue
		}
		if cached, ok := tokenBoundaryRegexCache.Load(tok); ok {
			out[i] = cached.(*regexp.Regexp)
			continue
		}
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(tok) + `\b`)
		tokenBoundaryRegexCache.Store(tok, re)
		out[i] = re
	}
	return out
}

// tokenize splits the query on whitespace, lowercases, and filters
// noise tokens. Stopwords are common English words that match dozens of
// unrelated symbols (e.g. "install" matched all install-related code in
// pip's source plus vendored libs). Short tokens (<4 chars) are filtered
// because they over-match in code: "get" is a substring of every getter
// in the graph, so it provides almost no specificity. The
// shortGoMethodAllow allowlist preserves a small set of canonical short
// Go method names ("New", "Run", "Get", etc.) that users intentionally
// query with — those keep their seed-matching power.
func tokenize(query string) []string {
	raw := strings.Fields(strings.ToLower(query))
	tokens := make([]string, 0, len(raw))
	for _, tok := range raw {
		tok = strings.Trim(tok, ".,;:!?\"'()[]{}")
		if tok == "" {
			continue
		}
		if shortGoMethodAllow[tok] {
			tokens = append(tokens, tok)
			continue
		}
		if stopwords[tok] || len(tok) < 4 {
			continue
		}
		tokens = append(tokens, tok)
	}
	return tokens
}

// stopwords are English function words plus high-frequency code/English
// words that over-match in natural-language queries. Tuning history:
//   - Initial set (PR #78): English articles, conjunctions, question words.
//   - Expanded (PR after #82): added words observed in Loc-Bench instance
//     pypa__pip-13085 that polluted seed matches — "install", "code",
//     "execute", "wheel", "lazy" all matched dozens of unrelated symbols
//     in pip's source plus vendored libraries, and BFS amplified the noise.
var stopwords = map[string]bool{
	// articles, conjunctions, prepositions
	"the": true, "a": true, "an": true, "of": true, "and": true, "or": true,
	"to": true, "in": true, "on": true, "for": true, "is": true, "as": true,
	"at": true, "by": true, "be": true, "if": true, "it": true, "this": true,
	"that": true, "with": true, "from": true, "into": true, "than": true,
	// question words
	"how": true, "does": true, "do": true, "what": true, "where": true,
	"when": true, "why": true, "which": true, "who": true, "whom": true,
	// common verbs/nouns that over-match in code
	"work": true, "use": true, "make": true, "call": true, "calls": true,
	"show": true, "need": true, "want": true, "find": true, "look": true,
	"have": true, "will": true, "would": true, "could": true, "should": true,
	"must": true, "install": true, "create": true, "delete": true, "update": true,
	"return": true, "execute": true, "handle": true, "process": true,
	// high-frequency code/English words observed polluting Loc-Bench queries
	"code": true, "file": true, "type": true, "name": true, "test": true,
	"data": true, "error": true, "value": true, "item": true, "list": true,
	"line": true, "step": true, "case": true, "true": true, "false": true,
	"null": true, "none": true, "self": true, "main": true,
	// quantifiers / adverbs
	"some": true, "many": true, "more": true, "most": true, "also": true,
	"only": true, "just": true, "very": true, "even": true, "such": true,
}

// shortGoMethodAllow is a small allowlist of canonical short Go method
// names that should pass the min-length filter. These are conventional
// across Go codebases (constructors, lifecycle, getters/setters, error
// idioms) and users who query for them mean the method, not the English
// word. Kept short — every entry costs precision when the same word
// appears as natural language ("run the script", "get the data").
var shortGoMethodAllow = map[string]bool{
	"new": true, "run": true, "get": true, "set": true, "add": true,
	"del": true, "ok": true, "io": true, "err": true, "log": true,
	"id": true, "key": true, "url": true, "tcp": true, "udp": true,
	"dns": true, "tls": true, "ssh": true, "ssl": true, "api": true,
}

// runPageRank executes weighted PageRank with personalization on `seedIdx`.
// If `reverse` is true, edges are flipped before building the transition
// matrix (source ↔ target swapped).
func runPageRank(n int, edges []*store.Edge, idxOf map[int64]int, seedIdx []int, reverse bool) []float64 {
	// Build outbound degree (number of out-edges per node in the chosen
	// direction). We use uniform edge weights; the transition matrix is
	// column-stochastic when each outbound edge gets weight 1/outDegree.
	outDegree := make([]int, n)
	type edgeIdx struct{ src, dst int }
	compact := make([]edgeIdx, 0, len(edges))
	for _, e := range edges {
		s, ok1 := idxOf[e.SourceID]
		t, ok2 := idxOf[e.TargetID]
		if !ok1 || !ok2 || s == t {
			continue
		}
		if reverse {
			s, t = t, s
		}
		compact = append(compact, edgeIdx{src: s, dst: t})
		outDegree[s]++
	}

	// Personalization vector: uniform over seed set.
	pers := make([]float64, n)
	if len(seedIdx) > 0 {
		w := 1.0 / float64(len(seedIdx))
		for _, s := range seedIdx {
			pers[s] = w
		}
	} else {
		w := 1.0 / float64(n)
		for i := range pers {
			pers[i] = w
		}
	}

	rank := make([]float64, n)
	copy(rank, pers)

	for iter := 0; iter < defaultIterations; iter++ {
		next := make([]float64, n)
		// Handle dangling nodes (no outbound edges): their rank is
		// redistributed via the personalization vector.
		danglingMass := 0.0
		for i := 0; i < n; i++ {
			if outDegree[i] == 0 {
				danglingMass += rank[i]
			}
		}
		danglingContrib := defaultDamping * danglingMass
		for i := 0; i < n; i++ {
			next[i] += (1 - defaultDamping) * pers[i]
			next[i] += danglingContrib * pers[i]
		}
		// Distribute rank along edges.
		for _, e := range compact {
			next[e.dst] += defaultDamping * rank[e.src] / float64(outDegree[e.src])
		}
		rank = next
	}
	return rank
}
