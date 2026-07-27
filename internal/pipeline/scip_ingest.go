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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
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

// scipAutoDiscoverEnabled reports whether in-repo index discovery is on.
// Any non-empty value other than an explicit falsey word enables it, matching
// how the other CBM_* gates in this package read their env vars.
func scipAutoDiscoverEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(scipAutoDiscoverEnv))) {
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
	if env := os.Getenv(scipIndexPathEnv); env != "" {
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

func (p *Pipeline) passSCIPIngest() {
	path, source := scipIndexPath(p.RepoPath)
	if path == "" {
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
}

//nolint:gocognit // linear two-pass derivation over the index
func (p *Pipeline) runSCIPIngest(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read index: %w", err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(raw, &idx); err != nil {
		return fmt.Errorf("unmarshal index: %w", err)
	}

	spans, err := p.loadFunctionSpans()
	if err != nil {
		return fmt.Errorf("load function spans: %w", err)
	}

	// SCIP symbols for functions/methods end with a `().` descriptor;
	// `local N` symbols are file-local (closures, parameters).
	isFuncSym := func(sym string) bool {
		return strings.HasSuffix(sym, ").") && !strings.HasPrefix(sym, "local ")
	}

	// Pass 1: symbol -> definition site. SCIP lines are 0-based; node
	// spans are 1-based tree-sitter lines, hence the +1 below.
	type defLoc struct {
		file string
		line int
	}
	defs := make(map[string]defLoc)
	defTotal := make(map[string]int)   // file -> function-def occurrences
	defMatched := make(map[string]int) // file -> defs landing on a node span start
	for _, doc := range idx.Documents {
		for _, occ := range doc.Occurrences {
			if occ.SymbolRoles&int32(scip.SymbolRole_Definition) == 0 || !isFuncSym(occ.Symbol) {
				continue
			}
			r, rErr := scip.NewRange(occ.Range)
			if rErr != nil {
				continue
			}
			defs[occ.Symbol] = defLoc{file: doc.RelativePath, line: int(r.Start.Line) + 1}
			defTotal[doc.RelativePath]++
			if spans.atLine(doc.RelativePath, int(r.Start.Line)+1) != nil {
				defMatched[doc.RelativePath]++
			}
		}
	}

	// Stale-index guard: if a document's definition sites no longer line up
	// with the freshly-indexed node spans, the file changed after the SCIP
	// index was generated. Replacing its edges would delete heuristic truth
	// and re-derive from drifted positions — ground-truth eval measured
	// recall 0.955 -> 0.847 with a 3-commit-old index. Drifted files are
	// excluded from BOTH deletion and derivation (their heuristic edges
	// stay authoritative); regenerate the index to cover them.
	drifted := make(map[string]bool)
	for file, total := range defTotal {
		if total > 0 && defMatched[file]*2 < total {
			drifted[file] = true
		}
	}
	if len(drifted) > 0 {
		slog.Warn("pass.scip_ingest.drifted_files",
			"count", len(drifted),
			"hint", "SCIP index predates current file contents; regenerate it at the indexed commit")
	}

	// Pass 2: derive call edges from call-shaped reference occurrences.
	srcCache := make(map[string][]string)
	lineOf := func(file string, n int) string {
		lines, ok := srcCache[file]
		if !ok {
			b, rErr := os.ReadFile(filepath.Join(p.RepoPath, file))
			if rErr != nil {
				srcCache[file] = []string{}
				return ""
			}
			lines = strings.Split(string(b), "\n")
			srcCache[file] = lines
		}
		if n < 0 || n >= len(lines) {
			return ""
		}
		return lines[n]
	}

	type pair struct{ src, tgt int64 }
	seen := make(map[pair]bool)
	var derived []*store.Edge
	refsSeen, callShaped := 0, 0
	for _, doc := range idx.Documents {
		if drifted[doc.RelativePath] {
			continue
		}
		for _, occ := range doc.Occurrences {
			if occ.SymbolRoles&int32(scip.SymbolRole_Definition) != 0 || !isFuncSym(occ.Symbol) {
				continue
			}
			d, ok := defs[occ.Symbol]
			if !ok {
				continue // external symbol (stdlib / dependency) — out of corpus
			}
			if drifted[d.file] {
				continue // callee position unreliable in a drifted file
			}
			refsSeen++
			r, rErr := scip.NewRange(occ.Range)
			if rErr != nil {
				continue
			}
			// Call-shaped: the next non-space char after the symbol is `(`.
			// Filters out function values, method expressions, and doc refs.
			line := lineOf(doc.RelativePath, int(r.End.Line))
			if int(r.End.Character) > len(line) {
				continue
			}
			rest := strings.TrimLeft(line[r.End.Character:], " \t")
			if !strings.HasPrefix(rest, "(") {
				continue
			}
			callShaped++

			caller := spans.innermost(doc.RelativePath, int(r.Start.Line)+1)
			if caller == nil {
				continue // call at file/package scope (init expressions etc.)
			}
			callee := spans.atLine(d.file, d.line)
			if callee == nil {
				callee = spans.innermost(d.file, d.line)
			}
			if callee == nil {
				continue
			}
			pr := pair{src: caller.id, tgt: callee.id}
			if seen[pr] {
				continue
			}
			seen[pr] = true
			derived = append(derived, &store.Edge{
				Project:  p.ProjectName,
				SourceID: caller.id,
				TargetID: callee.id,
				Type:     "CALLS",
				Properties: map[string]any{
					"resolver_rule": "scip-ingest",
				},
			})
		}
	}

	// Replace heuristic CALLS edges only where the index can re-derive
	// them: BOTH endpoints must live in covered, non-drifted files. Edges
	// into files the indexer cannot see (CGO sources, platform-gated
	// files) and edges touching drifted files keep their heuristic
	// fallback.
	covered := make([]string, 0, len(idx.Documents))
	for _, doc := range idx.Documents {
		if !drifted[doc.RelativePath] {
			covered = append(covered, doc.RelativePath)
		}
	}
	sort.Strings(covered)
	deleted, err := p.Store.DeleteEdgesBetweenFiles(p.ProjectName, "CALLS", covered, covered)
	if err != nil {
		return fmt.Errorf("replace heuristic edges: %w", err)
	}
	if err := p.Store.InsertEdgeBatch(derived); err != nil {
		return fmt.Errorf("insert derived edges: %w", err)
	}

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
