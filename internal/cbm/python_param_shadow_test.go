package cbm

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// Upstream 95689b5c: a bare Python call whose callee is bound as a parameter
// of an enclosing function (or lambda, or an outer function via closure) is
// flagged so the resolver never binds it to a same-named module-level symbol.
func TestPythonBareCallBoundToParameterIsFlagged(t *testing.T) {
	src := `
def cb():
    return 1

def run_with(cb):
    return cb()

def outer(run, *args, **kwargs):
    def inner():
        return run()
    return inner() + args() + kwargs()

def typed(handler: int = 3):
    return handler()

f = lambda cb: cb()

def plain():
    return cb()
`
	res, err := ExtractFile([]byte(src), lang.Python, "shadow", "app.py")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	flagged := map[string]bool{}
	for _, c := range res.Calls {
		key := c.EnclosingFuncQN + "->" + c.CalleeName
		if c.CalleeIsLocallyBound {
			flagged[key] = true
		} else if _, seen := flagged[key]; !seen {
			flagged[key] = false
		}
	}
	want := map[string]bool{
		"shadow.app.run_with->cb":   true,
		"shadow.app.inner->run":     true, // nested defs flatten to module.name
		"shadow.app.outer->args":    true,
		"shadow.app.outer->kwargs":  true,
		"shadow.app.typed->handler": true,
		"shadow.app.plain->cb":      false,
		"shadow.app.outer->inner":   false,
	}
	for key, wantFlag := range want {
		got, ok := flagged[key]
		if !ok {
			t.Errorf("call %s not extracted; have %v", key, flagged)
			continue
		}
		if got != wantFlag {
			t.Errorf("call %s locally_bound=%v, want %v", key, got, wantFlag)
		}
	}
}
