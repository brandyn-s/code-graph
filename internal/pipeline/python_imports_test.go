package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// CG-3 (2026-05-06) — Python IMPORTS resolver completeness.
//
// Background: 2026-05-06 baselines (`bench/accuracy/baselines/
// 2026-05-06-flask-adversarial-report.md`) show flask IMPORTS F1 of
// 0.038 — basically broken. The same resolver hits 0.979 on
// mcp-servers, so the resolver works on the canonical case but misses
// Python idioms that flask uses heavily:
//   1. Relative imports: `from . import foo`, `from .bar import baz`
//   2. Package re-exports through `__init__.py`
//   3. Aliased imports: `from foo import bar as b`
//
// These tests pin the post-fix behavior. Tests use Pipeline.Run() against
// a temp Python package on disk so the entire IMPORTS pass exercises.

// helper: get all IMPORTS edges from moduleQN in the store
func importsFrom(t *testing.T, s *store.Store, projectName, sourceQN string) []*store.Edge {
	t.Helper()
	src, _ := s.FindNodeByQN(projectName, sourceQN)
	if src == nil {
		return nil
	}
	all, _ := s.FindEdgesBySource(src.ID)
	var out []*store.Edge
	for _, e := range all {
		if e.Type == "IMPORTS" {
			out = append(out, e)
		}
	}
	return out
}

// importTargetQN returns the QN of an IMPORTS edge's target node, or
// empty if not resolvable.
func importTargetQN(s *store.Store, e *store.Edge) string {
	tgt, _ := s.FindNodeByID(e.TargetID)
	if tgt == nil {
		return ""
	}
	return tgt.QualifiedName
}

// TestPythonImports_RelativeDotImport — `from . import sibling` should
// produce an IMPORTS edge from the importing module to the sibling
// module within the same package.
func TestPythonImports_RelativeDotImport(t *testing.T) {
	dir, err := os.MkdirTemp("", "cgm-py-relimp-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	pkgDir := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pkgDir, "__init__.py"), "")
	writeFile(t, filepath.Join(pkgDir, "sibling.py"), `
def hello():
    return "hi"
`)
	writeFile(t, filepath.Join(pkgDir, "main.py"), `
from . import sibling

def go():
    return sibling.hello()
`)

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}

	mainQN := p.ProjectName + ".pkg.main"
	siblingQN := p.ProjectName + ".pkg.sibling"

	edges := importsFrom(t, s, p.ProjectName, mainQN)
	found := false
	for _, e := range edges {
		if importTargetQN(s, e) == siblingQN {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected IMPORTS edge from %s to %s; got %d edges:",
			mainQN, siblingQN, len(edges))
		for _, e := range edges {
			t.Logf("  -> %s", importTargetQN(s, e))
		}
	}
}

// TestPythonImports_RelativeFromDotSubmodule — `from .sub import x`
// should produce an IMPORTS edge to the submodule (or to x within it).
// The mcp-servers oracle measures module-level IMPORTS, so an edge to
// either the submodule QN OR a node inside that submodule satisfies
// the resolution-correctness criterion (the resolver can't always
// strip the final segment without label-checking, and the existing
// absolute-path test shows the same loose-acceptance pattern).
func TestPythonImports_RelativeFromDotSubmodule(t *testing.T) {
	dir, err := os.MkdirTemp("", "cgm-py-relfrom-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	pkgDir := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pkgDir, "__init__.py"), "")
	writeFile(t, filepath.Join(pkgDir, "sub.py"), `
def helper():
    return 42
`)
	writeFile(t, filepath.Join(pkgDir, "consumer.py"), `
from .sub import helper

def use():
    return helper()
`)

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}

	consumerQN := p.ProjectName + ".pkg.consumer"
	subQN := p.ProjectName + ".pkg.sub"
	helperQN := p.ProjectName + ".pkg.sub.helper"

	edges := importsFrom(t, s, p.ProjectName, consumerQN)
	found := false
	for _, e := range edges {
		tgt := importTargetQN(s, e)
		// Accept either module-level (subQN) or symbol-level
		// (helperQN) — both indicate successful relative-import
		// resolution. mcp-servers F1=0.979 oracle prefers module-
		// granularity but the resolver's first-found-wins behavior
		// can land on either depending on which exact-match hits
		// first; both are correct relative-import handling.
		if tgt == subQN || tgt == helperQN {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected IMPORTS edge from %s to %s or %s; got %d edges:",
			consumerQN, subQN, helperQN, len(edges))
		for _, e := range edges {
			t.Logf("  -> %s", importTargetQN(s, e))
		}
	}
}

// TestResolvePythonRelativeImport_Cases — unit tests for the
// dot-counting logic, isolated from pipeline machinery. The C
// extractor's sprintf("%s.%s", mod_path, name) collides the
// relative_import dots with the separator, producing different
// raw-leading-dot counts for "all-dots" vs "dots+module" cases.
func TestResolvePythonRelativeImport_Cases(t *testing.T) {
	cases := []struct {
		desc      string
		targetQN  string
		moduleQN  string
		localName string
		want      string
	}{
		{
			desc:      "from . import sibling (raw=2, sem=1, all-dots)",
			targetQN:  "..sibling",
			moduleQN:  "proj.pkg.main",
			localName: "sibling",
			want:      "proj.pkg.sibling",
		},
		{
			desc:      "from .sub import helper (raw=1, sem=1, dots+module)",
			targetQN:  ".sub.helper",
			moduleQN:  "proj.pkg.consumer",
			localName: "helper",
			want:      "proj.pkg.sub.helper",
		},
		{
			desc:      "from .. import top (raw=3, sem=2, all-dots)",
			targetQN:  "...top",
			moduleQN:  "proj.a.b.c",
			localName: "top",
			want:      "proj.a.top",
		},
		{
			desc:      "from ..top import x (raw=2, sem=2, dots+module)",
			targetQN:  "..top.x",
			moduleQN:  "proj.a.b.c",
			localName: "x",
			want:      "proj.a.top.x",
		},
		{
			desc:      "absolute import — no rewrite",
			targetQN:  "lib.util",
			moduleQN:  "proj.app",
			localName: "util",
			want:      "lib.util",
		},
		{
			desc:      "too many dots for moduleQN — give up, keep original",
			targetQN:  "....foo",
			moduleQN:  "proj.a",
			localName: "foo",
			want:      "....foo",
		},
	}
	for _, c := range cases {
		got := resolvePythonRelativeImport(c.targetQN, c.moduleQN, c.localName)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.desc, got, c.want)
		}
	}
}

// TestPythonImports_AbsoluteImport — control case: a normal absolute
// import should produce an edge today (this tests we haven't regressed
// the existing canonical case while extending coverage).
func TestPythonImports_AbsoluteImport(t *testing.T) {
	dir, err := os.MkdirTemp("", "cgm-py-absimp-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	writeFile(t, filepath.Join(dir, "lib.py"), `
def util():
    return 1
`)
	writeFile(t, filepath.Join(dir, "app.py"), `
from lib import util

def run():
    return util()
`)

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}

	appQN := p.ProjectName + ".app"
	libQN := p.ProjectName + ".lib"

	edges := importsFrom(t, s, p.ProjectName, appQN)
	found := false
	for _, e := range edges {
		if importTargetQN(s, e) == libQN {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected IMPORTS edge from %s to %s; got %d edges:",
			appQN, libQN, len(edges))
		for _, e := range edges {
			t.Logf("  -> %s", importTargetQN(s, e))
		}
	}
}

// TestPythonImports_RelativeImport_BareNameCallResolves — Phase B
// (2026-05-14, grade-lift roadmap). When a module does
// `from .util import default_hooks` and then calls bare-name
// `default_hooks()`, a CALLS edge must be emitted to the imported
// function.
//
// Pre-fix bug: passImports applied resolvePythonRelativeImport only
// when building IMPORTS edges. The raw `.util.default_hooks` path
// stayed in p.importMaps and p.importBindings, where the resolver
// consulted it during passCalls. applyImportBindingFilter saw the
// leading-dot target, found no matching candidate, and dropped the
// call as external. Per requests-adversarial baseline analysis, this
// was responsible for 5 of 10 sampled FN edges (50% of the recall
// gap on requests, per knowledge-base plan #523).
//
// Post-fix: normalizePythonRelativeImports rewrites the maps before
// passCalls runs, so the resolver sees absolute QNs and the
// import-binding filter matches the candidate correctly.
func TestPythonImports_RelativeImport_BareNameCallResolves(t *testing.T) {
	dir, err := os.MkdirTemp("", "cgm-py-relimp-call-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	pkgDir := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pkgDir, "__init__.py"), "")
	writeFile(t, filepath.Join(pkgDir, "util.py"), `
def default_hooks():
    return {}
`)
	writeFile(t, filepath.Join(pkgDir, "model.py"), `
from .util import default_hooks


class Model:
    def __init__(self):
        self.hooks = default_hooks()


def top_caller():
    return default_hooks()
`)

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	p := New(context.Background(), s, dir, discover.ModeFull)
	if err := p.Run(); err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}

	targetQN := p.ProjectName + ".pkg.util.default_hooks"
	target, _ := s.FindNodeByQN(p.ProjectName, targetQN)
	if target == nil {
		t.Fatalf("util.default_hooks node missing")
	}

	// Both callers (the __init__ method AND the top-level function)
	// must emit CALLS edges to default_hooks.
	for _, callerQN := range []string{
		p.ProjectName + ".pkg.model.Model.__init__",
		p.ProjectName + ".pkg.model.top_caller",
	} {
		caller, _ := s.FindNodeByQN(p.ProjectName, callerQN)
		if caller == nil {
			t.Errorf("caller %s missing from store", callerQN)
			continue
		}
		edges, _ := s.FindEdgesBySourceAndType(caller.ID, "CALLS")
		found := false
		for _, e := range edges {
			if e.TargetID == target.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected CALLS edge %s -> %s; got %d CALLS:",
				callerQN, targetQN, len(edges))
			for _, e := range edges {
				if tgt, _ := s.FindNodeByID(e.TargetID); tgt != nil {
					t.Logf("  -> %s", tgt.QualifiedName)
				}
			}
		}
	}
}
