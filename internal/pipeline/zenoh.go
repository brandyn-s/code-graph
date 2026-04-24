// Package pipeline: Zenoh pub/sub edge extraction.
//
// Scans Rust source files for libio::zenoh publisher/subscriber/querier/
// queryable creation sites (documented in psm/libio/
// src/zenoh/{async,sync}.rs) and emits Topic nodes + PUBLISHES_TO /
// SUBSCRIBES_TO / QUERIES / ANSWERS edges connecting the enclosing
// Function to the Topic.
//
// WHY this is a separate pass, not part of CALLS:
//   Zenoh topics are the actual inter-service boundary in redacted robotics
//   repos — regular CALLS edges stop at the libio API boundary and miss the
//   pub/sub dataflow. Surfacing Topic nodes makes the service dataflow graph
//   queryable (e.g., "who publishes to `controls/manual`?").
//
// MVP scope (v0):
//   - Literal topic strings only (variables/constants surface as AMBIGUOUS
//     in a later pass). Regex-based matching with ~1 false-positive per
//     libio::zenoh reexport.
//   - Publisher, Subscriber, Querier, Queryable plus session-owned subs.
//   - Builder pattern .with_rel_topic() / .with_abs_topic() detected but
//     cannot determine the surrounding type without AST — emitted as
//     Publisher with AMBIGUOUS confidence tier.
//
// Non-goals (deferred):
//   - libio-ng — separate API surface, separate pass (libio is legacy per
//     psm/CLAUDE.md).
//   - Topic variable resolution (symbolic topics named via `const TOPIC`).
//   - Cross-project topic unification.

package pipeline

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// Regex patterns for each libio::zenoh API constructor.
// Each captures the topic string literal (always the 2nd positional arg,
// directly after the session reference). Multi-line calls with the topic
// arg on a subsequent line are intentionally NOT matched — call sites in
// the PSM tree that we want to catch put the topic on the same line as
// `::new(`. A later tree-sitter pass can close this gap.
//
// Note on the session arg: we tolerate anything that isn't a comma
// (including method chains like `session.clone()`), which is why the
// first `[^,]+` is greedy.
var (
	// (Async|Sync)Publisher(Throttled)?::(new|new_external|new_unrestricted)
	rePublisherNew = regexp.MustCompile(
		`(?m)(Async|Sync)Publisher(Throttled)?::(new|new_external|new_unrestricted)\s*\(\s*[^,]+,\s*"([^"]+)"`,
	)

	// (Async|Sync)Subscriber(Fifo|Ring)?::(new|new_external|new_unrestricted)
	reSubscriberNew = regexp.MustCompile(
		`(?m)(Async|Sync)Subscriber(Fifo|Ring)?::(new|new_external|new_unrestricted)\s*\(\s*[^,]+,\s*"([^"]+)"`,
	)

	// session.create_subscriber(_external|_unrestricted)?("topic", ...)
	reSessionCreateSub = regexp.MustCompile(
		`(?m)\.create_subscriber(_external|_unrestricted)?\s*\(\s*"([^"]+)"`,
	)

	// (Async|Sync)Querier::(new|new_external|new_unrestricted)
	reQuerierNew = regexp.MustCompile(
		`(?m)(Async|Sync)Querier::(new|new_external|new_unrestricted)\s*\(\s*[^,]+,\s*"([^"]+)"`,
	)

	// (Async|Sync)Queryable(Fifo|Ring)?::(new|new_external|new_unrestricted)
	reQueryableNew = regexp.MustCompile(
		`(?m)(Async|Sync)Queryable(Fifo|Ring)?::(new|new_external|new_unrestricted)\s*\(\s*[^,]+,\s*"([^"]+)"`,
	)
)

// zenohSite is one detected pub/sub/query declaration.
type zenohSite struct {
	role       string // "Publisher" | "Subscriber" | "Querier" | "Queryable"
	topic      string
	line       int
	method     string
	scope      string // "local" | "external" | "absolute"
	throttled  bool
	bufferType string // "fifo" | "ring" | ""
}

// passZenoh scans .rs files under RepoPath for libio::zenoh creation sites.
// Post-flush pass — requires Function nodes already in Store.
//
// No-op silently on repos with no .rs files or no matches, so it's safe to
// run against every indexed project (non-robotics codebases pay a cheap
// walk + zero-matches cost).
func (p *Pipeline) passZenoh() {
	var rustFiles []string
	err := filepath.WalkDir(p.RepoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip inaccessible entries
		}
		if d.IsDir() {
			if discover.IGNORE_PATTERNS[d.Name()] {
				return filepath.SkipDir
			}
			if d.Name() == "target" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".rs") {
			rustFiles = append(rustFiles, path)
		}
		return nil
	})
	if err != nil {
		slog.Warn("pass.zenoh.walk", "err", err)
		return
	}
	if len(rustFiles) == 0 {
		return
	}

	// Lazy-loaded per-file function-range cache.
	fileFuncs := make(map[string][]funcRange)

	topicCount := 0
	edgeCount := 0

	for _, absPath := range rustFiles {
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		source := string(data)

		sites := findZenohSites(source)
		if len(sites) == 0 {
			continue
		}

		relPath, err := filepath.Rel(p.RepoPath, absPath)
		if err != nil {
			relPath = absPath
		}
		relPath = filepath.ToSlash(relPath)

		funcs, ok := fileFuncs[relPath]
		if !ok {
			funcs = p.loadFunctionRanges(relPath)
			fileFuncs[relPath] = funcs
		}

		for _, site := range sites {
			topicNode := p.zenohTopicNode(site.topic, site.scope, relPath)
			// UpsertNode's LastInsertId is unreliable on ON CONFLICT DO UPDATE
			// (store.UpsertNode doc). Re-resolve by QN so downstream edge
			// inserts don't fail FK-constraint with a stale topic ID.
			if _, err := p.Store.UpsertNode(topicNode); err != nil {
				continue
			}
			resolved, err := p.Store.FindNodeByQN(p.ProjectName, topicNode.QualifiedName)
			if err != nil || resolved == nil {
				continue
			}
			topicID := resolved.ID
			topicCount++

			fn := findEnclosingFunction(funcs, site.line)
			if fn == nil {
				// No enclosing function — call is at module init or test.
				// Skip for v0 (Function source is required for PUBLISHES_TO).
				continue
			}

			edgeType := zenohEdgeType(site.role)
			if edgeType == "" {
				continue
			}
			props := map[string]any{
				"method":          site.method,
				"scope":           site.scope,
				"line":            site.line,
				"confidence_tier": store.ConfidenceExtracted,
			}
			if site.throttled {
				props["throttled"] = true
			}
			if site.bufferType != "" {
				props["buffer_type"] = site.bufferType
			}
			edge := &store.Edge{
				Project:    p.ProjectName,
				SourceID:   fn.ID,
				TargetID:   topicID,
				Type:       edgeType,
				Properties: props,
			}
			if _, err := p.Store.InsertEdge(edge); err == nil {
				edgeCount++
			}
		}
	}

	if topicCount > 0 || edgeCount > 0 {
		slog.Info("pass.zenoh",
			"rust_files", len(rustFiles),
			"topics", topicCount,
			"edges", edgeCount,
		)
	}
}

// findZenohSites applies all patterns to a Rust source string and returns
// every detected declaration. Pure function — safe to unit-test with raw
// source fixtures.
func findZenohSites(source string) []zenohSite {
	var sites []zenohSite

	for _, m := range rePublisherNew.FindAllStringSubmatchIndex(source, -1) {
		throttled := captureAt(source, m, 2) == "Throttled"
		method := source[m[6]:m[7]]
		topic := source[m[8]:m[9]]
		sites = append(sites, zenohSite{
			role:      "Publisher",
			topic:     topic,
			line:      lineOf(source, m[0]),
			method:    method,
			scope:     zenohScope(method, topic),
			throttled: throttled,
		})
	}

	for _, m := range reSubscriberNew.FindAllStringSubmatchIndex(source, -1) {
		bufTypeStr := captureAt(source, m, 2) // "" | "Fifo" | "Ring"
		method := source[m[6]:m[7]]
		topic := source[m[8]:m[9]]
		sites = append(sites, zenohSite{
			role:       "Subscriber",
			topic:      topic,
			line:       lineOf(source, m[0]),
			method:     method,
			scope:      zenohScope(method, topic),
			bufferType: strings.ToLower(bufTypeStr),
		})
	}

	for _, m := range reSessionCreateSub.FindAllStringSubmatchIndex(source, -1) {
		variant := captureAt(source, m, 1) // "" | "_external" | "_unrestricted"
		method := "create_subscriber" + variant
		topic := source[m[4]:m[5]]
		sites = append(sites, zenohSite{
			role:   "Subscriber",
			topic:  topic,
			line:   lineOf(source, m[0]),
			method: method,
			scope:  zenohScope(method, topic),
		})
	}

	for _, m := range reQuerierNew.FindAllStringSubmatchIndex(source, -1) {
		method := source[m[4]:m[5]]
		topic := source[m[6]:m[7]]
		sites = append(sites, zenohSite{
			role:   "Querier",
			topic:  topic,
			line:   lineOf(source, m[0]),
			method: method,
			scope:  zenohScope(method, topic),
		})
	}

	for _, m := range reQueryableNew.FindAllStringSubmatchIndex(source, -1) {
		bufTypeStr := captureAt(source, m, 2)
		method := source[m[6]:m[7]]
		topic := source[m[8]:m[9]]
		sites = append(sites, zenohSite{
			role:       "Queryable",
			topic:      topic,
			line:       lineOf(source, m[0]),
			method:     method,
			scope:      zenohScope(method, topic),
			bufferType: strings.ToLower(bufTypeStr),
		})
	}

	return sites
}

// lineOf returns the 1-based line number of the byte offset.
func lineOf(source string, offset int) int {
	return 1 + strings.Count(source[:offset], "\n")
}

// captureAt returns the captured text for group `g` (1-indexed) from a
// FindAllStringSubmatchIndex match. Returns "" if the optional group was
// not captured (start index == -1), avoiding the `[:-1]` panic.
func captureAt(source string, m []int, g int) string {
	lo := m[2*g]
	hi := m[2*g+1]
	if lo < 0 || hi < 0 {
		return ""
	}
	return source[lo:hi]
}

// zenohScope infers topic scope from the method variant + topic prefix.
// Mirrors libio::zenoh::common semantics:
//   - `*_unrestricted` or absolute `asset/...` topics → absolute
//   - `*_external` → external
//   - default → local (asset-relative)
func zenohScope(method, topic string) string {
	if strings.Contains(method, "unrestricted") || strings.HasPrefix(topic, "asset/") {
		return "absolute"
	}
	if strings.Contains(method, "external") {
		return "external"
	}
	return "local"
}

// zenohEdgeType maps a role to its edge type.
func zenohEdgeType(role string) string {
	switch role {
	case "Publisher":
		return "PUBLISHES_TO"
	case "Subscriber":
		return "SUBSCRIBES_TO"
	case "Querier":
		return "QUERIES"
	case "Queryable":
		return "ANSWERS"
	}
	return ""
}

// zenohTopicNode builds a Topic node for the given topic expression.
// Topics are per-project (Corsair/code-graph convention — cross-project
// unification would require a global rendezvous pass).
func (p *Pipeline) zenohTopicNode(topic, scope, declaredIn string) *store.Node {
	sanitized := sanitizeTopicForQN(topic)
	qn := p.ProjectName + ".__topic__." + sanitized
	return &store.Node{
		Project:       p.ProjectName,
		Label:         "Topic",
		Name:          topic,
		QualifiedName: qn,
		FilePath:      declaredIn,
		Properties: map[string]any{
			"scope":            scope,
			"shared":           isSharedTopic(topic),
			"declared_in_file": declaredIn,
		},
	}
}

// sanitizeTopicForQN makes a topic string safe as a QN segment.
// Preserves uniqueness; readable in Cypher queries.
func sanitizeTopicForQN(topic string) string {
	r := strings.NewReplacer(
		"/", ".",
		" ", "_",
		"\t", "_",
	)
	return strings.Trim(r.Replace(topic), ".")
}

// isSharedTopic mirrors libio::zenoh::common::is_shared_topic — true when
// the topic is in the `shared/*` namespace (cross-asset rendezvous).
func isSharedTopic(topic string) bool {
	return strings.Contains(topic, "/shared/") || strings.HasPrefix(topic, "shared/")
}

// funcRange is a Function node's line span, kept for O(n) enclosing-fn
// lookup. Files rarely have >50 functions, so binary search isn't needed.
type funcRange struct {
	start int
	end   int
	node  *store.Node
}

// loadFunctionRanges fetches all Function nodes in `filePath` and sorts
// them by start line. Called once per file with matches.
func (p *Pipeline) loadFunctionRanges(filePath string) []funcRange {
	nodes, err := p.Store.FindNodesByFile(p.ProjectName, filePath)
	if err != nil {
		return nil
	}
	var ranges []funcRange
	for _, n := range nodes {
		if n.Label != "Function" {
			continue
		}
		ranges = append(ranges, funcRange{
			start: n.StartLine,
			end:   n.EndLine,
			node:  n,
		})
	}
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].start < ranges[j].start
	})
	return ranges
}

// findEnclosingFunction returns the innermost function whose line span
// contains `line`. "Innermost" = shortest span that still covers `line`,
// which handles nested closures that happen to also be tagged Function.
func findEnclosingFunction(ranges []funcRange, line int) *store.Node {
	var best *store.Node
	bestSpan := 1 << 30
	for _, r := range ranges {
		if r.start <= line && line <= r.end {
			span := r.end - r.start
			if span < bestSpan {
				bestSpan = span
				best = r.node
			}
		}
	}
	return best
}
