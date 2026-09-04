package cbm

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// Smoke coverage for the grammars vendored from upstream codebase-memory-mcp's
// manifest in 0.9.1. Each must parse a representative snippet, produce the
// module definition, and (where the spec declares function types) at least one
// function definition.
func TestVendoredSmallGrammarsParse(t *testing.T) {
	cases := []struct {
		lang      lang.Language
		file      string
		src       string
		wantFuncs bool
	}{
		{lang.Lua, "main.lua", "local M = {}\n\nfunction M.greet(name)\n  return \"hi \" .. name\nend\n\nlocal function helper()\n  return os.getenv(\"HOME\")\nend\n\nreturn M\n", true},
		{lang.Vue, "App.vue", "<template>\n  <div>{{ msg }}</div>\n</template>\n\n<script>\nexport default { data() { return { msg: 'hi' } } }\n</script>\n", false},
		{lang.Svelte, "App.svelte", "<script>\n  let count = 0;\n</script>\n\n{#if count > 0}\n  <p>{count}</p>\n{/if}\n", false},
		{lang.GraphQL, "schema.graphql", "type Query {\n  user(id: ID!): User\n}\n\ntype User {\n  id: ID!\n  name: String\n}\n\nenum Role { ADMIN USER }\n", false},
		{lang.GoMod, "go.mod", "module example.com/app\n\ngo 1.22\n\nrequire (\n\tgithub.com/foo/bar v1.2.3\n)\n\nreplace github.com/foo/bar => ../bar\n", false},
		{lang.Erlang, "app.erl", "-module(app).\n-export([start/0]).\n\nstart() ->\n    io:format(\"hi~n\"),\n    helper(1).\n\nhelper(X) ->\n    case X of\n        1 -> one;\n        _ -> other\n    end.\n", true},
		{lang.Clojure, "core.clj", "(ns app.core)\n\n(defn greet [name]\n  (str \"hi \" name))\n\n(def answer 42)\n\n(greet \"x\")\n", true},
	}
	for _, tc := range cases {
		t.Run(string(tc.lang), func(t *testing.T) {
			if !lang.BuildIncludes(tc.lang) {
				t.Skipf("%s excluded from this build", tc.lang)
			}
			res, err := ExtractFile([]byte(tc.src), tc.lang, "smoke", tc.file)
			if err != nil {
				t.Fatalf("extract %s: %v", tc.lang, err)
			}
			modules, funcs := 0, 0
			for _, d := range res.Definitions {
				switch d.Label {
				case "Module":
					modules++
				case "Function", "Method":
					funcs++
				}
			}
			if modules != 1 {
				t.Fatalf("%s: module definitions = %d, want 1", tc.lang, modules)
			}
			if tc.wantFuncs && funcs == 0 {
				t.Fatalf("%s: no function definitions extracted; defs=%v", tc.lang, res.Definitions)
			}
			if res.DepthCapped {
				t.Fatalf("%s: unexpected depth cap on a tiny file", tc.lang)
			}
		})
	}
}

func TestGoModIsDetectedByFilename(t *testing.T) {
	if l, ok := lang.LanguageForFilename("go.mod"); !ok || l != lang.GoMod {
		t.Fatalf("go.mod -> %q, %v", l, ok)
	}
	for ext, want := range map[string]lang.Language{".lua": lang.Lua, ".vue": lang.Vue, ".svelte": lang.Svelte, ".graphql": lang.GraphQL, ".gql": lang.GraphQL, ".erl": lang.Erlang, ".hrl": lang.Erlang, ".clj": lang.Clojure, ".cljs": lang.Clojure} {
		if l, ok := lang.LanguageForExtension(ext); !ok || l != want {
			t.Errorf("%s -> %q, %v; want %s", ext, l, ok, want)
		}
	}
}
