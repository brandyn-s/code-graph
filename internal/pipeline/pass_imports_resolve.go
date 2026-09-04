// Imports pass: relative-import normalization and IMPORTS edge emission.
//
// Split from pipeline.go without behaviour changes.
package pipeline

import (
	"log/slog"
	slashpath "path"
	"path/filepath"
	"strings"

	"github.com/brandyn-s/code-graph/internal/fqn"
	"github.com/brandyn-s/code-graph/internal/store"
)

// resolvePythonRelativeImport handles Python relative-import syntax.
//
// CBM's extract_imports.c builds the ModulePath via
// `cbm_arena_sprintf("%s.%s", mod_path, name)` where mod_path is the
// text of tree-sitter's `relative_import` node and name is the imported
// symbol. This ALWAYS produces a "." separator between mod_path and
// name, which collides with the leading dots from the relative_import
// node text. So:
//
//	from . import sibling      → mod_path=".",      full="..sibling"
//	from .sub import helper    → mod_path=".sub",   full=".sub.helper"
//	from .. import top         → mod_path="..",     full="...top"
//	from ..top import x        → mod_path="..top",  full="..top.x"
//
// To recover semantic-leading-dots from raw-leading-dots, we use the
// fact that Python's imported `name` is always a single identifier
// (no internal dots): if the `rest` after stripping leading dots
// contains 0 dots, the source was an "all-dots mod_path" (dots only,
// no module after them) and the raw-dot-count is one MORE than the
// semantic dot count. If rest has internal dots, the source had
// `dots + module` and raw == semantic.
//
// Rules (CG-3):
//   - 1 semantic dot = current package (strip 1 segment from moduleQN)
//   - N semantic dots = strip N segments
//   - rest (the module portion if any, plus imported name) appended to parent
//   - if rest was just the imported name ("all-dots" case), localName is
//     appended to parent
//
// Returns the resolved target QN, or the original targetQN if no leading
// dot was present (no rewrite needed).
func resolvePythonRelativeImport(targetQN, moduleQN, localName string) string {
	rawDots := 0
	for rawDots < len(targetQN) && targetQN[rawDots] == '.' {
		rawDots++
	}
	if rawDots == 0 {
		return targetQN
	}
	rest := targetQN[rawDots:]
	// Recover semantic dot count: if rest has no internal dots, the
	// source was "from <dots> import <name>" (no module), raw = sem+1.
	// Otherwise raw = sem.
	semDots := rawDots
	allDotsMode := !strings.Contains(rest, ".")
	if allDotsMode {
		semDots = rawDots - 1
		if semDots < 1 {
			// Defensive: shouldn't happen — `from . import X` produces
			// raw=2, sem=1. Single raw dot would mean a flat absolute
			// path that started with a dot, which shouldn't occur.
			return targetQN
		}
	}
	parts := strings.Split(moduleQN, ".")
	if len(parts) < semDots {
		// More dots than the moduleQN can support — give up.
		return targetQN
	}
	parent := strings.Join(parts[:len(parts)-semDots], ".")
	if allDotsMode {
		// rest IS the imported name. Target is parent.<name>.
		return parent + "." + rest
	}
	// rest is "module.name" or "module.sub.name". Target QN keeps
	// everything; downstream suffix fallback strips trailing name to
	// find the module.
	return parent + "." + rest
}

// normalizePythonRelativeImports rewrites relative-import paths (`.util`,
// `..pkg`, etc.) in p.importMaps and p.importBindings to absolute QNs
// rooted at the importing module's parent package.
//
// Before this normalization (Phase B 2026-05-14), Python relative imports
// only got resolved when building IMPORTS edges in passImports. The
// resolver consulted the still-raw maps in passCalls, where leading-dot
// paths never matched any registered QN. applyImportBindingFilter would
// then misclassify the bare-name call as external and drop the call
// entirely — producing the requests-adversarial recall floor (5 of 10
// sampled FNs were `from .X import Y` cross-module calls per plan #523).
//
// The mutation is safe to call on absolute imports too:
// resolvePythonRelativeImport returns the input unchanged when no
// leading dot is present.
func (p *Pipeline) normalizePythonRelativeImports() {
	for moduleQN, importMap := range p.importMaps {
		moduleNode, _ := p.findNodeByQN(p.ProjectName, moduleQN)
		if moduleNode == nil || strings.ToLower(filepath.Ext(moduleNode.FilePath)) != ".py" {
			continue
		}
		for localName, targetQN := range importMap {
			resolved := resolvePythonRelativeImport(targetQN, moduleQN, localName)
			if resolved != targetQN {
				importMap[localName] = resolved
			}
		}
		bindings, ok := p.importBindings[moduleQN]
		if !ok {
			continue
		}
		for bareName, targetQN := range bindings {
			resolved := resolvePythonRelativeImport(targetQN, moduleQN, bareName)
			if resolved != targetQN {
				bindings[bareName] = resolved
			}
		}
	}
}

var jsTsModuleExtensions = map[string]struct{}{
	".js": {}, ".jsx": {}, ".ts": {}, ".tsx": {},
	".mjs": {}, ".cjs": {}, ".mts": {}, ".cts": {},
}

// resolveJSTSRelativeImport converts a project-local ES module specifier into
// the same canonical module QN used by the graph's Module nodes. In NodeNext
// projects, source commonly says `./math.js` while the compiler resolves that
// specifier to `math.ts`; ModuleQN deliberately strips either extension, so the
// file-pair resolution remains exact without guessing a symbol target.
func resolveJSTSRelativeImport(target, importerFile, projectName string) string {
	if !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") {
		return target
	}
	if _, ok := jsTsModuleExtensions[strings.ToLower(filepath.Ext(importerFile))]; !ok {
		return target
	}
	// Keep non-code assets out of the TypeScript module graph. Without this,
	// `./App.css` and `App.tsx` collapse to the same extensionless ModuleQN and
	// manufacture a self IMPORTS edge.
	targetExt := strings.ToLower(slashpath.Ext(target))
	if targetExt != "" {
		if _, ok := jsTsModuleExtensions[targetExt]; !ok {
			return target
		}
	}
	resolvedPath := slashpath.Clean(slashpath.Join(slashpath.Dir(filepath.ToSlash(importerFile)), target))
	if resolvedPath == ".." || strings.HasPrefix(resolvedPath, "../") {
		return target
	}
	// Extensionless `./index` specifiers still refer to the JS/TS index module.
	// ModuleQN can only apply its index-file canonicalization when it can see a
	// JS/TS extension, so borrow the importer's extension for this identity-only
	// conversion. This keeps clean-buffer and incremental-SQLite resolution on
	// the same canonical folder QN.
	if slashpath.Ext(resolvedPath) == "" && slashpath.Base(resolvedPath) == "index" {
		resolvedPath += strings.ToLower(filepath.Ext(importerFile))
	}
	return fqn.ModuleQN(projectName, resolvedPath)
}

// normalizeJSTSRelativeImports makes the resolved module identity available to
// both passImports and the later call resolver. This is intentionally limited
// to explicit relative ES-module specifiers; package exports and tsconfig path
// aliases require compiler metadata and remain outside the heuristic tier.
func (p *Pipeline) normalizeJSTSRelativeImports() {
	for moduleQN, importMap := range p.importMaps {
		moduleNode, _ := p.findNodeByQN(p.ProjectName, moduleQN)
		if moduleNode == nil {
			continue
		}
		for localName, targetQN := range importMap {
			importMap[localName] = resolveJSTSRelativeImport(
				targetQN, moduleNode.FilePath, p.ProjectName,
			)
		}
		bindings, ok := p.importBindings[moduleQN]
		if !ok {
			continue
		}
		for bareName, targetQN := range bindings {
			bindings[bareName] = resolveJSTSRelativeImport(
				targetQN, moduleNode.FilePath, p.ProjectName,
			)
		}
	}
}

// passImports creates IMPORTS edges from the import maps built during pass 2.
func (p *Pipeline) passImports() {
	slog.Info("pass2b.imports")
	// Resolve Python relative imports (`.util` -> `<parent_pkg>.util`)
	// in p.importMaps and p.importBindings before any consumer reads
	// them. Without this, the resolver's applyImportBindingFilter sees
	// raw leading-dot paths and drops Python `from .X import Y` calls.
	p.normalizePythonRelativeImports()
	p.normalizeJSTSRelativeImports()
	count := 0
	suffixHits := 0
	for moduleQN, importMap := range p.importMaps {
		moduleNode, _ := p.findNodeByQN(p.ProjectName, moduleQN)
		if moduleNode == nil {
			continue
		}
		for localName, targetQN := range importMap {
			// CG-3 normalization above mutated importMap in place;
			// this call is a no-op when targetQN has no leading dot.
			// Kept as defensive belt-and-suspenders for any code path
			// that bypasses normalizePythonRelativeImports.
			targetQN = resolvePythonRelativeImport(targetQN, moduleQN, localName)
			// Try to find the target as a Module node first
			targetNode, _ := p.findNodeByQN(p.ProjectName, targetQN)
			if targetNode == nil {
				// Try treating import path as a relative file path (e.g. "utils.mag", "lib/helpers.h")
				resolvedQN := fqn.ModuleQN(p.ProjectName, targetQN)
				if resolvedQN != targetQN {
					targetNode, _ = p.findNodeByQN(p.ProjectName, resolvedQN)
				}
			}
			if targetNode == nil {
				// Suffix fallback: nested packages. For a Python project laid
				// out as `src/flask/` (PEP 518 src-layout), `from flask.ctx
				// import X` extracts as targetQN=`flask.ctx.X` but the actual
				// node lives at `<project>.src.flask.ctx`. A prefix-only
				// resolver fails; suffix search finds it.
				//
				// Also handles Rust: the Rust extractor emits raw `use` paths
				// with `::` separators (e.g. `canstatd_types::CanError`).
				// Stored QNs use `.`, so we translate `::` -> `.` for the
				// suffix search. Mirrors the 2026-04-24 Python fix but for
				// the Rust import-path form.
				//
				// Constraints:
				//  - Module-only targets. Allowing Function/Class regressed
				//    mcp-servers IMPORTS precision by linking symbol imports
				//    to their definition (asymmetric with oracle's
				//    module-granularity emission).
				//  - Single match required — ambiguous suffixes shouldn't guess.
				//
				// Candidates, most specific first:
				//   `flask.ctx.X` -> `flask.ctx` (drop trailing segment)
				//   (Rust) `foo::bar::Baz` -> `foo.bar.Baz` -> `foo.bar`
				dotted := strings.ReplaceAll(targetQN, "::", ".")
				if _, isJSTS := jsTsModuleExtensions[strings.ToLower(filepath.Ext(moduleNode.FilePath))]; isJSTS &&
					!strings.HasPrefix(targetQN, "@") {
					// TypeScript baseUrl-style imports (`components/Button`) are
					// project-root-relative. Convert only for the existing unique,
					// Module-only suffix resolver; ambiguity still fails closed.
					dotted = strings.ReplaceAll(filepath.ToSlash(dotted), "/", ".")
				}
				candidates := []string{dotted}
				if idx := strings.LastIndex(dotted, "."); idx > 0 {
					candidates = append(candidates, dotted[:idx])
				}
				// Rust crate-name substitution: if the first segment of the
				// dotted path matches a known crate in this workspace, also
				// try the form with that segment replaced by the crate's
				// actual directory path. `use canstatd_types::CanError` with
				// crate_map["canstatd_types"] = "types.src" yields candidate
				// `types.src.CanError`. Suffix-matched against the node
				// `<project>.types.src.lib.CanError` it finds a hit.
				if len(p.rustCrateMap) > 0 {
					// For `use crate_name` or `use crate_name::rest`, the
					// corresponding Module node is typically at
					// `<project>.<crate_path>.lib` (library crates) or
					// `<project>.<crate_path>.main` (binary crates) — since
					// the filename (lib.rs / main.rs) gets included in the QN.
					// We also try the crate_path as-is and with the remaining
					// :: path appended, to cover edge cases.
					var crateKey, restOfPath string
					if firstDot := strings.Index(dotted, "."); firstDot > 0 {
						crateKey = dotted[:firstDot]
						restOfPath = dotted[firstDot:] // includes leading dot
					} else {
						crateKey = dotted
						restOfPath = ""
					}
					if cratePath, ok := p.rustCrateMap[crateKey]; ok {
						// Prefer lib.rs / main.rs first — most imports target
						// the crate root which lives in one of these.
						candidates = append(candidates, cratePath+".lib"+restOfPath)
						candidates = append(candidates, cratePath+".main"+restOfPath)
						// Then the non-file variants.
						candidates = append(candidates, cratePath+restOfPath)
						if restOfPath != "" {
							// Strip trailing segment too.
							if idx := strings.LastIndex(cratePath+restOfPath, "."); idx > 0 {
								candidates = append(candidates, (cratePath + restOfPath)[:idx])
							}
						}
					}
				}
				// Label policy for suffix matches:
				//  - For Python/generic: Module-only (matches oracle form).
				//  - For Rust crate-resolved candidates (those containing a
				//    crate_map substitution): allow Module/Class/Struct/Enum/
				//    Function. `use foo::Bar` is commonly importing a type or
				//    function, not a module; the oracle doesn't measure Rust
				//    IMPORTS so there's no F1 cost to being more inclusive.
				for i, c := range candidates {
					hits, err := p.findNodesByQNSuffix(p.ProjectName, c)
					if err != nil || len(hits) == 0 {
						continue
					}
					isRustCrateResolved := i >= 2 // candidates[0..1] are Python-style; [2..] are crate-substituted
					// Filter by label. Rust crate-resolved allows symbols;
					// Python-style is Module-only to preserve module-granularity
					// matching against the ast oracle.
					var pick *store.Node
					for _, h := range hits {
						label := h.Label
						ok := false
						if isRustCrateResolved {
							ok = label == "Module" || label == "Class" || label == "Struct" ||
								label == "Enum" || label == "Function" || label == "Trait"
						} else {
							ok = label == "Module"
						}
						if !ok {
							continue
						}
						// Among eligible hits, prefer the SHORTEST QN — most
						// specific match to the suffix, least speculative.
						// `use foo::Bar` should resolve to the crate's root
						// `Bar`, not a deeply-nested re-export.
						if pick == nil || len(h.QualifiedName) < len(pick.QualifiedName) {
							pick = h
						}
					}
					if pick == nil {
						continue
					}
					targetNode = pick
					suffixHits++
					break
				}
			}
			if targetNode == nil {
				logImportDrop(moduleQN, localName, targetQN)
				continue
			}
			_ = p.insertEdge(&store.Edge{
				Project:  p.ProjectName,
				SourceID: moduleNode.ID,
				TargetID: targetNode.ID,
				Type:     store.EdgeImports,
				Properties: map[string]any{
					"alias": localName,
				},
			})
			count++
		}
	}
	slog.Info("pass2b.imports.done", "edges", count, "suffix_fallback_hits", suffixHits)
}
