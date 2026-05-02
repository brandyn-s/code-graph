package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// parseToVisitor is a test helper: parses Go source and walks it with the
// production visitor. Returns the visitor (with its captured edges + defs).
func parseToVisitor(t *testing.T, source string) *visitor {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	v := &visitor{
		fset:    fset,
		project: "proj",
		fileQN:  "proj.test",
		fileRel: "test.go",
	}
	ast.Walk(v, file)
	return v
}

// findEdge returns the first edge whose callee is exactly `to`, or empty Edge.
func findEdge(edges []Edge, to string) Edge {
	for _, e := range edges {
		if e.ToQN == to {
			return e
		}
	}
	return Edge{}
}

// TestY5_SelfReceiverMethodCall — the regression test for follow-up #5.
// Inside `func (p *Pipeline) Outer()`, a call `p.Inner()` must emit
// callee form `Pipeline.Inner` (resolved by receiver type), NOT
// `p.Inner` (which the Python wrapper would drop as `calls_path_dropped`).
func TestY5_SelfReceiverMethodCall(t *testing.T) {
	src := `package main

type Pipeline struct{}

func (p *Pipeline) Inner() {}

func (p *Pipeline) Outer() {
	p.Inner()
}
`
	v := parseToVisitor(t, src)
	want := "Pipeline.Inner"
	if got := findEdge(v.edges, want); got.ToQN == "" {
		var have []string
		for _, e := range v.edges {
			have = append(have, e.ToQN)
		}
		t.Errorf("expected callee %q (Y.5 receiver-substituted form); got %v", want, have)
	}
}

// TestY5_NonSelfReceiverNotSubstituted — when the receiver identifier
// does NOT match the enclosing method's receiver, the substitution must
// NOT fire. `q.Inner()` inside `(p *Pipeline) Outer()` should emit
// `q.Inner` (unchanged), not `Pipeline.Inner`.
func TestY5_NonSelfReceiverNotSubstituted(t *testing.T) {
	src := `package main

type Pipeline struct{}

func (p *Pipeline) Outer() {
	q.Inner()
}
`
	v := parseToVisitor(t, src)
	if got := findEdge(v.edges, "q.Inner"); got.ToQN == "" {
		t.Errorf("expected unchanged callee q.Inner")
	}
	if got := findEdge(v.edges, "Pipeline.Inner"); got.ToQN != "" {
		t.Errorf("Y.5 substitution must not fire when recv name does not match enclosing receiver")
	}
}

// TestY5_FreeFunctionNotAffected — Y.5 only applies inside methods. A
// call inside a free function must not trigger receiver substitution.
func TestY5_FreeFunctionNotAffected(t *testing.T) {
	src := `package main

func TopLevel() {
	x.Method()
}
`
	v := parseToVisitor(t, src)
	if got := findEdge(v.edges, "x.Method"); got.ToQN == "" {
		t.Errorf("expected unchanged callee x.Method (no receiver context in free function)")
	}
}

// TestY5_DeepSelectorNotSubstituted — `p.field.method()` is a deep
// selector. extractCallee returns just `method`, so Y.5's prefix check
// `recv.method` doesn't apply. The bare-name case is handled by the
// wrapper's bare-name path (and the ambiguity-aware bare_to_qns map).
func TestY5_DeepSelectorNotSubstituted(t *testing.T) {
	src := `package main

type Pipeline struct{}

func (p *Pipeline) Outer() {
	p.field.method()
}
`
	v := parseToVisitor(t, src)
	if got := findEdge(v.edges, "method"); got.ToQN == "" {
		t.Errorf("expected bare callee `method` for deep selector")
	}
	// And Pipeline.method must NOT appear — the receiver substitution
	// must only fire on direct `recv.method` form, not deep selectors.
	if got := findEdge(v.edges, "Pipeline.method"); got.ToQN != "" {
		t.Errorf("Y.5 must not fire on deep selectors")
	}
}

// TestY5_AnonymousReceiverDoesNotSubstitute — `func (*Pipeline) Method()`
// has no receiver name. recvName is empty, so Y.5 must not fire on calls
// that look like `<something>.Method`.
func TestY5_AnonymousReceiverDoesNotSubstitute(t *testing.T) {
	src := `package main

type Pipeline struct{}

func (*Pipeline) Outer() {
	x.Inner()
}
`
	v := parseToVisitor(t, src)
	// Without a receiver name, x.Inner stays as x.Inner.
	if got := findEdge(v.edges, "x.Inner"); got.ToQN == "" {
		t.Errorf("expected unchanged callee x.Inner with anonymous receiver")
	}
	if strings.Contains(strings.Join(callees(v.edges), ","), "Pipeline.Inner") {
		t.Errorf("Y.5 must not fire when receiver name is anonymous")
	}
}

func callees(edges []Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.ToQN)
	}
	return out
}

// TestY6_FreeFunctionParameterMethodCall — the regression test for Y.6.
// Inside `func writeCommands(cmd *Command, ...)`, a call `cmd.Method()` must
// emit callee form `Command.Method` (resolved by parameter type), NOT
// `cmd.Method` (which the wrapper would drop as `calls_path_dropped`).
// This is the parameter-receiver mirror of Y.5's method-receiver fix.
func TestY6_FreeFunctionParameterMethodCall(t *testing.T) {
	src := `package main

type Command struct{}

func (c *Command) Name() string { return "" }

func writeCommands(cmd *Command) {
	cmd.Name()
}
`
	v := parseToVisitor(t, src)
	want := "Command.Name"
	if got := findEdge(v.edges, want); got.ToQN == "" {
		var have []string
		for _, e := range v.edges {
			have = append(have, e.ToQN)
		}
		t.Errorf("expected callee %q (Y.6 parameter-substituted form); got %v", want, have)
	}
}

// TestY6_MultipleParamsSameType — `func foo(a, b *Command)` should resolve
// both a.Method() and b.Method() to Command.Method.
func TestY6_MultipleParamsSameType(t *testing.T) {
	src := `package main

type Command struct{}

func (c *Command) Run() {}

func twoCmds(a, b *Command) {
	a.Run()
	b.Run()
}
`
	v := parseToVisitor(t, src)
	count := 0
	for _, e := range v.edges {
		if e.ToQN == "Command.Run" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 Command.Run edges (one per param), got %d", count)
	}
}

// TestY6_MethodReceiverTakesPriority — inside a method `func (p *Pipeline) X(cmd *Command)`,
// a call `p.Inner()` should resolve via Y.5 (recv name), and `cmd.Run()` via Y.6
// (param). Both substitutions coexist; Y.5 runs first.
func TestY6_MethodReceiverTakesPriority(t *testing.T) {
	src := `package main

type Pipeline struct{}
type Command struct{}

func (p *Pipeline) Inner() {}
func (c *Command) Run() {}

func (p *Pipeline) Outer(cmd *Command) {
	p.Inner()
	cmd.Run()
}
`
	v := parseToVisitor(t, src)
	if got := findEdge(v.edges, "Pipeline.Inner"); got.ToQN == "" {
		t.Errorf("expected Pipeline.Inner from Y.5 self-receiver substitution")
	}
	if got := findEdge(v.edges, "Command.Run"); got.ToQN == "" {
		t.Errorf("expected Command.Run from Y.6 parameter substitution")
	}
}

// TestY6_NonStructParamTypesNotSubstituted — interface types, slices, maps,
// function types should NOT trigger Y.6 substitution. Calling methods on
// these doesn't resolve to a single struct type.
func TestY6_NonStructParamTypesNotSubstituted(t *testing.T) {
	src := `package main

import "io"

func writeStuff(w io.Writer, items []int, cb func() error) {
	w.Write(nil)
	cb()
}
`
	v := parseToVisitor(t, src)
	// Y.6 should NOT have substituted any of these. w.Write should stay
	// as "w.Write" (interface, can't resolve to a single type).
	if got := findEdge(v.edges, "Writer.Write"); got.ToQN != "" {
		t.Errorf("Y.6 must not substitute on interface-typed parameter (io.Writer)")
	}
	if got := findEdge(v.edges, "w.Write"); got.ToQN == "" {
		t.Errorf("expected unchanged callee w.Write for interface param")
	}
}

// TestY6_NonStructParamUnchanged — `cmd Command` (value, not pointer) should
// still resolve via Y.6 because `paramTypeName` handles both `*Command` and
// `Command`. Verify the value-receiver case.
func TestY6_ValueParamSubstituted(t *testing.T) {
	src := `package main

type Command struct{}

func (c Command) Name() string { return "" }

func showName(cmd Command) {
	cmd.Name()
}
`
	v := parseToVisitor(t, src)
	if got := findEdge(v.edges, "Command.Name"); got.ToQN == "" {
		t.Errorf("expected Y.6 substitution on value-typed parameter")
	}
}

// TestY6_DeepSelectorOnParam — `cmd.Inner.Name()` is a deep selector; the
// extractCallee returns just "Name" (bare). Y.6 must NOT fire on this case
// because it operates on `<recv>.<method>` two-segment form, not bare names.
func TestY6_DeepSelectorOnParam(t *testing.T) {
	src := `package main

type Command struct{}
type Inner struct{}

func (i *Inner) Name() string { return "" }

func deep(cmd *Command) {
	cmd.Inner.Name()
}
`
	v := parseToVisitor(t, src)
	// Bare-name "Name" is what extractCallee produces. Y.6 doesn't apply.
	if got := findEdge(v.edges, "Name"); got.ToQN == "" {
		t.Errorf("expected bare callee Name for deep selector on parameter")
	}
	// Command.Name must NOT appear — that would be wrong (cmd.Inner.Name
	// is calling Inner.Name, not Command.Name).
	if got := findEdge(v.edges, "Command.Name"); got.ToQN != "" {
		t.Errorf("Y.6 must not fire on deep selector cmd.Inner.Name")
	}
}
