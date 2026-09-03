package pipeline

import (
	"os"
	"regexp"
	"strings"

	"github.com/brandyn-s/code-graph/internal/cbm"
)

// augmentRustClosureTypeMap scans a Rust source file for closure patterns
// where the closure parameter's type is inferable from the receiver of
// the higher-order method call, and adds the inferred binding to the
// enclosing function's TypeMap.
//
// v1 pattern (this implementation):
//
//	<Type>::(iter|iter_mut|into_iter)()
//	    .<higher-order>(|<param>| ...)
//
// where Type is a simple identifier (no module prefix, no generics).
// Inferred binding: param -> Type.
//
// Examples this catches (from PSM assetman):
//
//	Platform::iter().map(|platform| platform.label())
//	    -> binds platform: Platform
//
// Patterns this does NOT yet catch (v2 work):
//   - `vec.iter()` receivers (need vec's element type)
//   - chained iter methods: `.iter().filter().map()`
//   - module-qualified types: `crate::Foo::iter()`
//   - explicit closure type annotations: `|x: T|`
//
// Closes the dominant "receiver-resolution failure" pattern from the
// knowledge-base PR #491 / #492 terminal doc. Used alongside the
// Janusian-chain drop gate to handle closure-rooted chains.
//
// The function reads the file from disk (cheap: <2KB-200KB Rust files).
// Skips if the file isn't Rust or can't be read.
func augmentRustClosureTypeMap(
	filePath string,
	definitions []cbm.Definition,
	perFunc PerFuncTypeMap,
) {
	if !strings.HasSuffix(filePath, ".rs") {
		return
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	src := string(data)

	// Pattern: Type::(iter|iter_mut|into_iter)()
	//             .(map|filter|for_each|find|any|all|position|flat_map|inspect|filter_map|fold|count|min|max|min_by|max_by)
	//             (|param| ...
	// We allow whitespace and newlines between segments because Rust style
	// frequently splits chains across multiple lines.
	re := regexp.MustCompile(
		`([A-Z][A-Za-z0-9_]+)\s*::\s*(?:iter|iter_mut|into_iter)\s*\(\s*\)\s*\.\s*(?:map|filter|for_each|find|any|all|position|flat_map|inspect|filter_map|fold|count|min|max|min_by|max_by)\s*\(\s*\|\s*([A-Za-z_][A-Za-z0-9_]*)`,
	)

	matches := re.FindAllStringSubmatchIndex(src, -1)
	if len(matches) == 0 {
		return
	}

	// Build a sorted list of function definitions for binary-search-style
	// enclosing-function lookup by source line.
	type fnRange struct {
		startLine, endLine int
		qn                 string
	}
	var fns []fnRange
	for _, d := range definitions {
		if d.QualifiedName == "" || d.StartLine == 0 {
			continue
		}
		switch d.Label {
		case "Function", "Method":
			// Acceptable.
		default:
			continue
		}
		fns = append(fns, fnRange{
			startLine: d.StartLine,
			endLine:   d.EndLine,
			qn:        d.QualifiedName,
		})
	}
	if len(fns) == 0 {
		return
	}

	// Helper: find the deepest (smallest-range) function containing a line.
	// O(n) per lookup; n is per-file function count (~30-100 in PSM files).
	findEnclosing := func(line int) string {
		best := ""
		bestRange := -1
		for _, f := range fns {
			if line < f.startLine || line > f.endLine {
				continue
			}
			r := f.endLine - f.startLine
			if bestRange == -1 || r < bestRange {
				best = f.qn
				bestRange = r
			}
		}
		return best
	}

	for _, m := range matches {
		typeName := src[m[2]:m[3]]
		paramName := src[m[4]:m[5]]
		// 1-based line number of the match start.
		lineNum := strings.Count(src[:m[0]], "\n") + 1
		funcQN := findEnclosing(lineNum)
		if funcQN == "" {
			continue
		}
		if perFunc[funcQN] == nil {
			perFunc[funcQN] = make(TypeMap)
		}
		// Do not overwrite an explicit binding (parameter, let, self).
		if _, exists := perFunc[funcQN][paramName]; !exists {
			perFunc[funcQN][paramName] = typeName
		}
	}
}
