# internal/pipeline single-site investigation — 2026-05-02

**Site**: `c-Users-...-internal-pipeline.pipeline.Pipeline.runIncrementalPasses`
**Blast-radius**: 34 of 434 errors (7.8% of all errors on Go fixture).
**Source baseline**: post-Y.3 (PR #135) + CBM QN fix (PR #136).
**Verdict**: **Oracle limitation, not code-graph bug.** Code-graph emits these edges correctly; the harness's go-ast oracle systematically drops them.

## Diagnosis

All 34 errors at this site are FPs (no FNs). All 34 callees are real `Pipeline.pass*` / `Pipeline.build*` methods on the same `*Pipeline` receiver, defined across `internal/pipeline/*.go` files:

- `Pipeline.passCommunities`, `Pipeline.passConfigLinker`, `Pipeline.passConfigures`, `Pipeline.passDataflow`, `Pipeline.passDecorates`, `Pipeline.passDecoratorTags`, `Pipeline.passEnvVarNodes`, `Pipeline.passGitHistory`, `Pipeline.passHTTPLinks`, `Pipeline.passImports`, `Pipeline.passImplements`, `Pipeline.passInherits`, `Pipeline.passNixServices`, `Pipeline.passOPALinker`, `Pipeline.passReadsWrites`, `Pipeline.passStructure`, `Pipeline.passTests`, `Pipeline.passThrows`, `Pipeline.passUsesType`, `Pipeline.passUsagesForFiles`, `Pipeline.passZenoh`
- `Pipeline.buildEnvReaders`, `Pipeline.buildGoLSPDefIndex`, `Pipeline.buildRegistry`, `Pipeline.buildReturnTypeMap`
- `Pipeline.passCallsForFiles`, `Pipeline.passDefinitions`, `Pipeline.checkCancel`, `Pipeline.cleanupASTCache`, `Pipeline.findDependentFiles`, `Pipeline.logEdgeCounts`, `Pipeline.releaseExtractionFields`, `Pipeline.removeDeletedFiles`, `Pipeline.updateFileHashes`

All are **literally present in the source** at `internal/pipeline/pipeline.go::runIncrementalPasses` (lines 676-816). All 34 are **TPs misclassified as FPs**.

## Root cause: oracle resolver shape mismatch

The go-ast oracle has two stages:

1. **Binary stage** (`bench/accuracy/tools/oracle-go-ast/main.go`): walks the AST. For a method call like `p.passConfigures()`, `extractCallee` (line 167-173) returns `"p.passConfigures"` — concatenating the **receiver identifier** (`p`) with the method selector.

2. **Python wrapper stage** (`bench/accuracy/oracle_go_ast.py:182-199`): for 2-segment callees, treats the first segment as a **filename** to resolve via the file_segments set. Since `"p"` is not a known file segment, the edge is **dropped** as `calls_path_dropped`.

The wrapper has no path for "first segment is a struct receiver type". So `p.passConfigures` (where `p *Pipeline`) gets dropped instead of resolving to `Pipeline.passConfigures`.

Code-graph's resolver, by contrast, knows `p` is `*Pipeline` (via the type-dispatch path with TypeMap binding) and resolves correctly to the real method QN.

## Implication

This isn't a 34-edge problem. It's a **systematic oracle bias** against receiver-method calls where the receiver is a function-parameter of a typed struct. Likely affected by the same gap:

- `internal/pipeline`: 276 FPs total, P=0.7163. A large share is probably oracle-dropped real edges.
- All `Server.handle*` methods that call `s.handleXxx` helpers (similar pattern in `internal/tools`).
- All `Store.*` methods calling `s.helperXxx` (similar pattern in `internal/store`).

If the oracle were fixed, internal/pipeline F1 would likely jump from 0.83 to 0.90+, and aggregate F1 by 2-5pp.

## Recommendation: new follow-up — oracle receiver-method resolution

This is the CBM QN bug's mirror at the oracle layer. Both are measurement-instrument bugs that show up as fake FNs/FPs.

**Fix design (out of scope for this PR)**:

1. **Oracle binary**: track receiver identifier in `visitor` (currently only type is tracked). When CallExpr is `recv.method` and `recv` matches the enclosing method's receiver name, emit callee form `<RecvType>.<method>` instead of `<recvIdent>.<method>`.
2. **Python wrapper**: extend the 2-segment branch to also try resolving `<RecvType>.<method>` patterns by looking for any QN in the def list ending with `.<RecvType>.<method>`.
3. **Test**: pin the resolution on a synthetic 2-method `*Pipeline` fixture.
4. **Re-baseline**: F1 will shift up notably; mark this as a new instrumented baseline and document the delta as instrument-improvement, not code-graph change.

**Headroom**: 30-100+ "FP" → "TP" conversions in internal/pipeline alone, plus parallel patterns in internal/store and internal/tools. Possible aggregate F1 lift: 2-5pp.

## What this means for #2's original premise

The original follow-up plan estimated #2 as "potentially multi-pp F1 impact, possibly a 1-line resolver fix". Both halves were wrong:

- **Multi-pp F1 impact**: still likely, but conditional on fixing the oracle, not code-graph.
- **1-line resolver fix**: there is no resolver fix — code-graph is already correct.

The 5-step plateau-diagnosis recipe correctly surfaced the cell. What it didn't surface (and the recipe doesn't yet have a step for) is: **distinguish "code-graph error" from "instrument error" before designing the fix**. The CBM QN bug (PR #136) was the same shape — instrument problem masquerading as code-graph problem.

## Action

- Code-graph: no changes.
- Knowledge-base: this report becomes the diagnostic record.
- New follow-up #5: oracle receiver-method resolution (out of scope here).
