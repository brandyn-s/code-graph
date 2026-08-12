// oracle-go-rta emits an independent compiler-analysis CALLS oracle.
//
// It loads a Go module with go/packages, builds SSA, and runs Rapid Type
// Analysis with every source function as a root. Edges are identified only by
// caller/callee definition coordinates. It does not read SCIP, code-graph
// databases, or code-graph output.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

type coordinate struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type oracleEdge struct {
	Caller  coordinate `json:"caller"`
	Callee  coordinate `json:"callee"`
	Dynamic bool       `json:"dynamic"`
}

type oracleOutput struct {
	SchemaVersion int          `json:"schema_version"`
	Oracle        string       `json:"oracle"`
	ModuleRoot    string       `json:"module_root"`
	Files         []string     `json:"files"`
	Edges         []oracleEdge `json:"edges"`
}

func sourceCoordinate(root string, fset *token.FileSet, fn *ssa.Function) (coordinate, bool) {
	if fn == nil || !fn.Pos().IsValid() {
		return coordinate{}, false
	}
	position := fset.PositionFor(fn.Pos(), true)
	if position.Filename == "" || position.Line <= 0 {
		return coordinate{}, false
	}
	abs, err := filepath.Abs(position.Filename)
	if err != nil {
		return coordinate{}, false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return coordinate{}, false
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
		return coordinate{}, false
	}
	return coordinate{File: rel, Line: position.Line}, true
}

func edgeKey(edge oracleEdge) string {
	return fmt.Sprintf("%s:%d>%s:%d", edge.Caller.File, edge.Caller.Line, edge.Callee.File, edge.Callee.Line)
}

func buildOracle(root string) (*oracleOutput, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve module root: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absRoot); resolveErr == nil {
		absRoot = resolved
	}
	if info, statErr := os.Stat(filepath.Join(absRoot, "go.mod")); statErr != nil || info.IsDir() {
		return nil, errors.New("module root must contain go.mod")
	}

	config := &packages.Config{
		Dir: absRoot,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes,
		Tests: false,
	}
	loaded, err := packages.Load(config, "./...")
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	if packages.PrintErrors(loaded) != 0 {
		return nil, errors.New("module contains package loading errors")
	}
	program, _ := ssautil.AllPackages(loaded, ssa.InstantiateGenerics)
	program.Build()

	functions := ssautil.AllFunctions(program)
	roots := make([]*ssa.Function, 0, len(functions))
	fileSet := make(map[string]struct{})
	for function := range functions {
		coord, ok := sourceCoordinate(absRoot, program.Fset, function)
		if !ok || function.Blocks == nil {
			continue
		}
		fileSet[coord.File] = struct{}{}
		roots = append(roots, function)
	}
	if len(roots) == 0 {
		return nil, errors.New("module contains no source functions")
	}

	result := rta.Analyze(roots, true)
	edgesByKey := make(map[string]oracleEdge)
	err = callgraph.GraphVisitEdges(result.CallGraph, func(edge *callgraph.Edge) error {
		caller, callerOK := sourceCoordinate(absRoot, program.Fset, edge.Caller.Func)
		callee, calleeOK := sourceCoordinate(absRoot, program.Fset, edge.Callee.Func)
		if !callerOK || !calleeOK {
			return nil
		}
		dynamic := edge.Site != nil && edge.Site.Common().StaticCallee() == nil
		candidate := oracleEdge{Caller: caller, Callee: callee, Dynamic: dynamic}
		key := edgeKey(candidate)
		if prior, exists := edgesByKey[key]; !exists || (prior.Dynamic && !dynamic) {
			edgesByKey[key] = candidate
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("visit call graph: %w", err)
	}

	edges := make([]oracleEdge, 0, len(edgesByKey))
	for _, edge := range edgesByKey {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(i, j int) bool { return edgeKey(edges[i]) < edgeKey(edges[j]) })
	files := make([]string, 0, len(fileSet))
	for file := range fileSet {
		files = append(files, file)
	}
	sort.Strings(files)
	return &oracleOutput{
		SchemaVersion: 1,
		Oracle:        "go-ssa-rta-all-source-roots-v1",
		ModuleRoot:    absRoot,
		Files:         files,
		Edges:         edges,
	}, nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: oracle-go-rta <module-root>")
		os.Exit(2)
	}
	output, err := buildOracle(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "oracle-go-rta: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintf(os.Stderr, "oracle-go-rta: encode: %v\n", err)
		os.Exit(1)
	}
}
