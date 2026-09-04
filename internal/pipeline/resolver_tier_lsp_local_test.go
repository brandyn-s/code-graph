package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/cbm"
	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/lang"
	"github.com/brandyn-s/code-graph/internal/store"
)

func TestCleanTypeNameReducesAnnotationsToAClass(t *testing.T) {
	cases := []struct {
		raw      string
		language lang.Language
		isReturn bool
		want     string
	}{
		{`Flask`, lang.Python, false, "Flask"},
		{`"FlaskClient"`, lang.Python, true, "FlaskClient"},
		{`t.Optional[Blueprint]`, lang.Python, false, "Blueprint"},
		{`Blueprint | None`, lang.Python, false, "Blueprint"},
		{`typing.Union[Blueprint, None]`, lang.Python, false, "Blueprint"},
		{`list[Blueprint]`, lang.Python, false, ""},
		{`dict[str, Any]`, lang.Python, false, ""},
		{`Blueprint | Flask`, lang.Python, false, ""},
		{`None`, lang.Python, true, ""},
		{`&mut Searcher`, lang.Rust, false, "Searcher"},
		{`&'a Matcher`, lang.Rust, false, "Matcher"},
		{`Option<Box<dyn Sink>>`, lang.Rust, false, "Sink"},
		{`Arc<Config>`, lang.Rust, false, "Config"},
		{`Result<Searcher, Error>`, lang.Rust, true, "Searcher"},
		{`Vec<Glob>`, lang.Rust, false, ""},
		{`impl AsRef<Path>`, lang.Rust, false, "AsRef"},
		{`crate::searcher::SearcherBuilder`, lang.Rust, false, "searcher::SearcherBuilder"},
		{`Self`, lang.Rust, true, ""},
	}
	for _, tc := range cases {
		if got := cleanTypeName(tc.raw, tc.language, tc.isReturn); got != tc.want {
			t.Errorf("cleanTypeName(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestParseSignatureTypedAndUntypedParameters(t *testing.T) {
	sig := `def register(self, blueprint: "Blueprint", app: Flask, opts: dict[str, Any] = None, *args, name: t.Optional[str] = None, **kw) -> "Scaffold":`
	params, ret := parseSignature(sig, lang.Python)
	if params["blueprint"] != "Blueprint" || params["app"] != "Flask" {
		t.Errorf("python params = %v", params)
	}
	if _, ok := params["opts"]; ok {
		t.Errorf("container-typed parameter must not be bound: %v", params)
	}
	if params["name"] != "str" {
		t.Errorf("Optional[str] should reduce to str: %v", params)
	}
	if ret != "Scaffold" {
		t.Errorf("python return = %q", ret)
	}
	untyped := untypedParams(`def test_x(client, app: Flask, *, monkeypatch, tmp_path=None):`, lang.Python)
	if len(untyped) != 3 || untyped[0] != "client" || untyped[1] != "monkeypatch" || untyped[2] != "tmp_path" {
		t.Errorf("untyped = %v", untyped)
	}

	rs := `pub fn search_path<P: AsRef<Path>>(&mut self, matcher: &RegexMatcher, path: P, sink: Box<dyn Sink>) -> Result<Searcher, io::Error> where P: Clone {`
	params, ret = parseSignature(rs, lang.Rust)
	if params["matcher"] != "RegexMatcher" || params["sink"] != "Sink" || params["path"] != "P" {
		t.Errorf("rust params = %v", params)
	}
	if ret != "Searcher" {
		t.Errorf("rust return = %q", ret)
	}
}

func TestSplitBaseClassesAndFixtureResultExpr(t *testing.T) {
	if got := splitBaseClasses(`(Base, metaclass=ABCMeta, Generic[T])`); len(got) != 2 || got[0] != "Base" || got[1] != "Generic" {
		t.Errorf("splitBaseClasses = %v", got)
	}
	lines := []string{
		"@pytest.fixture",
		"def client(app):",
		"    with app.test_client() as c:",
		"        yield c",
		"",
		"@pytest.fixture",
		"def app():",
		"    app = Flask('x')",
		"    return app",
	}
	if got := fixtureResultExpr(&cbm.Definition{StartLine: 2, EndLine: 4}, lines); got != "app.test_client()" {
		t.Errorf("with/yield fixture expr = %q", got)
	}
	if got := fixtureResultExpr(&cbm.Definition{StartLine: 7, EndLine: 9}, lines); got != "app" {
		t.Errorf("return fixture expr = %q", got)
	}
}

// End to end: without the tier a fixture-typed receiver and an inherited
// method stay unresolved; with CODE_GRAPH_RESOLVER_TIER=lsp_local they
// resolve and the edges carry resolver_tier=lsp_local.
func TestLSPLocalTierResolvesFixtureAnnotatedAndInheritedReceivers(t *testing.T) {
	src := lspLocalFixtureSources
	run := func(t *testing.T, tier string) map[string]map[string]any {
		t.Helper()
		t.Setenv("CODE_GRAPH_EXTRACT_ISOLATION", "off")
		t.Setenv("CODE_GRAPH_RESOLVER_TIER", tier)
		repo := t.TempDir()
		for rel, body := range src {
			path := filepath.Join(repo, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		st, err := store.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		p := New(context.Background(), st, repo, discover.ModeFull)
		if err := p.Run(); err != nil {
			t.Fatalf("run: %v", err)
		}
		edges, err := st.FindEdgesByType(p.ProjectName, store.EdgeCalls)
		if err != nil {
			t.Fatal(err)
		}
		name := map[int64]string{}
		for _, label := range []string{"Function", "Method"} {
			nodes, err := st.FindNodesByLabel(p.ProjectName, label)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range nodes {
				name[n.ID] = n.QualifiedName
			}
		}
		out := map[string]map[string]any{}
		for _, e := range edges {
			src, tgt := name[e.SourceID], name[e.TargetID]
			if src == "" || tgt == "" {
				continue
			}
			out[trimProject(src, p.ProjectName)+" -> "+trimProject(tgt, p.ProjectName)] = e.Properties
		}
		return out
	}

	want := []string{
		"tests.test_app.test_register -> pkg.app.Flask.register_blueprint", // fixture-typed receiver
		"tests.test_app.test_register -> pkg.app.FlaskClient.open",         // fixture chain: app.test_client() -> FlaskClient
		"tests.test_app.test_register -> pkg.base.Recorder.record",         // inherited method through fixture type
		"pkg.app.Blueprint.register -> pkg.base.Scaffold.add_url_rule",     // annotated param + inherited method
		"pkg.base.Scaffold.route -> pkg.base.Scaffold.add_url_rule",        // self-method, resolved by the registry tier already
	}

	t.Run("registry tier alone leaves the receivers speculative", func(t *testing.T) {
		assertTierOff(t, run(t, ""), want)
	})
	t.Run("lsp_local resolves and tags the receivers", func(t *testing.T) {
		assertTierOn(t, run(t, "lsp_local"), want)
	})
}

// assertTierOff checks the baseline: without receiver typing the fixture and
// inherited-method edges must not be confidently resolved, and no edge may
// carry the tier tag.
func assertTierOff(t *testing.T, before map[string]map[string]any, want []string) {
	t.Helper()
	for _, key := range want[:4] {
		if props, ok := before[key]; ok && props["confidence_band"] != "speculative-janusian" {
			t.Errorf("registry tier confidently resolved %s without receiver typing (the fixture would not measure the tier): %v", key, props)
		}
		if props, ok := before[key]; ok && props["resolver_tier"] != nil {
			t.Errorf("resolver_tier set while the tier is off: %v", props)
		}
	}
	if _, ok := before[want[4]]; !ok {
		t.Errorf("registry tier lost the self-method edge %s", want[4])
	}
}

// assertTierOn checks that every wanted edge resolves with the tier, carries
// the tag (except the self-method edge the registry already owns), and that
// no decoy edge is attributed to the tier.
func assertTierOn(t *testing.T, after map[string]map[string]any, want []string) {
	t.Helper()
	for _, key := range want {
		props, ok := after[key]
		if !ok {
			keys := make([]string, 0, len(after))
			for k := range after {
				keys = append(keys, k)
			}
			t.Errorf("lsp_local did not resolve %s; edges: %v", key, keys)
			continue
		}
		if key != want[4] && props["resolver_tier"] != "lsp_local" {
			t.Errorf("%s lacks resolver_tier=lsp_local: %v", key, props)
		}
		if props["confidence_band"] == "speculative-janusian" {
			t.Errorf("%s still speculative with the tier on: %v", key, props)
		}
	}
	if props := after[want[4]]; props["resolver_tier"] != nil {
		t.Errorf("self-method edge must not be attributed to the tier: %v", props)
	}
	if props := after["pkg.app.Blueprint.register -> pkg.base.Scaffold.add_url_rule"]; props != nil && props["resolver_rule"] != ResolverRuleReceiverQualified {
		t.Errorf("inherited resolution rule = %v, want %s", props["resolver_rule"], ResolverRuleReceiverQualified)
	}
	// Untyped parameters of a plain helper in a test file ARE fixture
	// candidates (pytest injects into test functions only, but helpers in
	// test files commonly receive the fixture value); a helper in a non-test
	// module must not be typed. Decoy edges to the wrong class must never
	// carry the tier tag.
	for key, props := range after {
		if props["resolver_tier"] == "lsp_local" && strings.Contains(key, "pkg.decoy.") {
			t.Errorf("tier attributed an edge to the decoy class: %s %v", key, props)
		}
	}
}

func trimProject(qn, project string) string {
	if len(qn) > len(project)+1 && qn[:len(project)+1] == project+"." {
		return qn[len(project)+1:]
	}
	return qn
}

// lspLocalFixtureSources is the small Python project the end-to-end tier test
// indexes: a decoy class, a base class, an app with annotated methods, and
// pytest fixtures wiring them together.
var lspLocalFixtureSources = map[string]string{
	"pkg/__init__.py": "",
	"pkg/decoy.py": `class Decoy:
    def register_blueprint(self, bp):
        return bp

    def open(self, path):
        return path

    def record(self, event):
        return event

    def add_url_rule(self, rule):
        return rule

    def route(self, rule):
        return rule

    def test_client(self):
        return self
`,
	"pkg/base.py": `class Scaffold:
    def add_url_rule(self, rule):
        return rule

    def route(self, rule):
        return self.add_url_rule(rule)


class Recorder:
    def record(self, event):
        return event
`,
	"pkg/app.py": `from .base import Scaffold, Recorder


class Flask(Scaffold):
    def __init__(self, name):
        self.name = name

    def test_client(self) -> "FlaskClient":
        return FlaskClient(self)

    def register_blueprint(self, bp: "Blueprint"):
        return bp.register(self)


class FlaskClient(Recorder):
    def __init__(self, app):
        self.app = app

    def open(self, path):
        return self.record(path)


class Blueprint:
    def register(self, app: Flask):
        return app.add_url_rule("/bp")
`,
	"tests/conftest.py": `import pytest
from pkg.app import Flask


@pytest.fixture
def app():
    app = Flask("test")
    return app


@pytest.fixture
def client(app):
    return app.test_client()
`,
	"tests/test_app.py": `from pkg.app import Blueprint


def test_register(app, client):
    app.register_blueprint(Blueprint())
    client.open("/")
    client.record("x")


def helper(app):
    app.route("/plain")
`,
}
