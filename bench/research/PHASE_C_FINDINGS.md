# Phase C: Joint experiment findings (2026-05-05)

## What this is

Phase C of [Plan 1](../../../../Documents/knowledge-base/plans/2026-05-05-codegraph-and-cross-tool-recommendations.md) is the highest-leverage item in the entire post-roundtable plan: a joint experiment that resolves two surviving roundtable disagreements simultaneously by binning unresolved calls per language AND classifying Loc-Bench misses.

This document captures what Phase C produced in this session and what's pending for follow-up.

## What shipped

### C1: Unresolved-call shape histogram

`bench/research/unresolved_call_shapes.py` walks every indexed project's source files and counts canonical dispatch shapes per language. Output saved to `bench/accuracy/baselines/2026-05-05-unresolved-call-shapes-aggregate.json` for drift comparison in future runs.

**Caveats**: pattern-based detection is heuristic. Patterns over-count (e.g., every `@foo(` matches `decorator_call`, including non-dispatch decorators) and under-count (some legitimate dispatch shapes are language-specific edge cases not covered). For "which shape category dominates," the data is directionally accurate.

### C1b: Failure-audit harness

`bench/research/locbench_failure_audit.py` provides the mechanical scaffold: load Loc-Bench results, identify misses, emit a YAML scaffold for human classification, then read back the classification and produce the decision-rule outcome.

**Status**: harness is ready, but per-case Loc-Bench results JSON is not currently checked in. The 2026-05-04 n=200 iter=2 run produced summary markdown at `bench/accuracy/baselines/2026-05-04-loc-bench-n200-iter2.md` but per-case data was not preserved as JSON. To run the audit, a follow-up session needs to re-run Loc-Bench eval with `--write-per-case-json` (the existing `eval_locbench_batch.py` may need a small change to add this output).

## Findings from C1 (shape histogram)

### Aggregate dispatch-shape distribution across 13 indexed projects

| Language | Top dispatch shape | % of dispatch sites | 2nd shape | %  |
|---|---|---:|---|---:|
| Go | `interface_method_call` | 88.4% | `function_value` | 9.4% |
| JavaScript | `method_dispatch` | 96.9% | `call_apply` | 2.5% |
| Python | `decorator_call` | 93.0% | `kwargs_call` | 2.7% |
| Rust | `trait_method_call` | 95.4% | `closure_invocation` | 4.2% |
| TypeScript | `method_dispatch` | 90.5% | `function_value` | 8.9% |

### Reading the data

These percentages count **all dispatch sites**, not **unresolved dispatch sites**. Many `interface_method_call` sites in Go ARE resolved by code-graph (it has Go LSP type resolution). The histogram answers "what shapes EXIST" — not "what shapes the extractor MISSES." That distinction matters.

A more precise question would be "what fraction of unresolved calls per language are which shape" — but the graph's `unresolved_call_count` property is just an integer; per-call shape data is not currently persisted. Adding per-call shape labels to the extractor (multi-day work — schema + pipeline change) would let us answer that directly.

### Initial directional read

Even with the caveats, the histogram favors **cross-language coverage** (Grok's roundtable position) over **Func Acc@10 work** (Opus + GPT's position) for at least 4 of 5 languages:

- **Go**: 88.4% dispatch sites are `interface_method_call`. Code-graph's Go LSP type resolution handles many of these but not all (e.g., interface methods on dynamically-created instances). Adding interface-resolution coverage closes the largest dispatch surface in Go codebases.
- **Rust**: 95.4% are `trait_method_call`. Similar story — many resolve via code-graph's Rust analyzer but trait-object dispatch (especially `Box<dyn Trait>`, 0.4%) is invisible.
- **TypeScript / JavaScript**: 90%+ are `method_dispatch`. TS has type info in the source; whether code-graph captures it depends on the TS extractor's typedness — would need direct measurement.
- **Python**: 93% `decorator_call` — but our pattern over-counts decorators that aren't dispatchers (`@staticmethod`, `@cached_property`, `@dataclass`). The actual unresolved-call surface in Python is dominated by `getattr_variable` (0.7% in our histogram, but probably the load-bearing source of unresolved-call-count).

### What this DOES NOT prove

The histogram does not prove which work yields the highest Loc-Bench Acc@10 lift. The dispatch shapes that EXIST are not 1:1 with the dispatch shapes that LOC-BENCH'S CASES TARGET. A Loc-Bench case asking "where is the JWT validation done?" might land on a sanitizer-protected path that doesn't depend on indirect dispatch at all.

The failure audit (C1b) is the only direct way to answer "what fraction of Loc-Bench misses are caused by missing-dynamic-dispatch-edges vs other causes." Until that runs, the dispatch-shape data is necessary-but-not-sufficient evidence.

## What needs to happen next

1. **Re-run Loc-Bench eval with per-case JSON output** so `locbench_failure_audit.py` can consume it. Likely a small change to `eval_locbench_batch.py` to emit per-case results, then a re-run on n=50-100 cases (~$2-5 in API spend).

2. **Classify the resulting cases** by hand into bucket a/b/c/d (~30s per case, ~30-60 minutes for 50-100 cases). The harness emits a YAML scaffold that minimizes the per-case work.

3. **Run `locbench_failure_audit.py --analyze`** to compute the bucket distribution and produce the decision-rule outcome:
   - >60% in (a) → Func Acc@10 work is #1 priority
   - >60% in (b) → cross-language indirect-call coverage is #1
   - >60% in (c) → scorer/protocol ablation is #1

## Honest framing for the roundtable disagreements

Until the failure audit runs, we can't say "the roundtable's question is resolved." We CAN say:

- **Grok's position** (cross-language indirect-call coverage as #1) is supported by the dispatch-shape distribution showing interface/trait dispatch dominates in 4 of 5 languages — but only **necessary** evidence, not **sufficient**.
- **Opus + GPT's position** (Func Acc@10 work as #1) is independent of the dispatch-shape data; the dispatch-shape distribution doesn't refute it. The Func Acc@10 work hypothesis still needs the per-case classification to confirm.

This is a partial resolution. The harness is in place; the data collection step is the remaining work.

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-05-codegraph-and-cross-tool-recommendations.md` Phase C
- Roundtable source: `~/Documents/roundtables/2026-05-05-code-graph/results/META_SYNTHESIS.md` Disagreement 1 + Disagreement 2
- Shape histogram script: `bench/research/unresolved_call_shapes.py`
- Failure audit harness: `bench/research/locbench_failure_audit.py`
- Aggregate JSON baseline: `bench/accuracy/baselines/2026-05-05-unresolved-call-shapes-aggregate.json`
