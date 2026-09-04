package pipeline

// SCIP ingest pass — opt-in precision tier (set CBM_SCIP_INDEX_PATH).
//
// When a SCIP index produced by a compiler-grade indexer (scip-go,
// rust-analyzer scip, scip-typescript, scip-python, ...) is available for
// the repo, this pass REPLACES the heuristic resolver's CALLS edges for
// every file the index covers with edges derived from the index's
// occurrences, keeping tree-sitter resolution as the fallback for files
// the index doesn't cover — the same precise-over-heuristic layering
// Sourcegraph and Glean use.
//
// Derivation: a call edge is a non-definition occurrence of a
// function/method symbol whose textual call site is followed by `(`,
// attributed to the enclosing Function/Method node by span containment;
// the callee is the node whose span starts at (or contains) the symbol's
// definition site. Both endpoints therefore live in the existing node
// vocabulary — no new node kinds are introduced.
//
// Measured on code-graph itself (scip-go, 2026-06-10): agreement with the
// heuristic resolver on 4,593 edges; 969 heuristic-only edges (~58% of
// them generic-method-name fuzzy resolutions like `.Error()`/`.Parse()`)
// and 830 SCIP-only edges (81% method calls on typed receivers the
// heuristic could not resolve).
//
// Inert by default: the pass is a no-op unless CBM_SCIP_INDEX_PATH names a
// readable SCIP index file.

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brandyn-s/code-graph/internal/config"
	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// scipIndexPathEnv names the SCIP index file to ingest. It is a SINGLE global
// path, so it selects an index for whatever repo is currently being indexed —
// which makes it unusable as a persistent setting across more than one repo
// (pointing it at repo A's index while indexing repo B is a no-op at best: the
// drift guard excludes every document because no definition site matches).
//
// scipIndexDefaultName is the per-repo convention that makes the precision tier
// persistent: with discovery enabled, an index at <repo>/index.scip is used
// automatically, so the gain survives a re-index instead of depending on an env
// var being set in the invoking shell. That name matches what the validation
// runbook and CLAUDE.md have used since the tier shipped
// (`rust-analyzer scip . --output index.scip`).
//
// scipAutoDiscoverEnv gates that discovery, and it is deliberately NOT
// on-by-default. Treating "a file named index.scip exists" as consent would mean
// a stale index left in a repo silently starts rewriting CALLS edges on the next
// re-index with no operator action — the same shape of silent behavior change
// this tier's drift guard exists to prevent. TestSCIPIngestInertWithoutEnv
// encodes the existing contract (env var is the only switch) and still passes:
// discovery is opt-in convenience, not an implicit trigger.
//
// Precedence: explicit env path > discovered in-repo index (when discovery is
// enabled) > off.
const (
	scipIndexPathEnv     = "CBM_SCIP_INDEX_PATH"
	scipAutoDiscoverEnv  = "CBM_SCIP_AUTO_DISCOVER"
	scipIndexDefaultName = "index.scip"
)

// SCIPIngestStatus is the machine-readable outcome of the optional SCIP
// precision pass. Coverage is function-level over the graph's existing
// Function/Method nodes, not a claim that every language construct is covered.
type SCIPIngestStatus struct {
	State                  string  `json:"state"`
	Source                 string  `json:"source,omitempty"`
	Documents              int     `json:"documents"`
	DriftedDocuments       int     `json:"drifted_documents"`
	ProjectFunctions       int     `json:"project_functions"`
	CoveredFunctions       int     `json:"covered_functions"`
	CoveragePercent        float64 `json:"coverage_percent"`
	HeuristicEdgesReplaced int     `json:"heuristic_edges_replaced"`
	SCIPCallsInserted      int     `json:"scip_calls_inserted"`
	IndexSHA256            string  `json:"index_sha256,omitempty"`
	Error                  string  `json:"error,omitempty"`
}

// scipAutoDiscoverEnabled reports whether in-repo index discovery is on.
// Any non-empty value other than an explicit falsey word enables it, matching
// how the other CBM_* gates in this package read their env vars.
func scipAutoDiscoverEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(config.Get(config.SCIPAutoDiscover))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// scipIndexPath resolves which SCIP index to ingest for repoRoot, and reports
// how it was found. The provenance string is logged so an auto-discovered index
// is distinguishable from an explicitly-configured one after the fact.
func scipIndexPath(repoRoot string) (path, source string) {
	if env := config.Get(config.SCIPIndexPath); env != "" {
		return env, "env:" + scipIndexPathEnv
	}
	if repoRoot == "" || !scipAutoDiscoverEnabled() {
		return "", ""
	}
	candidate := filepath.Join(repoRoot, scipIndexDefaultName)
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return "", ""
	}
	return candidate, "repo-default:" + scipIndexDefaultName
}

// scipFuncSpan is a Function/Method node's location, the shared vocabulary
// between the SCIP occurrences and the existing graph.
type scipFuncSpan struct {
	id         int64
	qn         string
	start, end int // 1-based, inclusive (tree-sitter node lines)
}

type scipFileSpans map[string][]scipFuncSpan // file_path -> spans sorted by start

type scipDefinitionLocation struct {
	file string
	line int
}

type scipEdgePair struct {
	source int64
	target int64
}

type scipSourceCache struct {
	repoRoot string
	lines    map[string][]string
}

func isCallSuffix(suffix string) bool {
	suffix = strings.TrimLeft(suffix, " \t")
	if strings.HasPrefix(suffix, "(") {
		return true
	}
	if !strings.HasPrefix(suffix, "<") {
		return false
	}
	depth := 0
	for i, char := range suffix {
		switch char {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return strings.HasPrefix(strings.TrimLeft(suffix[i+1:], " \t"), "(")
			}
			if depth < 0 {
				return false
			}
		}
	}
	return false
}

// innermost returns the smallest span containing line, or nil.
func (fs scipFileSpans) innermost(file string, line int) *scipFuncSpan {
	var best *scipFuncSpan
	bestSize := 1 << 30
	spans := fs[file]
	for i := range spans {
		s := &spans[i]
		if s.start <= line && line <= s.end {
			if sz := s.end - s.start; sz < bestSize {
				best, bestSize = s, sz
			}
		}
	}
	return best
}

// atLine returns a span starting exactly at line (a definition site), or nil.
func (fs scipFileSpans) atLine(file string, line int) *scipFuncSpan {
	spans := fs[file]
	for i := range spans {
		if spans[i].start == line {
			return &spans[i]
		}
	}
	return nil
}

func isSCIPFunctionSymbol(symbol string) bool {
	return strings.HasSuffix(symbol, ").") && !strings.HasPrefix(symbol, "local ")
}

// isSCIPFunctionDefinition accepts the explicit function-symbol shape used by
// Go and method definitions, plus a TypeScript-specific projection for
// top-level variable-assigned functions. scip-typescript encodes the latter as
// value symbols ending in "."; their multi-line enclosing range lets us prove
// that the definition covers an existing Function node. Requiring the graph
// span to have the exact same bounds avoids promoting ordinary local variables
// merely because they sit inside a function.
func isSCIPFunctionDefinition(
	symbol string,
	file string,
	occurrence *scip.Occurrence,
	spans scipFileSpans,
) bool {
	if isSCIPFunctionSymbol(symbol) {
		return true
	}
	if !strings.HasPrefix(symbol, "scip-typescript ") || !strings.HasSuffix(symbol, ".") {
		return false
	}
	// EnclosingSourceRange reads the typed enclosing range first and falls
	// back to the legacy enclosing_range field that older SCIP indexers emit.
	enclosing, ok := occurrence.EnclosingSourceRange()
	if !ok {
		return false
	}
	start := int(enclosing.Start.Line) + 1
	end := int(enclosing.End.Line) + 1
	for _, span := range spans[file] {
		if span.start == start && span.end == end {
			return true
		}
	}
	return false
}

func collectSCIPDefinitions(idx *scip.Index, spans scipFileSpans) (
	definitions map[string]scipDefinitionLocation,
	definitionTotals map[string]int,
	definitionMatches map[string]int,
) {
	definitions = make(map[string]scipDefinitionLocation)
	definitionTotals = make(map[string]int)
	definitionMatches = make(map[string]int)
	for _, document := range idx.Documents {
		for _, occurrence := range document.Occurrences {
			if occurrence.SymbolRoles&int32(scip.SymbolRole_Definition) == 0 ||
				!isSCIPFunctionDefinition(occurrence.Symbol, document.RelativePath, occurrence, spans) {
				continue
			}
			symbolRange, ok := occurrence.SourceRange()
			if !ok {
				continue
			}
			line := int(symbolRange.Start.Line) + 1
			// SCIP definitions point at the symbol token, while tree-sitter
			// nodes start at the whole declaration. Canonicalize a definition
			// inside an existing function span to that node's start so both
			// coordinate systems address the same graph endpoint. This matters
			// for variable-assigned functions and decorated/multiline
			// declarations whose symbol is not on the declaration's first line.
			match := spans.atLine(document.RelativePath, line)
			if match == nil {
				match = spans.innermost(document.RelativePath, line)
			}
			if match != nil {
				line = match.start
			}
			definitions[occurrence.Symbol] = scipDefinitionLocation{file: document.RelativePath, line: line}
			definitionTotals[document.RelativePath]++
			if match != nil {
				definitionMatches[document.RelativePath]++
			}
		}
	}
	return definitions, definitionTotals, definitionMatches
}

func driftedSCIPDocuments(definitionTotals, definitionMatches map[string]int) map[string]bool {
	drifted := make(map[string]bool)
	for file, total := range definitionTotals {
		if total > 0 && definitionMatches[file]*2 < total {
			drifted[file] = true
		}
	}
	return drifted
}

func coveredSCIPFiles(idx *scip.Index, drifted map[string]bool) []string {
	coveredSet := make(map[string]struct{}, len(idx.Documents))
	for _, document := range idx.Documents {
		if !drifted[document.RelativePath] {
			coveredSet[document.RelativePath] = struct{}{}
		}
	}
	covered := make([]string, 0, len(coveredSet))
	for file := range coveredSet {
		covered = append(covered, file)
	}
	sort.Strings(covered)
	return covered
}

func (cache *scipSourceCache) line(file string, number int) string {
	lines, ok := cache.lines[file]
	if !ok {
		contents, err := os.ReadFile(filepath.Join(cache.repoRoot, file))
		if err != nil {
			cache.lines[file] = []string{}
			return ""
		}
		lines = strings.Split(string(contents), "\n")
		cache.lines[file] = lines
	}
	if number < 0 || number >= len(lines) {
		return ""
	}
	return lines[number]
}

func (p *Pipeline) deriveSCIPCalls(
	idx *scip.Index,
	spans scipFileSpans,
	definitions map[string]scipDefinitionLocation,
	drifted map[string]bool,
	indexSHA256 string,
) (derived []*store.Edge, referencesSeen, callShaped int) {
	cache := scipSourceCache{repoRoot: p.RepoPath, lines: make(map[string][]string)}
	seen := make(map[scipEdgePair]bool)
	derived = make([]*store.Edge, 0)
	for _, document := range idx.Documents {
		if drifted[document.RelativePath] {
			continue
		}
		for _, occurrence := range document.Occurrences {
			if occurrence.SymbolRoles&int32(scip.SymbolRole_Definition) != 0 {
				continue
			}
			definition, ok := definitions[occurrence.Symbol]
			if !ok || drifted[definition.file] {
				continue
			}
			referencesSeen++
			symbolRange, ok := occurrence.SourceRange()
			if !ok {
				continue
			}
			line := cache.line(document.RelativePath, int(symbolRange.End.Line))
			if int(symbolRange.End.Character) > len(line) {
				continue
			}
			if !isCallSuffix(line[symbolRange.End.Character:]) {
				continue
			}
			callShaped++
			caller := spans.innermost(document.RelativePath, int(symbolRange.Start.Line)+1)
			if caller == nil {
				continue
			}
			callee := spans.atLine(definition.file, definition.line)
			if callee == nil {
				callee = spans.innermost(definition.file, definition.line)
			}
			if callee == nil {
				continue
			}
			pair := scipEdgePair{source: caller.id, target: callee.id}
			if seen[pair] {
				continue
			}
			seen[pair] = true
			derived = append(derived, &store.Edge{
				Project: p.ProjectName, SourceID: caller.id, TargetID: callee.id, Type: store.EdgeCalls,
				Properties: map[string]any{
					"resolver_rule":              "scip-ingest",
					"resolution_artifact_sha256": indexSHA256,
				},
			})
		}
	}
	return derived, referencesSeen, callShaped
}

func (p *Pipeline) passSCIPIngest() {
	path, source := p.scipPath, p.scipSource
	if !p.scipConfigured {
		path, source = scipIndexPath(p.RepoPath)
	}
	if path == "" {
		p.SCIPStatus = SCIPIngestStatus{State: "disabled", Source: source}
		return
	}
	// Log the source so an auto-discovered index is distinguishable from an
	// explicitly-configured one when reading pass.scip_ingest.done later.
	slog.Info("pass.scip_ingest.selected", "path", path, "source", source)
	if err := p.runSCIPIngest(path); err != nil {
		// Ingest failure must not fail indexing — the heuristic edges are
		// still in place and correct-by-default.
		slog.Warn("pass.scip_ingest.err", "path", path, "source", source, "err", err)
	}
	p.SCIPStatus.Source = source
}

func (p *Pipeline) runSCIPIngest(path string) (retErr error) {
	p.SCIPStatus = SCIPIngestStatus{State: "running"}
	defer func() {
		if retErr != nil {
			p.SCIPStatus.State = "failed"
			p.SCIPStatus.Error = retErr.Error()
		}
	}()
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read index: %w", err)
	}
	p.SCIPStatus.IndexSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	var idx scip.Index
	if err := proto.Unmarshal(raw, &idx); err != nil {
		return fmt.Errorf("unmarshal index: %w", err)
	}

	spans, err := p.loadFunctionSpans()
	if err != nil {
		return fmt.Errorf("load function spans: %w", err)
	}
	for _, fileSpans := range spans {
		p.SCIPStatus.ProjectFunctions += len(fileSpans)
	}

	// Pass 1: symbol -> definition site. SCIP lines are 0-based; node
	// spans are 1-based tree-sitter lines, hence the +1 below.
	definitions, definitionTotals, definitionMatches := collectSCIPDefinitions(&idx, spans)

	// Stale-index guard: if a document's definition sites no longer line up
	// with the freshly-indexed node spans, the file changed after the SCIP
	// index was generated. Replacing its edges would delete heuristic truth
	// and re-derive from drifted positions — ground-truth eval measured
	// recall 0.955 -> 0.847 with a 3-commit-old index. Drifted files are
	// excluded from BOTH deletion and derivation (their heuristic edges
	// stay authoritative); regenerate the index to cover them.
	drifted := driftedSCIPDocuments(definitionTotals, definitionMatches)
	if len(drifted) > 0 {
		slog.Warn("pass.scip_ingest.drifted_files",
			"count", len(drifted),
			"hint", "SCIP index predates current file contents; regenerate it at the indexed commit")
	}
	p.SCIPStatus.Documents = len(idx.Documents)
	p.SCIPStatus.DriftedDocuments = len(drifted)
	covered := coveredSCIPFiles(&idx, drifted)
	for _, file := range covered {
		p.SCIPStatus.CoveredFunctions += len(spans[file])
	}
	if p.SCIPStatus.ProjectFunctions > 0 {
		p.SCIPStatus.CoveragePercent = 100 * float64(p.SCIPStatus.CoveredFunctions) / float64(p.SCIPStatus.ProjectFunctions)
	}

	// Pass 2: derive call edges from call-shaped reference occurrences.
	derived, refsSeen, callShaped := p.deriveSCIPCalls(
		&idx,
		spans,
		definitions,
		drifted,
		p.SCIPStatus.IndexSHA256,
	)

	// Replace heuristic CALLS edges only where the index can re-derive
	// them: BOTH endpoints must live in covered, non-drifted files. Edges
	// into files the indexer cannot see (CGO sources, platform-gated
	// files) and edges touching drifted files keep their heuristic
	// fallback.
	deleted, err := p.Store.DeleteEdgesBetweenFiles(p.ProjectName, "CALLS", covered, covered)
	if err != nil {
		return fmt.Errorf("replace heuristic edges: %w", err)
	}
	if err := p.Store.InsertEdgeBatch(derived); err != nil {
		return fmt.Errorf("insert derived edges: %w", err)
	}
	p.SCIPStatus.State = "applied"
	p.SCIPStatus.HeuristicEdgesReplaced = int(deleted)
	p.SCIPStatus.SCIPCallsInserted = len(derived)

	slog.Info("pass.scip_ingest.done",
		"index", path,
		"documents", len(idx.Documents),
		"drifted_documents", len(drifted),
		"refs_in_corpus", refsSeen,
		"call_shaped", callShaped,
		"heuristic_edges_replaced", deleted,
		"scip_edges_inserted", len(derived))
	return nil
}

// loadFunctionSpans reads Function/Method node spans for the project into
// the per-file lookup structure used to attribute occurrences to nodes.
func (p *Pipeline) loadFunctionSpans() (scipFileSpans, error) {
	rows, err := p.Store.Q().Query(`
		SELECT id, qualified_name, file_path, start_line, end_line
		FROM nodes WHERE project = ? AND label IN ('Function', 'Method')`,
		p.ProjectName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fs := scipFileSpans{}
	for rows.Next() {
		var s scipFuncSpan
		var file string
		if err := rows.Scan(&s.id, &s.qn, &file, &s.start, &s.end); err != nil {
			return nil, err
		}
		fs[file] = append(fs[file], s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for k := range fs {
		sort.Slice(fs[k], func(i, j int) bool { return fs[k][i].start < fs[k][j].start })
	}
	return fs, nil
}
