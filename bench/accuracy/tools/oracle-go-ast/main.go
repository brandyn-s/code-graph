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
	// paramTypeStack: param identifier -> param type name (Y.6, 2026-05-02).
	// Used to resolve `cmd.Method` callees inside
	// `func writeCommands(cmd *Command, ...)` where cmd is a function
	// parameter, not a receiver.
	paramTypeStack []map[string]string
	// varTypeStack: local variable identifier -> declared type name (Plan
	// 5 Phase E, 2026-05-06). Populated from `var p *S` declarations and
	// short-var assignments where the RHS is a struct literal (`s := S{}`,
	// `p := &S{}`). Used to resolve `p.Method` callees on local
	// variables. Pre-existing Y.5 (receiver) and Y.6 (parameter) handlers
	// don't fire on local-var calls.
	varTypeStack []map[string]string
	edges        []Edge
	defs         []string
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
		// Y.6: extract parameter types so calls like `cmd.Method()` inside a
		// free function `func writeCommands(cmd *Command, ...)` can be
		// resolved the same way as method-receiver calls. Mirrors Y.5 but
		// applies to function parameters of typed-struct shape.
		paramTypes := extractParamTypes(n.Type)

		v.fnStack = append(v.fnStack, seg)
		v.recvNameStack = append(v.recvNameStack, recvName)
		v.recvTypeStack = append(v.recvTypeStack, recvType)
		v.paramTypeStack = append(v.paramTypeStack, paramTypes)
		// Plan 5 Phase E: empty per-function map for local-variable types.
		// Populated as we walk DeclStmt/AssignStmt nodes in the body.
		v.varTypeStack = append(v.varTypeStack, make(map[string]string))
		v.defs = append(v.defs, v.currentCaller())
		if n.Body != nil {
			ast.Walk(v, n.Body)
		}
		v.fnStack = v.fnStack[:len(v.fnStack)-1]
		v.recvNameStack = v.recvNameStack[:len(v.recvNameStack)-1]
		v.recvTypeStack = v.recvTypeStack[:len(v.recvTypeStack)-1]
		v.paramTypeStack = v.paramTypeStack[:len(v.paramTypeStack)-1]
		v.varTypeStack = v.varTypeStack[:len(v.varTypeStack)-1]
		return nil

	case *ast.DeclStmt:
		// Plan 5 Phase E: capture `var p *S` / `var s S` declarations so
		// later CallExpr resolution can substitute `p.M()` to `S.M()`.
		if gd, ok := n.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
			v.recordVarDecl(gd)
		}
		return v

	case *ast.AssignStmt:
		// Plan 5 Phase E: capture `s := S{}` and `p := &S{}` short-var
		// declarations. Token must be DEFINE (`:=`) for the LHS to be
		// fresh new bindings; ASSIGN (`=`) reuses existing names whose
		// types we already captured (or which originate outside the
		// function's local scope).
		if n.Tok == token.DEFINE {
			v.recordShortVarDecl(n)
		}
		return v

	case *ast.CallExpr:
		callee := extractCallee(n.Fun)
		// Plan 4 T3a (Go oracle gap fix #1, 2026-05-06 roundtable):
		// CGO callees look like `C.foo` — they're not Go-side calls and
		// have no Go def the wrapper can resolve. Code-graph's CALLS
		// extractor doesn't emit these as graph edges either. Without
		// this skip, the oracle emits "C.foo" callees that the wrapper
		// drops as unresolvable, but they still inflate the
		// "calls_path_dropped" count. Skipping at oracle-emit time
		// keeps the F1 denominator honest.
		//
		// Detection: SelectorExpr with X = Ident{Name: "C"}. The Go
		// language reserves "C" as the cgo package import; any
		// selector with this prefix is a CGO call, including
		// type names (C.int) and unsafe pointer ops.
		if isCGOCallee(n.Fun) {
			callee = ""
		}
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
		// Y.6 (2026-05-02 plateau plan, follow-up to PR #140): extend
		// receiver-type substitution to function parameters. When inside
		// `func writeCommands(cmd *Command, ...)`, a call `cmd.Method()`
		// has callee "cmd.Method" — Y.5 doesn't fire because cmd is a
		// parameter, not a method receiver. The wrapper drops "cmd.Method"
		// because "cmd" isn't a known file segment. Substituting to
		// "Command.Method" lets the wrapper resolve via recv_method_to_qns.
		// Discovered while running /plateau-diagnose on cobra-go's
		// function-body precision drop (P=0.60); 67/67 sampled FPs were
		// real edges that the oracle couldn't see.
		if callee != "" && len(v.paramTypeStack) > 0 {
			pt := v.paramTypeStack[len(v.paramTypeStack)-1]
			if len(pt) > 0 {
				if dotIdx := strings.Index(callee, "."); dotIdx > 0 {
					paramName := callee[:dotIdx]
					methodName := callee[dotIdx+1:]
					// Single-level call only (skip `p.field.method`)
					if !strings.Contains(methodName, ".") {
						if paramType, ok := pt[paramName]; ok && paramType != "" {
							callee = paramType + "." + methodName
						}
					}
				}
			}
		}
		// Plan 5 Phase E (2026-05-06): local-variable type substitution.
		// Mirrors Y.5/Y.6 but for variables declared inside the function
		// body via `var p *S` (DeclStmt) or `s := S{}` / `p := &S{}`
		// (AssignStmt with token.DEFINE). The four method-call shapes
		// covered:
		//   value-recv pointer-call:  func (s S) M(); var p *S; p.M()    → S.M (Go auto-derefs)
		//   value-recv value-call:    func (s S) M(); var s S;  s.M()    → S.M
		//   ptr-recv pointer-call:    func (s *S) M(); var p *S; p.M()   → S.M
		//   ptr-recv value-call:      func (s *S) M(); var s S;  s.M()   → S.M (Go auto-takes-addr)
		// All four substitute to the same `Type.Method` form. The wrapper's
		// recv_method_to_qns index resolves the callee identically, the
		// same way Y.5 produces `Pipeline.Inner` from `p.Inner` inside
		// `func (p *Pipeline) Outer()`.
		if callee != "" && len(v.varTypeStack) > 0 {
			vt := v.varTypeStack[len(v.varTypeStack)-1]
			if len(vt) > 0 {
				if dotIdx := strings.Index(callee, "."); dotIdx > 0 {
					varName := callee[:dotIdx]
					methodName := callee[dotIdx+1:]
					// Single-level call only (skip `p.field.method`).
					if !strings.Contains(methodName, ".") {
						if varType, ok := vt[varName]; ok && varType != "" {
							callee = varType + "." + methodName
						}
					}
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

// isCGOCallee reports whether the call expression's function is a
// CGO-side reference (`C.foo`, `C.int`, `(*C.struct_foo)(...)` etc.).
// CGO selectors are recognized by an outer SelectorExpr whose X is an
// Ident named "C". In Go, the package alias "C" is reserved for the
// cgo import directive (`import "C"`); user code cannot shadow it.
//
// Plan 4 T3a (2026-05-06): introduced to drop CGO callees before they
// enter the edge stream. Match on the call's function expression
// rather than on `extractCallee`'s string output because the latter
// loses the X-Ident name once nested calls or type assertions wrap it.
func isCGOCallee(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok && id.Name == "C" {
			return true
		}
	case *ast.ParenExpr:
		return isCGOCallee(f.X)
	case *ast.StarExpr:
		// `(*C.struct_foo)(unsafe.Pointer(p))` — a type-conversion
		// call. The function expression is *(*C.struct_foo); descend
		// through StarExpr to recognize it as CGO.
		return isCGOCallee(f.X)
	case *ast.IndexExpr:
		return isCGOCallee(f.X)
	case *ast.IndexListExpr:
		return isCGOCallee(f.X)
	}
	return false
}

// recordVarDecl walks `var x T` and `var x, y T` declarations and
// records the local variable -> type mapping in the current function's
// var-type frame.
//
// Plan 5 Phase E: only `var name Type` shape is recorded; `var name = expr`
// (without an explicit type) is skipped because we'd need full type
// inference to derive Type from the RHS. The struct-literal short-decl
// case is handled by recordShortVarDecl below.
func (v *visitor) recordVarDecl(gd *ast.GenDecl) {
	if len(v.varTypeStack) == 0 {
		return
	}
	frame := v.varTypeStack[len(v.varTypeStack)-1]
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok || vs.Type == nil {
			continue
		}
		typeName := paramTypeName(vs.Type)
		if typeName == "" {
			continue
		}
		for _, name := range vs.Names {
			if name.Name != "" && name.Name != "_" {
				frame[name.Name] = typeName
			}
		}
	}
}

// recordShortVarDecl walks `s := S{}` / `p := &S{}` / `s := SomeFunc()`
// short-var declarations and records the LHS->type mapping when the RHS
// shape lets us derive the type without full type inference.
//
// Plan 5 Phase E: covered RHS shapes:
//   - struct literal `S{}` → record S
//   - address-of struct literal `&S{}` → record S
//   - composite literal `S{f: 1, g: 2}` → record S
//
// NOT covered (silently skipped):
//   - function-call returns (`s := NewS()`) — would need return-type
//     resolution which depends on the function's declared signature
//   - type assertions (`s := iface.(S)`)
//   - complex expressions (`s := getS(x).child`)
//   - multi-value assignments where one side is a simple struct and the
//     other isn't (rare)
//
// Skipped cases just leave the variable's type un-recorded; the oracle
// keeps emitting the pre-substitution form, which the wrapper drops as
// unresolvable. Safe by construction — false-positives don't appear.
func (v *visitor) recordShortVarDecl(as *ast.AssignStmt) {
	if len(v.varTypeStack) == 0 {
		return
	}
	frame := v.varTypeStack[len(v.varTypeStack)-1]
	// Pair-wise LHS / RHS. `a, b := f()` has LHS=2, RHS=1 — skip those.
	if len(as.Lhs) != len(as.Rhs) {
		return
	}
	for i, lhs := range as.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || ident.Name == "" || ident.Name == "_" {
			continue
		}
		typeName := rhsLiteralType(as.Rhs[i])
		if typeName == "" {
			continue
		}
		frame[ident.Name] = typeName
	}
}

// rhsLiteralType extracts the struct type name from RHS expressions
// where the type is syntactically obvious. Returns "" for everything
// else so unresolvable RHS shapes don't poison the var-type table.
func rhsLiteralType(e ast.Expr) string {
	switch r := e.(type) {
	case *ast.CompositeLit:
		// `S{}` or `S{Field: x}` — extract S from the type expression.
		if r.Type != nil {
			return paramTypeName(r.Type)
		}
	case *ast.UnaryExpr:
		// `&S{}` — descend through the address-of operator.
		if r.Op == token.AND {
			return rhsLiteralType(r.X)
		}
	case *ast.ParenExpr:
		return rhsLiteralType(r.X)
	}
	return ""
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

// extractParamTypes returns a map of parameter identifier -> simple type name
// for a function declaration. Used by Y.6 to substitute callee form when a
// CallExpr targets a method on a parameter of typed-struct shape.
//
// Type extraction handles:
//   - `cmd *Command`        -> {"cmd": "Command"}
//   - `a, b *Command`       -> {"a": "Command", "b": "Command"}
//   - `cmd Command`         -> {"cmd": "Command"}
//   - `args ...*Command`    -> {} (variadic skipped — calling args.Method
//     on a slice is a different shape)
//
// SKIPPED (returned as empty entries / not added):
//   - Interface types (io.Writer, context.Context) — methods aren't owned
//     by a single type; substituting to "Writer.Close" produces no match.
//     Handled via the SelectorExpr branch returning "".
//   - Map / slice types — Type is *ast.MapType / *ast.ArrayType; method
//     calls on these don't resolve to a single struct type.
//   - Function types — `cb func(int) string` — no struct methods to
//     resolve.
//   - Channel types — same reasoning.
//
// The result map is intentionally conservative: only single-struct types
// that the wrapper's recv_method_to_qns index can resolve get included.
// Other shapes are silently dropped — the oracle will keep emitting the
// pre-substitution form, which the wrapper will (correctly) drop as
// unresolvable.
func extractParamTypes(funcType *ast.FuncType) map[string]string {
	result := make(map[string]string)
	if funcType == nil || funcType.Params == nil {
		return result
	}
	for _, field := range funcType.Params.List {
		if len(field.Names) == 0 {
			// Anonymous param — can't reference it from a CallExpr anyway
			continue
		}
		typeName := paramTypeName(field.Type)
		if typeName == "" {
			continue
		}
		for _, name := range field.Names {
			if name.Name != "" && name.Name != "_" {
				result[name.Name] = typeName
			}
		}
	}
	return result
}

// paramTypeName extracts the simple struct-type name from a parameter type
// expression. Returns "" for anything that isn't a concrete struct type
// (interfaces, maps, slices, channels, function types, generics with
// constraints).
//
// Handles:
//   - `*Command`             -> "Command"
//   - `Command`              -> "Command"
//   - `**Command`            -> "Command"  (pointer-to-pointer, rare)
//   - `Command[T]`           -> "Command"  (generic struct)
//
// Returns "" for:
//   - `io.Writer` (SelectorExpr — interface or qualified type, type identity unclear)
//   - `[]Command` (ArrayType — calling method on slice doesn't resolve)
//   - `map[string]int`, `chan T`, `func()`, etc.
//   - Anonymous struct literals
func paramTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return paramTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return paramTypeName(t.X)
	case *ast.IndexListExpr:
		return paramTypeName(t.X)
	}
	// SelectorExpr (qualified types like io.Writer, context.Context),
	// ArrayType, MapType, ChanType, FuncType, InterfaceType,
	// Ellipsis (variadic), StructType, all return "".
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
	// not project+router+go+fn. ACC-009 fix: previously we joined the full
	// rel path with `.`, which doubled the package segment for files like
	// `helpers/helpers.go` (produced `<project>.helpers.helpers`). Use
	// the basename so `helpers/helpers.go` → `<project>.helpers`.
	base := filepath.Base(relNoExt)
	if base == "" || base == "." {
		return nil
	}
	fileQN := project + "." + base

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
