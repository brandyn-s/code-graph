package pipeline

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/brandyn-s/code-graph/internal/cbm"
	"github.com/brandyn-s/code-graph/internal/config"
	"github.com/brandyn-s/code-graph/internal/fqn"
	"github.com/brandyn-s/code-graph/internal/lang"
)

// Hybrid "lsp_local" resolver tier (opt-in: CODE_GRAPH_RESOLVER_TIER=lsp_local).
//
// Upstream codebase-memory-mcp resolves Python and Rust receivers with
// per-language C modules (py_lsp.c, rust_lsp.c, ~12k lines with their own
// type registry). This fork's registry tier already types locals from
// constructor assignments (inferTypesCBM) and walks field/return chains
// (resolveCallWithTypes). Measured on flask/requests/ripgrep before this
// tier, the remaining in-project unresolved receivers were dominated by
// (1) parameters that carry a type annotation the extractor does not bind,
// (2) pytest fixture parameters whose type is only visible in the fixture's
// return/yield, and (3) methods defined on a base class of the receiver's
// type. lspLocalTier closes those three gaps from the definitions and the
// source text already in hand, and tags every edge it enabled with
// resolver_tier="lsp_local" so the gain is measurable
// (bench/accuracy/unresolved_calls.py). What it does NOT do: flow-sensitive
// inference, Rust trait-method resolution through generics, or anything
// requiring the upstream C modules; see docs/resolver-tiers.md.
type lspLocalTier struct {
	// fixtureTypes: pytest fixture name -> class QN the fixture returns/yields,
	// project-wide. Names whose fixtures disagree on the type across files
	// are left out (pytest scoping would pick one; guessing risks a wrong
	// receiver type, which costs precision).
	fixtureTypes map[string]string
	// fixtureTypesByFile: relPath -> fixture name -> class QN for fixtures
	// defined in that file; a module-local fixture overrides conftest.
	fixtureTypesByFile map[string]map[string]string
	// classBases: class QN -> resolved base class QNs (Python bases, Rust
	// has none here; traits are handled by traitImpls).
	classBases map[string][]string
	// returnTypes: function QN -> class QN from a return annotation the
	// extractor did not surface (quoted forward references, Optional[...]).
	returnTypes map[string]string
	// paramTypes: function QN -> parameter name -> class QN, parsed from the
	// definition header once at build time and applied per file in augment.
	paramTypes map[string]map[string]string

	mu       sync.Mutex
	bindings map[string]map[string]bool // caller QN -> receiver root names bound by this tier
	stats    lspLocalStats
}

type lspLocalStats struct {
	paramBindings, fixtureBindings, inheritedResolutions int
}

func lspLocalTierEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(config.Get(config.ResolverTier)), "lsp_local")
}

// fixtureDef is a pytest fixture whose result type the tier tries to infer.
type fixtureDef struct {
	def       *cbm.Definition
	relPath   string
	moduleQN  string
	importMap map[string]string
	typeMap   TypeMap
	lines     []string
}

// buildLSPLocalTier scans every cached extraction once, before passCalls.
// Returns nil when the tier is not enabled.
func (p *Pipeline) buildLSPLocalTier() *lspLocalTier {
	if !lspLocalTierEnabled() {
		return nil
	}
	t := &lspLocalTier{
		fixtureTypes:       map[string]string{},
		fixtureTypesByFile: map[string]map[string]string{},
		classBases:         map[string][]string{},
		returnTypes:        map[string]string{},
		paramTypes:         map[string]map[string]string{},
		bindings:           map[string]map[string]bool{},
	}
	var fixtures []fixtureDef
	for relPath, ext := range p.extractionCache {
		if ext == nil || ext.Result == nil {
			continue
		}
		if ext.Language != lang.Python && ext.Language != lang.Rust {
			continue
		}
		fixtures = append(fixtures, p.collectTierFacts(t, relPath, ext)...)
	}
	p.resolveFixtureTypes(t, fixtures)

	slog.Info("resolver.lsp_local.built",
		"typed_params", len(t.paramTypes),
		"return_types", len(t.returnTypes),
		"fixtures", len(t.fixtureTypes),
		"classes_with_bases", len(t.classBases))
	return t
}

// collectTierFacts records class bases, typed parameters, and return types
// for one file and returns the pytest fixtures it defines.
func (p *Pipeline) collectTierFacts(t *lspLocalTier, relPath string, ext *cachedExtraction) []fixtureDef {
	moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
	importMap := p.importMaps[moduleQN]
	var lines []string
	var perFunc PerFuncTypeMap
	var fixtures []fixtureDef
	defs := ext.Result.Definitions
	for i := range defs {
		def := &defs[i]
		switch def.Label {
		case "Class", "Struct":
			t.recordClassBases(def, p.registry, moduleQN, importMap)
		case "Function", "Method":
			if lines == nil {
				lines = readSourceLines(p.RepoPath, relPath)
			}
			p.recordSignatureTypes(t, def, lines, ext.Language, moduleQN, importMap)
			if ext.Language == lang.Python && isPytestFixture(def) {
				if perFunc == nil {
					perFunc = inferTypesCBM(ext.Result.TypeAssigns, p.registry, moduleQN, importMap)
				}
				fixtures = append(fixtures, fixtureDef{def, relPath, moduleQN, importMap, perFunc[def.QualifiedName], lines})
			}
		}
	}
	return fixtures
}

// recordClassBases resolves a class's base classes to registry QNs.
func (t *lspLocalTier) recordClassBases(def *cbm.Definition, registry *FunctionRegistry, moduleQN string, importMap map[string]string) {
	for _, base := range def.BaseClasses {
		for _, name := range splitBaseClasses(base) {
			if qn := resolveAsClass(name, registry, moduleQN, importMap); qn != "" && qn != def.QualifiedName {
				t.classBases[def.QualifiedName] = append(t.classBases[def.QualifiedName], qn)
			}
		}
	}
}

// recordSignatureTypes records annotated parameter and return types of a
// function or method.
func (p *Pipeline) recordSignatureTypes(t *lspLocalTier, def *cbm.Definition, lines []string, language lang.Language, moduleQN string, importMap map[string]string) {
	sig := headerSignature(def, lines, language)
	params, ret := parseSignature(sig, language)
	for name, typ := range params {
		if qn := resolveAsClass(typ, p.registry, moduleQN, importMap); qn != "" {
			if t.paramTypes[def.QualifiedName] == nil {
				t.paramTypes[def.QualifiedName] = map[string]string{}
			}
			t.paramTypes[def.QualifiedName][name] = qn
		}
	}
	if ret == "" {
		return
	}
	if qn := resolveAsClass(ret, p.registry, moduleQN, importMap); qn != "" {
		t.returnTypes[def.QualifiedName] = qn
		if _, known := p.returnTypes[def.QualifiedName]; !known {
			if p.returnTypes == nil {
				p.returnTypes = make(ReturnTypeMap)
			}
			p.returnTypes[def.QualifiedName] = qn
		}
	}
}

// resolveFixtureTypes infers each fixture's result class: annotation first,
// then the returned/yielded expression. Fixtures depend on other fixtures
// (`client(app)` returns `app.test_client()`), so iterate to a fixpoint
// (bounded).
func (p *Pipeline) resolveFixtureTypes(t *lspLocalTier, fixtures []fixtureDef) {
	resolvedFixture := map[string]string{} // fixture QN -> class QN
	for round := 0; round < 4; round++ {
		progressed := false
		for i := range fixtures {
			fx := &fixtures[i]
			if resolvedFixture[fx.def.QualifiedName] != "" {
				continue
			}
			qn := p.fixtureResultType(t, fx)
			if qn == "" {
				continue
			}
			resolvedFixture[fx.def.QualifiedName] = qn
			progressed = true
			t.recordFixtureType(fx, qn)
		}
		if !progressed {
			break
		}
	}
}

// fixtureResultType returns the class QN a fixture yields, or "".
func (p *Pipeline) fixtureResultType(t *lspLocalTier, fx *fixtureDef) string {
	if qn := t.returnTypes[fx.def.QualifiedName]; qn != "" {
		return qn
	}
	if qn := p.returnTypes[fx.def.QualifiedName]; qn != "" {
		return qn
	}
	tm := TypeMap{}
	for k, v := range fx.typeMap {
		tm[k] = v
	}
	for name, cls := range t.paramTypes[fx.def.QualifiedName] {
		tm[name] = cls
	}
	for _, name := range untypedParams(headerSignature(fx.def, fx.lines, lang.Python), lang.Python) {
		if cls := t.fixtureType(fx.relPath, name); cls != "" {
			tm[name] = cls
		}
	}
	if expr := fixtureResultExpr(fx.def, fx.lines); expr != "" {
		return t.exprType(p, expr, tm, fx.moduleQN, fx.importMap)
	}
	return ""
}

// recordFixtureType stores a resolved fixture type per file and, when
// unambiguous, project-wide.
func (t *lspLocalTier) recordFixtureType(fx *fixtureDef, qn string) {
	if t.fixtureTypesByFile[fx.relPath] == nil {
		t.fixtureTypesByFile[fx.relPath] = map[string]string{}
	}
	t.fixtureTypesByFile[fx.relPath][fx.def.Name] = qn
	if existing, seen := t.fixtureTypes[fx.def.Name]; !seen {
		t.fixtureTypes[fx.def.Name] = qn
	} else if existing != qn {
		t.fixtureTypes[fx.def.Name] = "" // conflicting definitions: ambiguous project-wide
	}
}

// augment binds typed and fixture parameters into the per-function TypeMaps
// of one file before its calls are resolved.
func (t *lspLocalTier) augment(ext *cachedExtraction, perFunc PerFuncTypeMap) {
	if t == nil || ext == nil || ext.Result == nil {
		return
	}
	defs := ext.Result.Definitions
	for i := range defs {
		def := &defs[i]
		if def.Label != "Function" && def.Label != "Method" {
			continue
		}
		typed := t.paramTypes[def.QualifiedName]
		var fixtureParams []string
		if ext.Language == lang.Python && len(t.fixtureTypes) > 0 && (def.IsTest || isPytestFixture(def) || isPytestFile(def.FilePath)) {
			fixtureParams = untypedParams(headerSignature(def, nil, lang.Python), lang.Python)
		}
		if len(typed) == 0 && len(fixtureParams) == 0 {
			continue
		}
		tm := perFunc[def.QualifiedName]
		if tm == nil {
			tm = TypeMap{}
			perFunc[def.QualifiedName] = tm
		}
		for name, cls := range typed {
			if existing := tm[name]; existing != "" {
				continue
			}
			tm[name] = cls
			t.recordBinding(def.QualifiedName, name)
			t.mu.Lock()
			t.stats.paramBindings++
			t.mu.Unlock()
		}
		for _, name := range fixtureParams {
			if tm[name] != "" {
				continue
			}
			if cls := t.fixtureType(def.FilePath, name); cls != "" {
				tm[name] = cls
				t.recordBinding(def.QualifiedName, name)
				t.mu.Lock()
				t.stats.fixtureBindings++
				t.mu.Unlock()
			}
		}
	}
}

// fixtureType returns the class QN for a fixture parameter as seen from
// relPath: a fixture defined in the same file wins, then the directory's
// conftest.py, then the unambiguous project-wide name.
func (t *lspLocalTier) fixtureType(relPath, name string) string {
	if cls := t.fixtureTypesByFile[relPath][name]; cls != "" {
		return cls
	}
	dir := relPath
	for {
		i := strings.LastIndexAny(dir, "/\\")
		if i < 0 {
			if cls := t.fixtureTypesByFile["conftest.py"][name]; cls != "" {
				return cls
			}
			break
		}
		dir = dir[:i]
		if cls := t.fixtureTypesByFile[dir+"/conftest.py"][name]; cls != "" {
			return cls
		}
	}
	return t.fixtureTypes[name]
}

func (t *lspLocalTier) recordBinding(callerQN, name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.bindings[callerQN] == nil {
		t.bindings[callerQN] = map[string]bool{}
	}
	t.bindings[callerQN][name] = true
}

// usedBinding reports whether the receiver root of calleeName in callerQN
// was typed by this tier (so the resulting edge is tagged resolver_tier).
func (t *lspLocalTier) usedBinding(callerQN, calleeName string) bool {
	if t == nil {
		return false
	}
	root := calleeName
	if i := strings.IndexAny(root, ".("); i >= 0 {
		root = root[:i]
	}
	root = strings.TrimSpace(root)
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bindings[callerQN][root]
}

// resolveInherited resolves `recv.method` / `self.method` where method is
// defined on a base class of the receiver's type rather than the type
// itself. Strategy "lsp_local_inherited".
func (t *lspLocalTier) resolveInherited(calleeName, callerQN string, typeMap TypeMap, registry *FunctionRegistry) (ResolutionResult, bool) {
	if t == nil || len(t.classBases) == 0 || registry == nil {
		return ResolutionResult{}, false
	}
	parts := strings.Split(calleeName, ".")
	if len(parts) != 2 || strings.ContainsAny(parts[0], "()") {
		return ResolutionResult{}, false
	}
	root, method := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	var classQN string
	if root == "self" || root == "cls" {
		classQN = extractClassFromMethodQN(callerQN)
	} else {
		classQN = typeMap[root]
	}
	if classQN == "" || !registry.IsClassLike(classQN) {
		return ResolutionResult{}, false
	}
	seen := map[string]bool{classQN: true}
	queue := append([]string(nil), t.classBases[classQN]...)
	for depth := 0; len(queue) > 0 && depth < 64; depth++ {
		base := queue[0]
		queue = queue[1:]
		if seen[base] {
			continue
		}
		seen[base] = true
		if candidate := base + "." + method; registry.Exists(candidate) {
			t.mu.Lock()
			t.stats.inheritedResolutions++
			t.mu.Unlock()
			return ResolutionResult{QualifiedName: candidate, Strategy: "lsp_local_inherited", Confidence: 0.85, CandidateCount: 1}, true
		}
		queue = append(queue, t.classBases[base]...)
	}
	return ResolutionResult{}, false
}

func (t *lspLocalTier) logStats() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	slog.Info("resolver.lsp_local.applied",
		"param_bindings", t.stats.paramBindings,
		"fixture_bindings", t.stats.fixtureBindings,
		"inherited_resolutions", t.stats.inheritedResolutions)
}

// exprType types a returned/yielded expression: `name`, `Ctor(...)`,
// `recv.method(...)`, `Ctor(...).method(...)` chains.
func (t *lspLocalTier) exprType(p *Pipeline, expr string, tm TypeMap, moduleQN string, importMap map[string]string) string {
	expr = strings.TrimSpace(expr)
	segs := splitTopLevel(expr, '.')
	if len(segs) == 0 {
		return ""
	}
	root := strings.TrimSpace(segs[0])
	var current string
	if i := strings.IndexByte(root, '('); i >= 0 {
		current = resolveAsClass(strings.TrimSpace(root[:i]), p.registry, moduleQN, importMap)
	} else {
		current = tm[root]
	}
	for _, seg := range segs[1:] {
		if current == "" {
			return ""
		}
		seg = strings.TrimSpace(seg)
		if i := strings.IndexByte(seg, '('); i >= 0 {
			method := seg[:i]
			next := t.returnTypes[current+"."+method]
			if next == "" {
				next = p.returnTypes[current+"."+method]
			}
			if next == "" {
				// Inherited method: look on the bases.
				for _, base := range t.classBases[current] {
					if n := t.returnTypes[base+"."+method]; n != "" {
						next = n
						break
					}
					if n := p.returnTypes[base+"."+method]; n != "" {
						next = n
						break
					}
				}
			}
			current = next
			continue
		}
		current = p.fieldTypes[current+"."+seg]
	}
	if current != "" && p.registry != nil && !p.registry.IsClassLike(current) {
		return ""
	}
	return current
}

// ---- header/signature parsing -------------------------------------------

func readSourceLines(repoPath, relPath string) []string {
	data, err := os.ReadFile(filepath.Join(repoPath, relPath))
	if err != nil {
		return []string{}
	}
	return strings.Split(string(data), "\n")
}

// headerSignature returns the definition header text from the `def`/`fn`
// keyword through the body-opening `:`/`{`, joined across lines. Falls back
// to the extractor's Signature when source lines are unavailable.
func headerSignature(def *cbm.Definition, lines []string, language lang.Language) string {
	if lines == nil || def.StartLine <= 0 || def.StartLine > len(lines) {
		return def.Signature
	}
	var b strings.Builder
	depth := 0
	for i := def.StartLine - 1; i < len(lines) && i < def.StartLine+40; i++ {
		line := lines[i]
		if idx := strings.Index(line, "#"); idx >= 0 && language == lang.Python && !strings.ContainsAny(line[:idx], "\"'") {
			line = line[:idx]
		}
		b.WriteString(strings.TrimSpace(line))
		b.WriteByte(' ')
		for _, c := range line {
			switch c {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				depth--
			}
		}
		if depth <= 0 && strings.Contains(line, ")") {
			s := b.String()
			if language == lang.Python && strings.Contains(s, ":") {
				return s
			}
			if language == lang.Rust && (strings.Contains(s, "{") || strings.Contains(s, ";")) {
				return s
			}
		}
	}
	return b.String()
}

// parseSignature returns typed parameters (name -> cleaned type name) and the
// cleaned return type of a Python def or Rust fn header.
func parseSignature(sig string, language lang.Language) (params map[string]string, ret string) {
	params = map[string]string{}
	open := strings.IndexByte(sig, '(')
	if open < 0 {
		return params, ""
	}
	closeIdx := matchingParen(sig, open)
	if closeIdx < 0 {
		return params, ""
	}
	if arrow := strings.Index(sig[closeIdx:], "->"); arrow >= 0 {
		rest := sig[closeIdx+arrow+2:]
		end := len(rest)
		if language == lang.Python {
			if i := strings.LastIndexByte(rest, ':'); i >= 0 {
				end = i
			}
		} else if i := strings.IndexAny(rest, "{;"); i >= 0 {
			end = i
		}
		if language == lang.Rust {
			if i := strings.Index(rest[:end], " where "); i >= 0 {
				end = i
			}
		}
		ret = cleanTypeName(rest[:end], language, true)
	}
	for _, raw := range splitTopLevel(sig[open+1:closeIdx], ',') {
		p := strings.TrimSpace(raw)
		if p == "" || p == "/" || p == "*" || strings.HasPrefix(p, "*") {
			continue
		}
		colon := indexTopLevel(p, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(p[:colon])
		typ := p[colon+1:]
		if eq := indexTopLevel(typ, '='); eq >= 0 {
			typ = typ[:eq]
		}
		name = strings.TrimPrefix(strings.TrimPrefix(name, "mut "), "&")
		if name == "" || name == "self" || name == "cls" || strings.ContainsAny(name, " ()[]{}&") {
			continue
		}
		if cleaned := cleanTypeName(typ, language, false); cleaned != "" {
			params[name] = cleaned
		}
	}
	return params, ret
}

// untypedParams lists parameter names without an annotation (pytest fixture
// injection candidates).
func untypedParams(sig string, language lang.Language) []string {
	open := strings.IndexByte(sig, '(')
	if open < 0 {
		return nil
	}
	closeIdx := matchingParen(sig, open)
	if closeIdx < 0 {
		return nil
	}
	var out []string
	for _, raw := range splitTopLevel(sig[open+1:closeIdx], ',') {
		p := strings.TrimSpace(raw)
		if p == "" || p == "/" || strings.HasPrefix(p, "*") || indexTopLevel(p, ':') >= 0 {
			continue
		}
		if eq := indexTopLevel(p, '='); eq >= 0 {
			p = strings.TrimSpace(p[:eq])
		}
		if p == "self" || p == "cls" || !isIdentifier(p) {
			continue
		}
		out = append(out, p)
	}
	_ = language
	return out
}

// containerTypes are generic containers whose subscript does not name the
// receiver's own type; a parameter typed `list[Foo]` is not a Foo.
var containerTypes = map[string]bool{
	"list": true, "dict": true, "set": true, "frozenset": true, "tuple": true, "type": true,
	"List": true, "Dict": true, "Set": true, "FrozenSet": true, "Tuple": true, "Type": true,
	"Iterable": true, "Iterator": true, "Sequence": true, "Mapping": true, "MutableMapping": true,
	"Callable": true, "Generator": true, "AsyncIterator": true, "Awaitable": true, "Coroutine": true,
	"Vec": true, "HashMap": true, "BTreeMap": true, "HashSet": true, "BTreeSet": true, "VecDeque": true,
	"Result": false, // handled as a wrapper for return types
}

// wrapperTypes are transparent for receiver typing: Optional[Foo] behaves as
// Foo at a call site; &Foo, Box<Foo>, Arc<Foo> dispatch to Foo's methods.
var wrapperTypes = map[string]bool{
	"Optional": true, "Final": true, "ClassVar": true, "Annotated": true,
	"Box": true, "Arc": true, "Rc": true, "Option": true, "RefCell": true, "Mutex": true, "RwLock": true, "Cow": true, "Pin": true,
}

// cleanTypeName reduces an annotation to the class name the resolver should
// look up, or "" when the annotation does not name a single class.
func cleanTypeName(raw string, language lang.Language, isReturn bool) string {
	s := strings.TrimSpace(raw)
	for depth := 0; depth < 6 && s != ""; depth++ {
		s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "\"'"))
		var ok bool
		if language == lang.Rust {
			if s, ok = stripRustQualifiers(s); !ok {
				return ""
			}
		}
		if language == lang.Python {
			var done bool
			s, done, ok = stripPythonUnion(s)
			if !ok {
				return ""
			}
			if done {
				continue
			}
		}
		next, again, ok := unwrapGeneric(s, isReturn)
		if !ok {
			return ""
		}
		if !again {
			s = next
			break
		}
		s = next
	}
	return finalTypeName(s, language)
}

// stripRustQualifiers removes references, lifetimes, mutability, and
// dyn/impl from a Rust type. ok is false when a lifetime is all that remains.
func stripRustQualifiers(s string) (out string, ok bool) {
	s = strings.TrimPrefix(s, "&")
	if strings.HasPrefix(s, "'") {
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			return "", false
		}
		s = s[i+1:]
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "mut "), "dyn ")
	s = strings.TrimPrefix(s, "impl ")
	return strings.TrimSpace(s), true
}

// stripPythonUnion drops typing prefixes and reduces `X | None` to X.
// done reports that a union was collapsed and the loop should re-examine the
// result; ok is false when the union names more than one class.
func stripPythonUnion(s string) (out string, done, ok bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "typing."), "t.")
	if !strings.Contains(s, "|") {
		return s, false, true
	}
	kept := nonNoneParts(splitTopLevel(s, '|'))
	if len(kept) != 1 {
		return "", false, false
	}
	return kept[0], true, true
}

// unwrapGeneric handles one level of `Head[Inner]` / `Head<Inner>`. again
// reports that the loop should continue on the returned string; ok is false
// when the annotation cannot name a single class.
func unwrapGeneric(s string, isReturn bool) (out string, again, ok bool) {
	openIdx := strings.IndexAny(s, "[<")
	if openIdx < 0 {
		return s, false, true
	}
	head := strings.TrimSpace(s[:openIdx])
	inner := s[openIdx+1:]
	if i := strings.LastIndexAny(inner, "]>"); i >= 0 {
		inner = inner[:i]
	}
	headShort := shortTypeHead(head)
	switch {
	case wrapperTypes[headShort]:
		return strings.TrimSpace(splitTopLevel(inner, ',')[0]), true, true
	case headShort == "Union":
		kept := nonNoneParts(splitTopLevel(inner, ','))
		if len(kept) != 1 {
			return "", false, false
		}
		return kept[0], true, true
	case headShort == "Result" && isReturn:
		return strings.TrimSpace(splitTopLevel(inner, ',')[0]), true, true
	case containerTypes[headShort]:
		return "", false, false
	default:
		// A generic user type `Foo<T>` / `Foo[T]` dispatches on Foo.
		return head, true, true
	}
}

// shortTypeHead strips module/path qualifiers from a generic head.
func shortTypeHead(head string) string {
	if i := strings.LastIndex(head, "::"); i >= 0 {
		head = head[i+2:]
	}
	if i := strings.LastIndexByte(head, '.'); i >= 0 {
		head = head[i+1:]
	}
	return head
}

// nonNoneParts trims parts and drops empty and None members.
func nonNoneParts(parts []string) []string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "None" && part != "" {
			kept = append(kept, part)
		}
	}
	return kept
}

// finalTypeName validates the reduced name and strips Rust path prefixes the
// resolver cannot map.
func finalTypeName(s string, language lang.Language) string {
	s = strings.TrimSpace(s)
	switch s {
	case "", "None", "Self", "self", "Any", "object", "()", "!":
		return ""
	}
	if strings.ContainsAny(s, " ()[]{}<>|,*") {
		return ""
	}
	if language == lang.Rust {
		for _, prefix := range []string{"crate::", "self::", "super::"} {
			s = strings.TrimPrefix(s, prefix)
		}
	}
	return s
}

func splitBaseClasses(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "(")
	raw = strings.TrimSuffix(raw, ")")
	var out []string
	for _, part := range splitTopLevel(raw, ',') {
		part = strings.TrimSpace(part)
		if part == "" || strings.Contains(part, "=") || part == "object" {
			continue
		}
		if cleaned := cleanTypeName(part, lang.Python, false); cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return out
}

func isPytestFixture(def *cbm.Definition) bool {
	for _, d := range def.Decorators {
		if strings.Contains(d, "fixture") {
			return true
		}
	}
	return false
}

func isPytestFile(path string) bool {
	base := path
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	return base == "conftest.py" || strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
}

// fixtureResultExpr returns the expression of the first `return X` or
// `yield X` statement in the fixture body.
func fixtureResultExpr(def *cbm.Definition, lines []string) string {
	if def.StartLine <= 0 || def.EndLine > len(lines) {
		return ""
	}
	for i := def.StartLine; i < def.EndLine && i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		for _, kw := range []string{"return ", "yield "} {
			if strings.HasPrefix(s, kw) {
				expr := strings.TrimSpace(s[len(kw):])
				if idx := strings.Index(expr, " as "); idx >= 0 {
					expr = expr[:idx]
				}
				return expr
			}
		}
		// `with app.test_client() as client: yield client` shape.
		if strings.HasPrefix(s, "with ") && strings.Contains(s, " as ") && i+1 < len(lines) {
			asIdx := strings.Index(s, " as ")
			bound := strings.TrimSuffix(strings.TrimSpace(s[asIdx+4:]), ":")
			next := strings.TrimSpace(lines[i+1])
			if next == "yield "+bound || next == "return "+bound {
				return strings.TrimSpace(s[5:asIdx])
			}
		}
	}
	return ""
}

// ---- small lexical helpers -----------------------------------------------

func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

func indexTopLevel(s string, sep byte) int {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		default:
			if c == sep && depth == 0 {
				return i
			}
		}
	}
	return -1
}

func matchingParen(s string, open int) int {
	depth := 0
	var quote byte
	for i := open; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}
