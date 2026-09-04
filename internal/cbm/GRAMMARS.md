# Tree-sitter Grammar Vendoring

This directory (`internal/cbm/vendored/grammars/`) contains 27 vendored
tree-sitter grammars compiled into the binary via CGO.

## Vendoring strategy

Grammars are vendored as raw `parser.c` + `scanner.c` + tree-sitter
header files, copied directly from upstream grammar repositories at
some past time. There is no submodule, no git lockfile, and no
recorded upstream SHA tracking when a given grammar was vendored.

This is a **known gap** identified in the 2026-05-05 multi-agent
roundtable: tree-sitter grammar version drift has no detection
mechanism. Without upstream SHAs we cannot ask "is grammar X stale?"
The mitigation is a canary-fixture stability check
(`bench/research/grammar_drift_check.py`) which detects parse-shape
regressions without needing upstream tracking.

## Canary-fixture stability

The drift detection harness lives at
`bench/research/grammar_drift_check.py`. It works as follows:

1. For each tracked language, parse a small canary fixture (1-3 files
   per language, in `bench/research/grammar_canaries/<lang>/`).
2. Compute a structural fingerprint of the resulting AST: top-level
   node types and counts, total node count, max depth.
3. Compare against the saved baseline at
   `bench/research/grammar_canaries/baselines.json`.
4. Surface drift as a non-zero exit code in CI.

The fingerprint is intentionally coarse — we want to detect
"someone updated parser.c and the AST shape changed," not "the
parser added a new optional sub-rule that doesn't affect what we
extract." If a canary fixture's fingerprint changes, the upgrade
needs human review.

## When the canary fires

A canary regression has three plausible causes:

1. **Intentional grammar upgrade** (rare; we don't currently rotate
   vendored grammars on a schedule). Update the baseline.
2. **Accidental drift** (someone copied parser.c from a different
   upstream version). Revert or accept the upgrade with eyes open.
3. **Canary fixture itself was edited** (someone changed the .py
   fixture, which legitimately changes the AST). Update the
   baseline if the fixture change was intentional.

The CI runs the harness weekly. Drift surfaces as an issue comment
on the workflow run.

## Tracked languages

The canary corpus initially covers the top-priority languages used
in the reference codebases. Other languages remain untracked until either
(a) a regression is observed in the wild, or (b) someone adds a
canary fixture for that language.

| Language | Canary fixture | Notes |
|---|---|---|
| python | `grammar_canaries/python/canary.py` | Most accuracy-fragile per confidence_band probe |
| go | `grammar_canaries/go/canary.go` | Self-host (code-graph itself is Go) |
| rust | `grammar_canaries/rust/canary.rs` | Heavy use in the reference codebases |
| typescript | `grammar_canaries/typescript/canary.ts` | TS-MRR outlier per code-search roundtable |
| javascript | `grammar_canaries/javascript/canary.js` | Webclient codebases |

Adding a language: drop `canary.<ext>` in
`bench/research/grammar_canaries/<lang>/`, run `grammar_drift_check.py
--update-baseline <lang>`, commit the new entry in `baselines.json`.

## What we do NOT track (yet)

- Upstream grammar repository SHAs at vendoring time. Adding this
  would require auditing every grammar in `vendored/grammars/` and
  recording the SHA from upstream. Plausible follow-up workstream;
  not currently in scope.
- Per-grammar generation date. Same as above.
- Differential testing against the upstream tree-sitter parser
  binary. Would catch "we have a stale grammar; the new grammar
  behaves differently on the same input." Currently relies on
  whoever vendored each grammar to have done so cleanly.

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-05-codegraph-and-cross-tool-recommendations.md` Phase A2
- Roundtable finding (HIGH confidence, 3-of-3): `~/Documents/roundtables/2026-05-05-code-graph/results/META_SYNTHESIS.md` convergent finding 5
- Surface in `index_health` MCP tool (Phase B4): grammar version + parse-error rate
- Schema metadata: `internal/tools/METADATA_SCHEMA.md` — `provenance.grammar_versions` field

## Provenance and licenses (2026-09-03)

Every vendored grammar now carries its upstream LICENSE file in its
directory, and `THIRD_PARTY_NOTICES.md` at the repository root records the
upstream URL, pinned ref, ABI, and license for each one.

## PowerShell provenance (2026-06-10)

`powershell/` was the first grammar with recorded provenance:
vendored from https://github.com/airbus-cert/tree-sitter-powershell at
commit `d398441825243b00e317e87e1829b9d6a3e54ce0` (MIT license), traded
in during the 38-grammar cut. The grammar has no named fields; the
extraction fallbacks live in extract_defs.c / extract_calls.c /
extract_unified.c / helpers.c gated on CBM_LANG_POWERSHELL, and are
pinned by TestPowerShellExtraction.

## Grammars added from the upstream manifest (0.9.1)

Lua, Vue, Svelte, GraphQL, go.mod, Erlang, and Clojure were vendored at the
commits pinned in upstream codebase-memory-mcp's `MANIFEST.md` using
`scripts/vendor-grammar-from-manifest.sh`. They are in the default build
(about 4 MB of parser source in total). Extraction depth today: Lua, Erlang,
and Clojure emit functions and calls; GraphQL emits types and fields; go.mod
emits require/replace directives; Vue and Svelte are parsed for the module
node and branching only (embedded `<script>` blocks are not yet extracted).
Smoke coverage: `internal/cbm/grammar_smoke_test.go`.
