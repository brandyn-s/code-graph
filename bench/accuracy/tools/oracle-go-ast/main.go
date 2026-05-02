// CALLS + IMPORTS ground-truth oracle for Go fixtures via go/ast.
//
// Run: oracle-go-ast <subset_root> <project_name>
//
// Emits JSON {"edges":[...], "defs":[...]} to stdout. Same shape as
// the Rust oracle (bench/accuracy/tools/oracle-rust-syn/).
//
// Design decisions:
//   - go/parser + go/ast (Go's standard library) parses unexpanded source.
//     Matches code-graph's tree-sitter granularity. Apples-to-apples.
//   - QN format matches code-graph's Go storage form: `<project>.<file_no_ext>.<name>`
//     for functions AND methods. Code-graph DOES NOT include receiver type
//     in method QNs for concrete types (verified empirically on the indexed
//     c-Users-...internal-store graph). Ambiguity (multiple methods named
//     `Close` across different receivers) is resolved by filename, so methods
//     on different types in different files don't collide.
//   - Caller-side: callee paths are reported as written (bare ident, or
//     one-segment selector). The Python wrapper resolves bare names against
//     the def list by last-segment match, same pattern as the Rust oracle.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Edge struct {
	FromQN   string `json:"from_qn"`
	ToQN     string `json:"to_qn"`
	EdgeType string `json:"type"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Source   string `json:"source"`
}

type Output struct {
	Edges []Edge   `json:"edges"`
	Defs  []string `json:"defs"`
}

type visitor struct {
	fset          *token.FileSet
	project       string
	fileQN        string // <project>.<file_no_ext>
	fileRel       string // for edge.File
	fnStack       []string
	recvNameStack []string // parallel to fnStack: receiver var name (e.g. "p" in `func (p *Pipeline) ...`), "" for free funcs
	recvTypeStack []string // parallel to fnStack: receiver type name (e.g. "Pipeline"), "" for free funcs
	edges         []Edge
	defs          []string
}

func (v *visitor) currentCaller() string {
	if len(v.fnStack) == 0 {
		return v.fileQN
	}
	return v.fileQN + "." + strings.Join(v.fnStack, ".")
}

// Visit implements ast.Visitor. We use a single-method dispatch.
func (v *visitor) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.FuncDecl:
		// Free fns: QN = <file>.<name>
		// Methods: QN = <file>.<ReceiverType>.<name>
		// Matches code-graph's extractor behavior as of 2026-04-24 (prior to
		// that, code-graph dropped the receiver segment; the oracle did too,
		// which matched by accident). The fix makes both sides agree on
		// the receiver-qualified form.
		seg := n.Name.Name
		recvName := ""
		recvType := ""
		if n.Recv != nil && len(n.Recv.List) > 0 {
			recvType = receiverTypeName(n.Recv.List[0].Type)
			if recvType != "" {
				seg = recvType + "." + n.Name.Name
				// Capture the receiver identifier (e.g., "p" in
				// `func (p *Pipeline) ...`). Empty when the receiver is
				// anonymous (`func (*Pipeline) ...`). Used by the
				// CallExpr branch below to resolve self-receiver calls.
				if len(n.Recv.List[0].Names) > 0 {
					recvName = n.Recv.List[0].Names[0].Name
				}
			}
		}
		v.fnStack = append(v.fnStack, seg)
		v.recvNameStack = append(v.recvNameStack, recvName)
		v.recvTypeStack = append(v.recvTypeStack, recvType)
		v.defs = append(v.defs, v.currentCaller())
		if n.Body != nil {
			ast.Walk(v, n.Body)
		}
		v.fnStack = v.fnStack[:len(v.fnStack)-1]
		v.recvNameStack = v.recvNameStack[:len(v.recvNameStack)-1]
		v.recvTypeStack = v.recvTypeStack[:len(v.recvTypeStack)-1]
		return nil

	case *ast.CallExpr:
		callee := extractCallee(n.Fun)
		// Follow-up #5 (2026-05-02 plateau plan): resolve self-receiver
		// method calls. When inside `func (p *Pipeline) X() { ... p.Y() ... }`,
		// extractCallee returned "p.Y". The Python wrapper has no resolution
		// path for "p.Y" (it expects either a bare name or "<file>.<fn>"),
		// so it dropped the edge as `calls_path_dropped`. By substituting
		// the recv identifier with the recv type name, we emit "Pipeline.Y"
		// which the wrapper can resolve to the matching `*.Pipeline.Y` def.
		// This eliminates ~30-100+ fake FPs per the runIncrementalPasses
		// investigation (PR #137).
		if callee != "" && len(v.recvNameStack) > 0 {
			rn := v.recvNameStack[len(v.recvNameStack)-1]
			rt := v.recvTypeStack[len(v.recvTypeStack)-1]
			if rn != "" && rt != "" {
				prefix := rn + "."
				if strings.HasPrefix(callee, prefix) && !strings.Contains(callee[len(prefix):], ".") {
					callee = rt + "." + callee[len(prefix):]
				}
			}
		}
		if callee != "" {
			line := 0
			if v.fset != nil {
				pos := v.fset.Position(n.Pos())
				if pos.IsValid() {
					line = pos.Line
				}
			}
			v.edges = append(v.edges, Edge{
				FromQN:   v.currentCaller(),
				ToQN:     callee,
				EdgeType: "CALLS",
				File:     v.fileRel,
				Line:     line,
				Source:   "go-ast",
			})
		}
		// Continue descending — calls can nest inside args, etc.
		return v

	case *ast.ImportSpec:
		path := ""
		if n.Path != nil {
			path = strings.Trim(n.Path.Value, `"`)
		}
		if path != "" {
			line := 0
			if v.fset != nil {
				pos := v.fset.Position(n.Pos())
				if pos.IsValid() {
					line = pos.Line
				}
			}
			v.edges = append(v.edges, Edge{
				FromQN:   v.fileQN,
				ToQN:     path,
				EdgeType: "IMPORTS",
				File:     v.fileRel,
				Line:     line,
				Source:   "go-ast",
			})
		}
		return nil
	}
	return v
}

// receiverTypeName extracts the type name from a Go method receiver expression.
// `*Store` -> "Store"; `Store` -> "Store"; `Store[T]` -> "Store".
func receiverTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	}
	return ""
}

// extractCallee returns the syntactic callee form:
//
//	foo()         -> "foo"           (bare ident)
//	pkg.Fn()      -> "pkg.Fn"         (qualified)
//	x.method()    -> "method"         (method, receiver unresolved)
//	(*T).method() -> "T.method"       (method expression)
//	closure()     -> ""               (skip)
//	f.g.h.call()  -> "call"           (multi-level selector, best-effort)
func extractCallee(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		sel := f.Sel.Name
		// If receiver is a plain identifier, it could be a package qualifier
		// or a variable. Syntactically we can't tell; emit "recv.sel" — the
		// Python wrapper does internal-vs-external filtering.
		if recvIdent, ok := f.X.(*ast.Ident); ok {
			return recvIdent.Name + "." + sel
		}
		// For deeper selectors or type assertions, emit just the method name.
		return sel
	case *ast.ParenExpr:
		return extractCallee(f.X)
	case *ast.StarExpr:
		return extractCallee(f.X)
	}
	return ""
}

func processFile(path, crateRoot, project string, allEdges *[]Edge, allDefs *[]string) error {
	rel, err := filepath.Rel(crateRoot, path)
	if err != nil {
		return err
	}
	relSlash := filepath.ToSlash(rel)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	// Strip .go from file path, replace / with . for QN.
	// Keep `_test` suffix — code-graph preserves it (cbm_test.go -> cbm_test
	// in the QN, not cbm). Earlier I stripped it, which put 907 test-caller
	// FNs on the books. Verified empirically 2026-04-24 on indexed
	// internal-cbm: code-graph QN is `cbm_test.TestGoFunctionExtraction`.
	relNoExt := strings.TrimSuffix(relSlash, ".go")
	// Use the bare filename — code-graph's fqn.Compute splits by path
	// separators BUT for Go packages, the "file" segment is just the
	// filename (without directories). Verified: `internal/store/router.go`
	// indexed with root = `internal/store/` becomes project+router+fn,
	// not project+router+go+fn.
	parts := strings.Split(relNoExt, "/")
	if len(parts) == 0 || parts[0] == "" {
		return nil
	}
	fileQN := project + "." + strings.Join(parts, ".")

	v := &visitor{
		fset:    fset,
		project: project,
		fileQN:  fileQN,
		fileRel: relSlash,
	}
	ast.Walk(v, file)
	*allEdges = append(*allEdges, v.edges...)
	*allDefs = append(*allDefs, v.defs...)
	return nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: oracle-go-ast <subset_root> <project_name>")
		os.Exit(2)
	}
	crateRoot := os.Args[1]
	project := os.Args[2]

	info, err := os.Stat(crateRoot)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "subset_root not a directory: %s\n", crateRoot)
		os.Exit(2)
	}

	var edges []Edge
	var defs []string
	errors := 0
	parsed := 0

	err = filepath.WalkDir(crateRoot, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		if e := processFile(p, crateRoot, project, &edges, &defs); e != nil {
			errors++
			fmt.Fprintf(os.Stderr, "  error: %v\n", e)
			return nil
		}
		parsed++
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk error: %v\n", err)
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr,
		"oracle-go-ast: project=%s files_parsed=%d edges=%d defs=%d errors=%d\n",
		project, parsed, len(edges), len(defs), errors,
	)

	out := Output{Edges: edges, Defs: defs}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "json encode error: %v\n", err)
		os.Exit(2)
	}
}
