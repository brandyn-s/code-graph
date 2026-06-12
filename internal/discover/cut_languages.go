package discover

// CutLanguageHints maps extensions of languages removed in the 2026-06-10
// grammar cut to a restore hint (see CLAUDE.md "Parsing" for the restore
// procedure). index_health uses this to split unsupported-extension tallies
// into a cut-language tier (always reported when non-empty, any count — the
// language-adoption-lag canary the cut created) and an unknown-extension
// tier (informational).
//
// ".m" is ambiguous (MATLAB / Objective-C) and is deliberately omitted;
// ".mm" is unambiguously Objective-C++. If a cut language is ever restored,
// remove its entries here — TestCutLanguageHints_NoneSupported enforces
// that every listed extension is actually unsupported.
var CutLanguageHints = map[string]string{
	".kt": "kotlin", ".kts": "kotlin",
	".swift": "swift", ".rb": "ruby", ".php": "php",
	".scala": "scala", ".cs": "csharp", ".fs": "fsharp",
	".ml": "ocaml", ".mli": "ocaml", ".hs": "haskell",
	".ex": "elixir", ".exs": "elixir", ".erl": "erlang", ".hrl": "erlang",
	".clj": "clojure", ".cljs": "clojure", ".cljc": "clojure",
	".lua": "lua", ".jl": "julia", ".dart": "dart",
	".r": "r", ".pl": "perl", ".pm": "perl",
	".mm": "objc", ".zig": "zig", ".vue": "vue",
	".groovy": "groovy", ".gradle": "groovy",
	".el": "elisp", ".vim": "vimscript",
	".f90": "fortran", ".f95": "fortran",
	".lean": "lean", ".sv": "verilog", ".svh": "verilog",
	".graphql": "graphql", ".gql": "graphql",
}
