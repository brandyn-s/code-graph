# INDIRECT_CALLS edge design — Python indirect-dispatch analyzer

> Status: **design doc + shipped v0.1/v0.2**. v0.3 scope refreshed
> 2026-05-23 after the flask-tiny baseline measurement (see
> "Measured failure mode" below). v0.4+ remain forward-looking.

## Measured failure mode (2026-05-23)

The flask-tiny baseline
(a private Markdown knowledge-base repo →
`harness/baselines/2026-05-23-code-graph-flask-tiny-baseline.md`,
PR #607) ran `trace_call_path` against a small synthetic Flask app
and measured the actual response shape for decorator-dispatched
handlers and `before_request`-registered middleware.

The empirical finding refutes the earlier "speculative band" framing:

| Question | Pattern | `total_results` | `confidence_band` | `unresolved_call_count` |
|---|---|---:|---|---:|
| F003 inbound `login_view` | `@login_required` functools.wraps | 0 | **`high`** | **0** |
| F004 inbound `log_request` | `@app.before_request(fn)` | 0 | **`high`** | **0** |

The trace returns `confidence_band="high"` with `unresolved_call_count=0`
because **the extractor never emits a `CBMCall` entry for these
dispatch sites at all**. They are not "speculative" calls (calls the
resolver saw and couldn't bind). They are **invisible** calls — the
CBM extractor has no pattern that fires on the `@app.route(...)`
decoration, the `@login_required` chain, or the `app.before_request(fn)`
hook registration.

Concrete consequence for v0.3 scoping: the fix is **extractor
synthesis**, not metadata refinement on already-emitted edges. The
existing `executor.submit` pattern at
`internal/cbm/extract_calls.c:286-309` (and the symmetric
`Depends(fn)` pattern at lines 247-268, `getattr(obj,"name")()` at
313-353) is the architectural precedent: at the relevant call site,
push an additional `CBMCall` with the synthesized callee_name and a
`dispatch_kind` label. The Go-side `tagIndirectDispatch`
(`internal/pipeline/pipeline_cbm.go:456-465`) labels the resulting
CALLS edge with `properties.dispatch_kind` + `confidence`; no new
edge type is required.

## Why

The most common Python indirect-dispatch shapes are invisible to a
tree-sitter call-site walker that only matches lexical call
expressions:

```python
executor.submit(fn_var, arg)        # fn_var is a function reference
Depends(fn)                         # FastAPI dep injection
getattr(obj, name)()                # method named at runtime
app.before_request(fn)              # Flask hook registration
@app.route('/path')                 # Flask route decoration
def handler(): ...
@login_required                     # functools.wraps closure dispatch
def view(): ...
```

For each of these patterns, the call that ultimately reaches the
target function (`fn_var`, `fn`, `name`, `handler`, `view`) is not
spelled lexically as a call to that name. v0.1 and v0.2 covered the
first three shapes via Call-synthesis (emit an additional `CBMCall`
with the dispatch target as `callee_name`). v0.3 extends the same
pattern family to Flask hook registration. See
`INDIRECT_CALLS_V0_3_PLAN.md` for the v0.3 implementation plan.

The 2026-05-05 confidence_band probe across 11 projects showed
~10% of nodes with both resolved and unresolved calls hit ratio < 0.10
("low" or "speculative" band). For the .claude project specifically,
P50 ratio is 0.15 — the median Python function has ~85% of its calls
unresolved. That measurement aimed at the "resolver-misses-known-call-
sites" class. The flask-tiny baseline above isolated a **different**
class: dispatch sites the extractor never sees as calls in the first
place. Both classes are real; v0.3 targets the latter on the Flask
hook-registration family because the flask-tiny fixture pins it
directly.

## Existing infrastructure (v0.1 / v0.2 — shipped)

`CBMCall.dispatch_kind` (`internal/cbm/cbm.h:108-121`) is a string
field on every extracted call. Direct calls leave it `NULL`; the
extractor sets it to a label string when emitting a synthesized
dispatch call. Shipped values so far:

- `"depends"` — FastAPI `Depends(fn)` argument injection
- `"executor_submit"` — `<pool>.submit(fn, ...)` (v0.1)
- `"getattr"` — `getattr(obj, "method")()` (v0.2)

`tagIndirectDispatch(edge *resolvedEdge, dispatchKind string)`
(`internal/pipeline/pipeline_cbm.go:456-465`) consumes the field
during edge resolution: when the CBM call resolved to a target node,
the resulting `CALLS` edge gets `properties.dispatch_kind = <label>`
and `properties.confidence = <band>` per
`dispatchKindConfidence` (line 470-479: executor_submit/depends →
`"high"`, getattr → `"medium"`, unknown → `"medium"`).

`trace_call_path`'s response surfaces `confidence_band` per node
(`internal/tools/trace.go::traceConfidenceBand`); the band is
computed from `resolved / (resolved + unresolved)` over the node's
call sites. INDIRECT_CALLS-style edges count as resolved for the
band calculation, which is the desired behavior — they indicate the
extractor handled the dispatch deterministically.

**Note on edge type naming.** There IS a separate `INDIRECT_CALLS`
edge label in the graph today, but it is used by a different mechanism:
`internal/pipeline/pipeline.go:2074-2087` re-types a `CALLS` edge to
`INDIRECT_CALLS` when the resolver targets a non-callable node
(Variable, Class, File). v0.1/v0.2 dispatch_kind labeling does NOT
use the `INDIRECT_CALLS` edge type — it stays on `CALLS` edges with
a `dispatch_kind` property. v0.3 follows the same convention. The
original framing of "INDIRECT_CALLS edge type with confidence /
dispatch_kind properties" was superseded during v0.1 implementation
in favor of property-on-CALLS, which avoided introducing a parallel
edge-resolution code path.

## Scope decomposition

### v0.1 (first iter, ~2-4 days) — `executor.submit` only

> Original design preserved below. The shipped implementation took
> the C-extractor / `CBMCall.dispatch_kind` path (see "Existing
> infrastructure" above + the trailing "v0.1 — shipped" note); the
> "Emit INDIRECT_CALLS edge" step below was superseded by emitting a
> synthesized `CBMCall` whose `dispatch_kind="executor_submit"` flows
> through `tagIndirectDispatch` onto a plain `CALLS` edge.

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

> Same redirect as v0.1: shipped implementation emits a synthesized
> `CBMCall` with `dispatch_kind="getattr"` on a plain CALLS edge.

Detection:
1. Find `Call(Call(Name("getattr"), [obj, Constant(name)]), ...)`
2. Resolve obj's type via assignment-tracing
3. If obj's type has a method `name`, emit edge to that method
4. Confidence: `medium` (type inference is imperfect)

### v0.3 (refreshed 2026-05-23) — Flask hook registration synthesis

**Scope refresh.** The original v0.3 framing ("Decorator dispatch")
collapsed three distinct dispatch shapes into one. After the
flask-tiny baseline, v0.3 is re-scoped to the simplest shape — Flask
hook-registration calls — and the other shapes are split into
follow-up versions.

**Primary scope (Pattern A) — `app.before_request(fn)` family.**
Detection:
1. In `walk_calls` / `handle_calls`, when callee_name matches
   `*.before_request`, `*.after_request`, `*.teardown_request`,
   `*.errorhandler`, `*.context_processor` (Flask hook registrars),
   AND the first argument is a bare identifier
2. Push an additional `CBMCall` with `callee_name = <fn>`,
   `enclosing_func_qn = <containing function>`,
   `dispatch_kind = "before_request_hook"` (or per-registrar tag —
   see v0.3 plan for the precise allowlist)
3. Confidence: `"high"` — the registration is an explicit function
   reference; only the dispatch is indirect

This is the same architectural shape as `executor.submit` (the
existing pattern at `internal/cbm/extract_calls.c:286-309`). No new
extractor pass, no closure tracking, no new edge type.

**Secondary scope (Pattern B) — `@app.route('/path')` decorator
with args.** Detection rules + the open design question on
`enclosing_func_qn` semantics are documented in
`INDIRECT_CALLS_V0_3_PLAN.md`. Ships in v0.3 if the design question
is resolved cheaply; otherwise rolls to v0.4 alongside Pattern C.

**Deferred to v0.4 (Pattern C) — `@login_required` functools.wraps
closure.** This requires resolving the closure-bound variable `func`
inside the `wrapper` body back to the wrapped function — i.e.
intra-function scope tracking that the existing C extractor does not
do today. Per `scope-discipline.md`: building a new extractor
analysis pass for a single decorator family is too costly for v0.3.
flask-tiny F003 stays at recall=0.0 until v0.4 lands Pattern C.

See `INDIRECT_CALLS_V0_3_PLAN.md` for the full v0.3 implementation
plan (file-by-file change list, test plan, success criteria, risks).

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
1. CBM-level tests in `internal/cbm/cbm_test.go` (e.g.
   `TestPythonExecutorSubmitTracked`) asserting that the new patterns
   produce additional `CBMCall` entries with the expected
   `dispatch_kind` label
2. Pipeline-level tests in `internal/pipeline/dispatch_tag_test.go`
   asserting that the resulting CALLS edges land with
   `properties.dispatch_kind = <label>` and the correct confidence
   band per `dispatchKindConfidence`
3. A bench/accuracy run on `.claude` and `mcp-servers` that compares
   pre- and post-iter:
   - Number of CALLS edges with `dispatch_kind` set
   - Reduction in `unresolved_call_count` per Function node
   - Change in `confidence_band` distribution (expect % of
     "speculative" to drop)
4. No regression in CALLS F1 — synthesized edges resolve to known
   target nodes via the same `resolveCallEdge` path; precision/recall
   on the base CALLS relation should be unchanged or improved
5. For v0.3 specifically: the flask-tiny baseline regression gate
   (see Cross-references) — F004 must move from recall=0.0 to ≥0.5
   without regressing F001/F002

## Confidence-band integration

Each synthesized CBMCall flows through `tagIndirectDispatch` and
lands on a plain `CALLS` edge with `properties.confidence` set per
`dispatchKindConfidence(dispatchKind)` (`pipeline_cbm.go:470-479`).
When `trace_call_path` builds its response:

- Synthesized CALLS edges count toward the resolved bucket
  (`traceConfidenceBand` operates on `resolved_call_count` vs
  `unresolved_call_count`); the `dispatch_kind` property is available
  for downstream consumers that want to weight or filter them
- The threshold work shipped 2026-05-05b (95% / 10% breakpoints) is
  unchanged — synthesized edges affect the numerator on which the
  ratio is computed
- A future follow-up could differentiate "all-resolved-direct" from
  "all-resolved-with-some-indirect" in the response, but this is
  not part of v0.3 scope

## v0.1 — shipped (executor.submit + Depends)

The eventual v0.1 implementation went through the C extractor rather
than a Go-side `python_indirect.go` pass. Rationale: the AST walk was
already happening in C (`internal/cbm/extract_calls.c`), and emitting
an additional `CBMCall` from the existing call-walk site was a 15-line
patch vs a new Go pass + new file + new test infrastructure. The
property-on-CALLS approach also avoided plumbing a parallel
`INDIRECT_CALLS` edge-resolution path. See
`internal/cbm/extract_calls.c:247-309` for the shipped emission sites
(both `walk_calls` legacy path and `handle_calls` unified path).

The historical "v0.1 stub" framing above (a new
`python_indirect.go` skeleton) was superseded and never landed; this
note replaces it.

## Cross-references

- Flask hook-registration baseline + v0.3 regression gate:
  a private Markdown knowledge-base repo →
  `harness/baselines/2026-05-23-code-graph-flask-tiny-baseline.md`
  (PR #607, 2026-05-23)
- v0.3 implementation plan: `INDIRECT_CALLS_V0_3_PLAN.md` (this directory)
- Empirical threshold work: `bench/research/confidence_band_distribution.py`
- Threshold values: `internal/tools/trace.go::traceConfidenceBand`
- Existing CALLS-edge re-type to INDIRECT_CALLS for non-callable
  targets: `internal/pipeline/pipeline.go:2033-2087` (distinct
  mechanism from dispatch_kind labeling)
- Trace response shape: `internal/tools/trace.go::buildTraceResponse`
