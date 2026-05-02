# Oracle Y.6 — parameter-receiver method call resolution

**Date**: 2026-05-02
**Source**: `/plateau-diagnose` invocation on cobra-go's function-body precision plateau (P=0.60)
**Discovery method**: Step 2 of the recipe (cheap reality check) — direct inspection of cell FPs surfaced the parameter-typed-receiver pattern in 5 minutes
**Impact**: cobra-go F1 0.9258 → 0.9396 (+1.4pp); function-body P 0.60 → 0.76 (+16pp)

## What changed in the instrument

PR #140 (Y.5) extended the oracle to substitute `<recv>.<method>` callees with `<RecvType>.<method>` form when `<recv>` matches the enclosing **method's** receiver name. This let the wrapper's `recv_method_to_qns` index resolve self-receiver method calls.

Y.5 didn't fix the parameter case: inside

```go
func writeCommands(cmd *Command, ...) {
    cmd.Name()  // ← Y.5 doesn't fire (no enclosing method receiver)
}
```

the oracle still emitted `cmd.Name`, which the wrapper dropped because "cmd" isn't a known file segment.

Y.6 mirrors the substitution for **function parameters** of typed-struct shape:

1. Visitor adds `paramTypeStack []map[string]string` (parallel to `fnStack`) — captures `param_name → type_name` per function via `extractParamTypes`.
2. CallExpr handling: after the Y.5 receiver substitution check, if `<recv>.<method>` callee remains and `<recv>` is in `paramTypeStack[top]`, substitute to `<RecvType>.<method>`.
3. `paramTypeName` extracts simple struct-type names from parameter type expressions (`*T`, `T`, `**T`, `T[Generic]`). Returns `""` for interfaces (SelectorExpr like `io.Writer`), slices, maps, channels, function types — all skipped because methods on these don't resolve to a single struct type.

## Measurement

### cobra-go (the target)

| Metric | Pre-Y.6 (Y.5 instrument) | Post-Y.6 | Δ |
|--------|--------------------------:|---------:|---:|
| F1 | 0.9258 | **0.9396** | +1.4pp |
| Precision | 0.8726 | 0.8967 | +2.4pp |
| Recall | 0.9861 | 0.9868 | +0.07pp |
| TP | 849 | 894 | +45 |
| FP | 124 | 103 | -21 |
| FN | 12 | 12 | 0 |
| **function-body P** | **0.599** | **0.759** | **+16pp** |
| cross-package-heuristic P | 0.851 | 0.884 | +3.3pp |
| oracle recv_method count | 239 | 286 | +47 |

The 47 new oracle resolutions translate to +45 TPs in the harness (2 lost to alignment quirks). 21 FPs eliminated as code-graph's emissions are now recognized.

### code-graph-go (cross-fixture confirmation)

| Metric | Pre-Y.6 | Post-Y.6 | Δ |
|--------|--------:|---------:|---:|
| F1 | 0.9801 | 0.9803 | +0.02pp |
| TP | 2363 | 2367 | +4 |
| FP | 94 | 93 | -1 |

Essentially unchanged. code-graph's call patterns are dominated by method receivers (Y.5 territory), not free-functions-with-typed-params (Y.6 territory). Y.6 is a no-op for self-hosting-style code; the cobra fixture is what made it visible.

## Why function-body P didn't reach the predicted ~0.95

Predicted: ~0.95+ based on "67/67 sampled FPs were parameter-receiver pattern."
Actual: 0.759 (still 46 residual FPs in function-body).

Reasons for the residual:
1. Some "free functions" actually have nested closures or method literals where the receiver is bound differently
2. cobra has function-typed fields (`PreRun func(cmd *Command, args []string)`) called via `cmd.PreRun(cmd, args)` — the call goes through a struct field, not a direct method call. Y.6 doesn't resolve these because `PreRun` isn't a method on `Command`, it's a field.
3. Some calls use intermediate variables (`c := cmd; c.Method()`) where Y.6 doesn't track local-variable type assignments.

These remaining 46 FPs would need additional oracle work (variable type inference, function-typed-field tracking) — diminishing returns. The +1.4pp F1 from Y.6 is close to ceiling for this class without much heavier oracle infrastructure.

## Discovery via /plateau-diagnose

This work is the first end-to-end run of the new `/plateau-diagnose` skill. Outcomes:

- **Step 1 (persona discovery): triage gate refused (0/5 criteria).** Fresh problem, hypothesis space partially mapped, no prior conventional engineering. Persona dispatch was not appropriate. Inline hypothesis generation substituted (5 hypotheses with calibration tags).
- **Step 2 (cheap reality check): immediately surfaced the answer.** Direct inspection of cobra's function-body FPs revealed the `<free_function> → Command.<method>` pattern in 5 minutes.
- **Steps 3-5: skipped** — instrumentation, baseline, and cell identification were already done by the cobra fixture addition (PR #142).
- **Step 6 (verify cell edges): 5/5 sampled were real failures, oracle artifact.** Fix lives in the harness, not code-graph.

The recipe ran in ~30 minutes total and produced an actionable, scoped fix. The triage gate's refusal saved ~5 min of unnecessary persona dispatch + ~$0.10. Step 2 alone surfaced the answer that would have justified the dispatch. **The recipe's worst-case (skipping Step 1) was its best-case here.**

## Files changed

- `bench/accuracy/tools/oracle-go-ast/main.go` — add `paramTypeStack` to visitor, populate from `extractParamTypes(funcType)`, substitute in CallExpr handler. Add `extractParamTypes` and `paramTypeName` helpers.
- `bench/accuracy/tools/oracle-go-ast/main_test.go` — 6 new tests for Y.6 (free-function param, multiple params same type, method receiver takes priority, non-struct param types not substituted, value param substituted, deep selector on param).
- `bench/accuracy/baselines/2026-05-02-oracle-y6-baseline.md` — this document.

No code-graph resolver changes. No Python wrapper changes (the existing `recv_method_to_qns` index handles `<RecvType>.<method>` callees correctly post-Y.5).

## Cumulative oracle improvements (since Step 6)

| Lever | Fixture | F1 delta |
|-------|---------|---------:|
| Y.3 Janusian penalty (#135) | code-graph-go | +0.5pp |
| CBM Method QN fix (#136) | code-graph-go | +0.6pp |
| Oracle Y.5 receiver-method (#140) | code-graph-go | +8.7pp |
| **Oracle Y.6 parameter-method (this PR)** | **cobra-go** | **+1.4pp** |
| **Cumulative on code-graph-go** | code-graph-go | **+8.8pp** (0.8934 → 0.9803) |
| **Cobra-go vs aspirational ~0.96+** | cobra-go | 0.9396 (still 0.04pp from aspiration) |

## Next priorities (after Y.6)

1. **Local variable type inference in oracle** — if the team wants to close the remaining cobra function-body residuals (~46 FPs). Higher effort (~6-8 hours), diminishing returns (~+0.5pp F1).
2. **Add a 3rd Go fixture** — gin (already cloned at `bench-fixtures/gin`) or chi. Would surface failure classes invisible on both code-graph-go and cobra-go.
3. **Stop oracle improvements** — current state (F1=0.98 on code-graph-go, F1=0.94 on cobra-go) is respectable. Further squeeze has diminishing returns; better to invest in resolver work or new features.

The team should pick based on what's blocking. If a downstream user reports inaccurate edges, fix that. If accuracy work is open-ended, stop here and revisit.
