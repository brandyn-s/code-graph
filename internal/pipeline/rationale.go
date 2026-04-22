package pipeline

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// Rationale is the label used for nodes emitted by this pass.
const rationaleLabel = "Rationale"

// rationaleEdge is our outbound-from-rationale edge type. It points FROM
// the rationale node TO its enclosing Function/Method/Class. The backward
// lookup — "give me all rationale for function X" — uses the inverse
// via FindEdgesByTargetAndType on the subject node.
const rationaleEdge = "RATIONALE_FOR"

// rationaleMarker is a comment-line marker the pass recognizes. Each
// language branch below selects the line-prefix syntax (``#`` or ``//``)
// and applies a shared keyword alternation so the set of marker KINDS
// stays consistent across languages.
type rationaleMarker struct {
	kind string
	text string
}

// Match a comment line with one of our keywords. Capture group 1 is the
// kind, capture group 2 is the trailing prose (may be empty).
//
// Supported kinds, kept tight to avoid sweeping in every TODO comment
// ever written. The bar for inclusion is "this kind of comment contains
// *rationale* — a reason, a constraint, a warning — not just a task".
// TODO / FIXME are included because in practice they frequently carry a
// "why we can't do X yet" explanation that is rationale-shaped, and
// gating them out would force callers to re-implement the extraction.
var rationaleKinds = []string{
	"WHY",
	"NOTE",
	"IMPORTANT",
	"HACK",
	"SAFETY",
	"TODO",
	"FIXME",
	"XXX",
}

var (
	// # KIND: text or # KIND text   (Python, Bash, Ruby, YAML, HCL...)
	hashCommentRe = regexp.MustCompile(
		`^\s*#\s*(` + strings.Join(rationaleKinds, "|") + `)\b\s*:?\s*(.*)$`)

	// // KIND: text or // KIND text   (Go, Rust, TS, JS, C, C++, Java, Swift, C#...)
	slashCommentRe = regexp.MustCompile(
		`^\s*//\s*(` + strings.Join(rationaleKinds, "|") + `)\b\s*:?\s*(.*)$`)
)

// rationaleExtensions maps extension -> which regex to apply. Deliberately
// narrow: only languages where we've verified the comment syntax matches
// one of the two supported shapes. Languages with only block-comment
// syntax (e.g. raw HTML, CSS) are excluded here to avoid false positives.
var rationaleExtensions = map[string]*regexp.Regexp{
	// # KIND: ...
	".py":    hashCommentRe,
	".pyi":   hashCommentRe,
	".rb":    hashCommentRe,
	".sh":    hashCommentRe,
	".bash":  hashCommentRe,
	".zsh":   hashCommentRe,
	".yaml":  hashCommentRe,
	".yml":   hashCommentRe,
	".toml":  hashCommentRe,
	".hcl":   hashCommentRe,
	".tf":    hashCommentRe,
	".nix":   hashCommentRe,
	".ex":    hashCommentRe,
	".exs":   hashCommentRe,
	".r":     hashCommentRe,
	".pl":    hashCommentRe,
	".R":     hashCommentRe,

	// // KIND: ...
	".go":    slashCommentRe,
	".rs":    slashCommentRe,
	".c":     slashCommentRe,
	".h":     slashCommentRe,
	".cpp":   slashCommentRe,
	".cxx":   slashCommentRe,
	".cc":    slashCommentRe,
	".hpp":   slashCommentRe,
	".hxx":   slashCommentRe,
	".js":    slashCommentRe,
	".jsx":   slashCommentRe,
	".mjs":   slashCommentRe,
	".cjs":   slashCommentRe,
	".ts":    slashCommentRe,
	".tsx":   slashCommentRe,
	".java":  slashCommentRe,
	".kt":    slashCommentRe,
	".kts":   slashCommentRe,
	".scala": slashCommentRe,
	".swift": slashCommentRe,
	".cs":    slashCommentRe,
	".php":   slashCommentRe,
	".dart":  slashCommentRe,
	".zig":   slashCommentRe,
}

// Cap on how much text we keep per rationale. Callers surface this in
// tool output (find_rationale) and inside the orientation report's
// "notable rationale" section, where unbounded prose would corrupt the
// markdown table. Also bounds the storage cost of a misbehaving scan
// against a generated file full of "// TODO" lines.
const rationaleMaxTextLen = 200

// Hard cap on rationale nodes emitted per file. Guards against pathological
// cases (auto-generated tables with thousands of "// TODO" markers, etc.).
// Per-file observed count on redacted repos: median 2, p95 ~15.
const rationalePerFileCap = 50

// passRationale extracts reason-carrying comments (WHY / NOTE / HACK / ...)
// from source files and creates Rationale nodes + RATIONALE_FOR edges to
// the enclosing Function/Method/Class. Answers "why was this written the
// way it was?" without requiring the caller to grep across files.
//
// Scope: regex-based line matching over source text, no AST changes.
// Tradeoff: simple and language-extensible via a map; misses comments
// inside /* */ block syntax in C-family languages and triple-quoted
// docstrings in Python. Those formats carry a different kind of content
// (reference-style documentation vs. annotation-style rationale) and are
// a separate follow-up pass if the need arises.
//
// Order: runs post-flush so Function/Method/Class nodes exist in the DB
// for the enclosing-symbol lookup. Zero interaction with the embeddings
// or similarity passes. Reads files freshly from disk (not through the
// extraction cache) — rationale extraction is purely textual and the
// small re-read cost is negligible compared to already-finished heavier
// passes.
func (p *Pipeline) passRationale() {
	// Source files: walk the File nodes already in the DB — the pass runs
	// post-flush, after cleanupASTCache, so the extraction cache is gone.
	// File nodes give us relative paths, which is all this pass needs
	// (scan source text + look up enclosing symbols by line range).
	fileNodes, err := p.Store.FindNodesByLabel(p.ProjectName, "File")
	if err != nil || len(fileNodes) == 0 {
		slog.Info("pass.rationale.skip", "reason", "no_file_nodes")
		return
	}

	var (
		filesScanned       int
		rationaleNodeCount int
		rationaleEdgeCount int
		byKind             = map[string]int{}
	)

	type pendingRationale struct {
		node *store.Node
		// Subject = enclosing function/method/class node; resolved after
		// the rationale node is upserted (so we have its concrete ID).
		subjectID int64
	}

	for _, fileNode := range fileNodes {
		relPath := fileNode.FilePath
		if relPath == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(relPath))
		re, ok := rationaleExtensions[ext]
		if !ok {
			continue
		}

		absPath := relPath
		if p.RepoPath != "" {
			absPath = filepath.Join(p.RepoPath, relPath)
		}
		f, err := os.Open(absPath)
		if err != nil {
			slog.Debug("pass.rationale.open.err", "path", relPath, "err", err)
			continue
		}
		filesScanned++

		pending := make([]pendingRationale, 0, 4)

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024) // tolerate long lines
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			m := re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			kind := strings.ToUpper(m[1])
			text := strings.TrimSpace(m[2])
			if len(text) > rationaleMaxTextLen {
				text = text[:rationaleMaxTextLen] + "..."
			}

			// Subject = smallest-range Function/Method/Class containing the
			// comment line. Nil is acceptable — we still emit the rationale,
			// but it won't be linked to a subject (orphaned, but the find_
			// rationale tool can still surface it by kind).
			var subjectID int64
			subjects, err := p.Store.FindNodesByFileOverlap(p.ProjectName, relPath, lineNum, lineNum)
			if err == nil && len(subjects) > 0 {
				subjectID = smallestRangeNode(subjects).ID
			}

			nameHint := kind
			if text != "" {
				nameHint = kind + ": " + trunc(text, 40)
			}

			qn := fmt.Sprintf("%s.__rationale__.%s:%d", p.ProjectName, relPath, lineNum)

			node := &store.Node{
				Project:       p.ProjectName,
				Label:         rationaleLabel,
				Name:          nameHint,
				QualifiedName: qn,
				FilePath:      relPath,
				StartLine:     lineNum,
				EndLine:       lineNum,
				Properties: map[string]any{
					"kind":            kind,
					"text":            text,
					"confidence_tier": store.ConfidenceInferred,
				},
			}
			pending = append(pending, pendingRationale{node: node, subjectID: subjectID})
			byKind[kind]++

			if len(pending) >= rationalePerFileCap {
				break
			}
		}
		_ = f.Close()
		if err := scanner.Err(); err != nil {
			slog.Debug("pass.rationale.scan.err", "path", relPath, "err", err)
		}

		if len(pending) == 0 {
			continue
		}

		// Upsert rationale nodes first so we have IDs for the edges.
		nodes := make([]*store.Node, len(pending))
		for i, pr := range pending {
			nodes[i] = pr.node
		}
		qnToID, err := p.Store.UpsertNodeBatch(nodes)
		if err != nil {
			slog.Warn("pass.rationale.upsert.err", "path", relPath, "err", err)
			continue
		}
		rationaleNodeCount += len(nodes)

		// Emit edges for rationales that resolved to a subject.
		edges := make([]*store.Edge, 0, len(pending))
		for _, pr := range pending {
			if pr.subjectID == 0 {
				continue
			}
			id, ok := qnToID[pr.node.QualifiedName]
			if !ok {
				continue
			}
			edges = append(edges, &store.Edge{
				Project:  p.ProjectName,
				SourceID: id,
				TargetID: pr.subjectID,
				Type:     rationaleEdge,
				Properties: map[string]any{
					"confidence_tier": store.ConfidenceInferred,
				},
			})
		}
		if len(edges) > 0 {
			if err := p.Store.InsertEdgeBatch(edges); err != nil {
				slog.Warn("pass.rationale.edges.err", "path", relPath, "err", err)
			} else {
				rationaleEdgeCount += len(edges)
			}
		}
	}

	slog.Info("pass.rationale.done",
		"files_scanned", filesScanned,
		"rationale_nodes", rationaleNodeCount,
		"rationale_for_edges", rationaleEdgeCount,
		"by_kind", byKind)
}

// smallestRangeNode returns the node with the smallest (end-start) range.
// Used to pick the most-specific enclosing symbol when a file has nested
// scopes (a method inside a class inside a module).
func smallestRangeNode(nodes []*store.Node) *store.Node {
	if len(nodes) == 0 {
		return nil
	}
	sort.Slice(nodes, func(i, j int) bool {
		a := nodes[i].EndLine - nodes[i].StartLine
		b := nodes[j].EndLine - nodes[j].StartLine
		return a < b
	})
	return nodes[0]
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
