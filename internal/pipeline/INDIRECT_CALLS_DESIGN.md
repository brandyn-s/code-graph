# INDIRECT_CALLS edge design — Python indirect-dispatch analyzer

> Status: **design doc + first-iter scaffold**. Multi-week work to fully
> implement; this lands the design and one narrow path so subsequent
> sessions can extend incrementally.

## Why

`trace_call_path` returns `confidence_band: "speculative"` when the
extractor sees N call sites and binds 0 of them. The most common cause
on Python codebases is indirect dispatch:

```python
executor.submit(fn_var, arg)   # fn_var is a function reference
getattr(obj, name)()           # method named at runtime
@decorator                     # decorator dispatch chain
def handler(): ...
```

Today, code-graph's Python extractor emits 0 CALLS edges for any of
these. The 2026-05-05 confidence_band probe across 11 projects showed
~10% of nodes with both resolved and unresolved calls hit ratio < 0.10
("low" or "speculative" band). For the .claude project specifically,
P50 ratio is 0.15 — the median Python function has ~85% of its calls
unresolved.

## Existing infrastructure

`INDIRECT_CALLS` edge type already exists (35 edges in the .claude DB).
Schema:

```sql
edges (id, project, source_id, target_id, type, properties)
-- type='INDIRECT_CALLS', properties JSON includes:
--   confidence: "speculative" | "low" | "medium" | "high"
--   dispatch_kind: "executor_submit" | "getattr" | "decorator" | "fn_pointer" | ...
--   resolution_method: "scope_lookup" | "type_hint" | "import_trace" | "ast_pattern"
```

The edge type is plumbed through `internal/store/edges.go`; what's
missing is the **analyzer pass** that creates these edges.

## Scope decomposition

### v0.1 (first iter, ~2-4 days) — `executor.submit` only

Covers the most common pattern in `~/.claude` (4 instances in
`session-start.py` alone, plus `consistency.py`'s
`executor.submit(_run_check, item)` pattern).

Detection algorithm:
1. AST walk Python module
2. Find `Call(Attribute(Name(obj), "submit"), ...)` where obj is a
   `concurrent.futures` ThreadPoolExecutor or ProcessPoolExecutor (check
   via assignment-tracing within the function scope)
3. The first arg is the target callable — usually a `Name` referring to
   a function defined in the module
4. Resolve the Name to its `def` via scope analysis
5. Emit `INDIRECT_CALLS` edge with `confidence: "high"` (we're confident
   it's a function reference, just dispatched indirectly),
   `dispatch_kind: "executor_submit"`

### v0.2 (~3-5 days) — `getattr(obj, name)()`

Detection:
1. Find `Call(Call(Name("getattr"), [obj, Constant(name)]), ...)`
2. Resolve obj's type via assignment-tracing
3. If obj's type has a method `name`, emit edge to that method
4. Confidence: `medium` (type inference is imperfect)

### v0.3 (~5-7 days) — Decorator dispatch

Detection:
1. Find `@decorator` syntax above `def f`
2. The decorator is itself a Call or Name expression
3. The function is "called" through the decorator's wrapped path
4. Emit `INDIRECT_CALLS` from outer wrapping site to `f`
5. Confidence: depends — `@functools.wraps` decorators preserve behavior;
   custom decorators may rewrite

### v0.4 (~1-2 weeks) — Fn-pointer-as-arg

Detection:
1. Pattern: `f(callback)` where `callback` is a function reference
2. Need to track callee's signature: does `callback` get called inside
   `f`? This requires inter-procedural analysis or annotations
3. Conservative: only emit if `callback` is named like a callback
   (`callback`, `cb`, `handler`, `listener`) AND the callee has it
   in its signature

### v0.5 (~1-2 weeks) — `**kwargs` propagation

Most complex. Pattern: `f(**ctx)` where `ctx` is a dict of named
callables. Common in test fixtures and dependency injection.

## Acceptance criteria per iter

Each iter ships with:
1. Tests in `internal/pipeline/python_indirect_test.go` covering
   the patterns it claims to handle (3-5 fixtures per iter)
2. The fixture's expected `INDIRECT_CALLS` edge count appears in the
   test assertions
3. A bench/accuracy run on `.claude` and `mcp-servers` that compares
   pre- and post-iter:
   - Number of `INDIRECT_CALLS` edges added
   - Reduction in `unresolved_call_count` per Function node
   - Change in `confidence_band` distribution (expect % of "speculative"
     to drop)
4. No regression in CALLS F1 — the new edges are INDIRECT_CALLS, not
   regular CALLS, so the precision/recall measurement on CALLS should
   be unchanged

## Confidence-band integration

Each `INDIRECT_CALLS` edge has a `confidence` property. When
`trace_call_path` builds its response, the band calculation should:

- Count INDIRECT_CALLS edges as RESOLVED only when `confidence >=
  "high"` (executor.submit, basically)
- Count INDIRECT_CALLS edges with `confidence < "high"` as RESOLVED-
  PARTIAL: contribute to the resolved bucket but flag in response
- This is a follow-up to the empirical-threshold work shipped 2026-05-05b

## v0.1 stub (this commit)

`internal/pipeline/python_indirect.go` (NEW): skeleton with:
- `analyzePythonIndirectCalls(file *ast.File) []IndirectCallEdge`
- One unit test covering one fixture
- TODOs marking where the AST walk needs implementation

The stub doesn't run in the production pipeline yet — it's a separate
file with `_test.go` accompaniment. Wiring it into the pipeline
(`pipeline.go` `runIndirectCallsPass`) is the v0.1 ship step.

## Cross-references

- Empirical threshold work: `bench/research/confidence_band_distribution.py`
- Threshold values: `internal/tools/trace.go::traceConfidenceBand`
- Existing INDIRECT_CALLS infrastructure: `internal/store/edges.go`
- Trace response shape: `internal/tools/trace.go::buildTraceResponse`
