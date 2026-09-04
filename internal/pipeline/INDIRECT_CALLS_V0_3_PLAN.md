# INDIRECT_CALLS v0.3 — Flask hook registration synthesis

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to
> implement this plan task-by-task.

**Goal:** Extend the Python C extractor to synthesize `CBMCall` entries
for Flask `app.before_request(fn)` family hook registrations, and
(stretch) for `@app.route('/path')` route decorators with args, so
`trace_call_path` returns non-empty inbound traces on the affected
target functions. The flask-tiny baseline (PR #607 on
a private Markdown knowledge-base repo) is the regression gate:
F004 (`@app.before_request` inbound) must move from recall=0.0 to ≥0.5
without regressing F001/F002 sanity-checks.

**Architecture:** Two new pattern-emission blocks added to
`internal/cbm/extract_calls.c` next to the existing
`executor.submit` / `Depends` / `getattr` Call-synthesis sites. The
emission shape is identical: at a known call expression, push an
additional `CBMCall` whose `callee_name` is the function-reference
argument (or the decorated function) and whose `dispatch_kind` is a
new string label. The Go-side `dispatchKindConfidence` in
`internal/pipeline/pipeline_cbm.go` is extended to map the new labels
to confidence bands. No new C-extractor pass, no new Go pipeline
stage, no new edge type — same property-on-CALLS convention as v0.1.

**Tech Stack:** C (tree-sitter Python grammar already vendored), Go 1.26,
SQLite (existing `properties` JSON column), tree-sitter call_node_types
spec for Python (already populated for the existing patterns).

**Repo:** `brandyn-s/code-graph` (branch
`claude/ecstatic-pasteur-ebWx8` for the design refresh PR;
implementation lands on a separate `feat/indirect-calls-v0.3` branch).

**Dependencies:** v0.1 (`executor.submit` Call-synthesis +
`CBMCall.dispatch_kind` field + Go-side `tagIndirectDispatch`) is the
foundation. v0.2 (`getattr`) adds nothing new at the schema level but
demonstrates the multi-pattern emission strategy. v0.3 extends both —
no new types, no new passes.

**Regression gate (flask-tiny):** Documented at
a private Markdown knowledge-base repo →
`harness/baselines/2026-05-23-code-graph-flask-tiny-baseline.md`
(PR #607, merged 2026-05-23). Re-running
`harness/runners/run_code_graph_trace.py` against the v0.3 build is
the binary pass/fail signal for this plan.

---

## Pattern A (PRIMARY) — `app.before_request(fn)` family

### Task A1: Synthesize CBMCall for Flask hook registrations

**Finding:** Today, `app.before_request(log_request)` is walked by
`extract_calls.c::walk_calls` (and the unified `handle_calls`) as a
plain call. `callee_name` becomes the attribute path (e.g.
`"app.before_request"`), `enclosing_func_qn` is the containing
function (e.g. `create_app`), and `dispatch_kind` is `NULL`. The
function reference `log_request` is **not** emitted as a call target,
so the graph has no inbound CALLS edge into `log_request`. The
flask-tiny baseline (F004) confirms this is recall=0.0 with
`confidence_band="high"` (the extractor sees no calls into
`log_request` and reports that confidently).

The shape is IDENTICAL to the existing `executor.submit` pattern at
`internal/cbm/extract_calls.c:286-309`. The fix is a parallel
emission block keyed off a different suffix allowlist.

**Allowlist of Flask hook registrar method suffixes** (initial
proposed set — sized to avoid false positives outside Flask):

| Suffix | Flask method | dispatch_kind label |
|---|---|---|
| `.before_request` | `Flask.before_request`, `Blueprint.before_request` | `"before_request_hook"` |
| `.after_request` | `Flask.after_request`, `Blueprint.after_request` | `"after_request_hook"` |
| `.teardown_request` | `Flask.teardown_request` | `"teardown_request_hook"` |
| `.teardown_appcontext` | `Flask.teardown_appcontext` | `"teardown_appcontext_hook"` |
| `.errorhandler` | `Flask.errorhandler`, `Blueprint.errorhandler` | `"errorhandler_hook"` |
| `.context_processor` | `Flask.context_processor` | `"context_processor_hook"` |
| `.before_first_request` | `Flask.before_first_request` (deprecated but extant) | `"before_first_request_hook"` |

All seven use the same emission shape. The plan ships the full
allowlist in one patch because the C code is one suffix match.

**Files:**
- Modify: `internal/cbm/extract_calls.c` — add the new emission block
  in BOTH `walk_calls` (legacy path, ~line 309) and `handle_calls`
  (unified path, ~line 464). Mirror the executor.submit block exactly,
  swap the suffix match and the `dispatch_kind` label.
- Modify: `internal/pipeline/pipeline_cbm.go` — extend
  `dispatchKindConfidence` switch (line 470-479) to map each new
  label to `"high"`.
- Modify: `internal/cbm/cbm.h` — no schema change required.
  Confirm + document by adding the new labels to the comment block
  at lines 111-116 (currently lists `"executor_submit"`, `"depends"`,
  with v0.2+ noted as `"getattr"`, `"decorator"`, `"fn_pointer"`).
- Add tests: `internal/cbm/cbm_test.go` — one test per registrar
  family (or one parameterized test covering the allowlist).
- Add tests: `internal/pipeline/dispatch_tag_test.go` — one test
  asserting that a `before_request_hook` dispatch_kind on the CBM
  side produces a CALLS edge with `properties.confidence="high"`.

**Step 1 (CBM-level): Write the failing test**

Add to `internal/cbm/cbm_test.go` (next to the existing
`TestPythonExecutorSubmitTracked` if present; otherwise modeled on it):

```go
func TestPythonFlaskBeforeRequestTracked(t *testing.T) {
    src := []byte(`
from flask import Flask
from middleware import log_request

def create_app():
    app = Flask(__name__)
    app.before_request(log_request)
    return app
`)
    res := extractCalls(t, "create_app.py", src, langPython)
    // Expect 3 calls: Flask(__name__), app.before_request(log_request),
    // and the synthesized log_request call carrying the dispatch_kind.
    var synth *CBMCall
    for i := range res.Calls {
        if res.Calls[i].CalleeName == "log_request" &&
            res.Calls[i].DispatchKind == "before_request_hook" {
            synth = &res.Calls[i]
            break
        }
    }
    if synth == nil {
        t.Fatalf("expected synthesized call to log_request with "+
            "dispatch_kind=before_request_hook; got %d calls: %+v",
            len(res.Calls), res.Calls)
    }
    if synth.EnclosingFuncQN != "create_app" {
        t.Errorf("expected enclosing_func_qn=create_app; got %q",
            synth.EnclosingFuncQN)
    }
}
```

(Parameterize over the 7 registrars if the test harness already
supports it; otherwise one test per registrar.)

**Step 2 (CBM-level): Make the test pass**

Add to `extract_calls.c::handle_calls` (mirror in `walk_calls`):

```c
// Python: Flask hook registration — emit fn as a call target.
// INDIRECT_CALLS v0.3 Pattern A. Mirrors executor.submit; the
// before_request family (before_request / after_request /
// teardown_request / errorhandler / etc.) registers a function
// reference with Flask, which then invokes it at request-dispatch
// time. Without this, every hook registration is silent on the
// inbound trace of the registered function. See
// INDIRECT_CALLS_V0_3_PLAN.md for the full registrar allowlist.
if (ctx->language == CBM_LANG_PYTHON) {
    const char* flask_hook_kind = cbm_python_flask_hook_label(callee);
    if (flask_hook_kind != NULL) {
        TSNode args = ts_node_child_by_field_name(node, "arguments", 9);
        if (!ts_node_is_null(args)) {
            uint32_t ncount = ts_node_named_child_count(args);
            if (ncount > 0) {
                TSNode first_arg = ts_node_named_child(args, 0);
                if (!ts_node_is_null(first_arg) &&
                    strcmp(ts_node_type(first_arg), "identifier") == 0) {
                    char* hook_name = cbm_node_text(ctx->arena, first_arg, ctx->source);
                    if (hook_name && hook_name[0] && !cbm_is_keyword(hook_name, ctx->language)) {
                        CBMCall hook_call;
                        hook_call.callee_name = hook_name;
                        hook_call.enclosing_func_qn = state->enclosing_func_qn;
                        hook_call.dispatch_kind = flask_hook_kind;
                        cbm_calls_push(&ctx->result->calls, ctx->arena, hook_call);
                    }
                }
            }
        }
    }
}
```

`cbm_python_flask_hook_label(callee)` is a new static helper (in
`extract_calls.c` or a sibling file) that returns the per-registrar
label string when the callee_name ends in one of the seven suffixes,
NULL otherwise. Implementation is a 7-branch suffix check; the same
helper is consulted from both `walk_calls` and `handle_calls` to
keep the allowlist single-sourced.

**Step 3 (pipeline-level): Confidence-label test**

Add to `internal/pipeline/dispatch_tag_test.go`:

```go
func TestDispatchKindConfidence_BeforeRequestHook(t *testing.T) {
    if got := dispatchKindConfidence("before_request_hook"); got != "high" {
        t.Errorf("before_request_hook → confidence: got %q, want %q", got, "high")
    }
    // Repeat for the other 6 labels.
}
```

**Step 4 (pipeline-level): Extend dispatchKindConfidence**

Add the new cases to the switch at `pipeline_cbm.go:470-479`:

```go
case "executor_submit", "depends",
     "before_request_hook", "after_request_hook",
     "teardown_request_hook", "teardown_appcontext_hook",
     "errorhandler_hook", "context_processor_hook",
     "before_first_request_hook":
    return "high"
```

**Step 5 (flask-tiny regression gate):**

After Steps 1-4 land, run the runner from the harness repo:

```bash
# Rebuild v0.3 binary
CGO_ENABLED=1 go build -o /tmp/cmm-v0.3.exe ./cmd/code-graph/

# In ../claude-knowledge-base
python3 harness/runners/run_code_graph_trace.py \
    --binary /tmp/cmm-v0.3.exe \
    --repo C:/Users/user/tmp/flask-tiny-repo \
    --questions harness/fixtures/flask-tiny/questions-flask-tiny.jsonl \
    --output harness/baselines/<v03-date>-code-graph-flask-tiny.jsonl

python3 harness/score.py \
    --results harness/baselines/<v03-date>-code-graph-flask-tiny.jsonl \
    --oracle  harness/fixtures/flask-tiny/oracle-flask-tiny.jsonl \
    --questions harness/fixtures/flask-tiny/questions-flask-tiny.jsonl
```

Expected after Pattern A: F004 recall@10 goes from 0.0 → 1.0
(`create_app` synthesized as inbound caller of `log_request`).
F001/F002 unchanged at 1.0. F003 unchanged at 0.0 (Pattern C
deferred). Aggregate recall@10 0.5 → 0.75.

---

## Pattern B (SECONDARY) — `@app.route('/path')` decorator with args

### Task B1: Synthesize CBMCall for route decorator registrations

**Finding:** `@auth_bp.route("/login", methods=["POST"])` above
`def login_view():` is a Python `decorator` node containing a `call`
expression. tree-sitter's call_node_types already include this call —
`extract_calls.c` visits it. Today the call emits one `CBMCall` with
`callee_name = "auth_bp.route"`, `enclosing_func_qn = ??` (see open
question below), `dispatch_kind = NULL`.

The function being decorated (`login_view`) is the *next sibling*
of the decorator node in the tree-sitter AST (or, equivalently, the
parent of the decorator is the `decorated_definition` whose other
child is the `function_definition`). The synthesized edge target is
the decorated function name.

**Open design question — `enclosing_func_qn` for module-level
decorator calls.** When the decorator is at module level (the
common case), the call's `enclosing_func_qn` is the module QN, not a
function. An inbound trace on `login_view` would then surface the
module — actionable in principle but not the most useful answer. Two
acceptable resolutions:

1. **Module-level emission (cheapest).** Set
   `enclosing_func_qn = <module_qn>`. Inbound trace returns "called
   from <module>"; the caller can grep `@*.route` to find the line.
2. **Self-referential emission.** Set
   `enclosing_func_qn = <decorated_function_qn>` (i.e. the function
   "registers itself" as a route handler). Inbound trace then
   surfaces the decorated function as its own caller — semantically
   weird but it makes the routing relationship discoverable as a
   self-edge.

Resolution (B1 task): pick option 1 in v0.3 and document the
decision. Self-edges are a graph-shape anti-pattern; module-level
edges are at least defensible. If neither feels right, defer
Pattern B to v0.4.

**Allowlist of route decorator method suffixes:**

| Suffix | Flask method | dispatch_kind label |
|---|---|---|
| `.route` | `Flask.route`, `Blueprint.route` | `"route_register"` |
| `.get` | `Flask.get` (Flask 2.0+ shortcut) | `"route_register"` |
| `.post` | `Flask.post` | `"route_register"` |
| `.put` | `Flask.put` | `"route_register"` |
| `.delete` | `Flask.delete` | `"route_register"` |
| `.patch` | `Flask.patch` | `"route_register"` |
| `.add_url_rule` | `Flask.add_url_rule` (non-decorator form, second arg is fn) | `"route_register"` |

For the decorator forms, the *target* of the synthesized call is the
decorated function (next sibling in tree-sitter). For
`add_url_rule`, the target is the second argument (the view function).

**Files:** Same set as Task A1, plus:
- `internal/cbm/extract_calls.c` — new emission block that walks
  upward from the call node to the enclosing `decorator` node, then
  to the `decorated_definition` parent, then locates the
  `function_definition` child to extract the decorated name.
- `internal/pipeline/pipeline_cbm.go::dispatchKindConfidence` —
  add `"route_register"` → `"high"`.

**Step 1 (CBM-level): Write the failing test**

```go
func TestPythonFlaskRouteDecoratorTracked(t *testing.T) {
    src := []byte(`
from flask import Blueprint

auth_bp = Blueprint("auth", __name__)

@auth_bp.route("/login", methods=["POST"])
def login_view():
    return "ok"
`)
    res := extractCalls(t, "routes.py", src, langPython)
    var synth *CBMCall
    for i := range res.Calls {
        if res.Calls[i].CalleeName == "login_view" &&
            res.Calls[i].DispatchKind == "route_register" {
            synth = &res.Calls[i]
            break
        }
    }
    if synth == nil {
        t.Fatalf("expected synthesized call to login_view with "+
            "dispatch_kind=route_register; got %d calls: %+v",
            len(res.Calls), res.Calls)
    }
}
```

**Step 2 (CBM-level): Sketch (full implementation in PR)**

```c
// Python: Flask route decorator — emit decorated function as a
// call target. INDIRECT_CALLS v0.3 Pattern B. Walks upward from
// the route call site to the enclosing decorated_definition, then
// down to the function_definition's name to get the registered
// handler. enclosing_func_qn intentionally set to the MODULE QN
// (option 1 in INDIRECT_CALLS_V0_3_PLAN.md — module-level
// emission is the chosen resolution).
if (ctx->language == CBM_LANG_PYTHON &&
    cbm_python_flask_route_callee(callee) != NULL) {
    TSNode dec = cbm_walk_up_to(node, "decorator");
    if (!ts_node_is_null(dec)) {
        TSNode dec_def = ts_node_parent(dec);
        if (!ts_node_is_null(dec_def) &&
            strcmp(ts_node_type(dec_def), "decorated_definition") == 0) {
            TSNode fn_def = cbm_find_child_of_type(dec_def, "function_definition");
            if (!ts_node_is_null(fn_def)) {
                TSNode name = ts_node_child_by_field_name(fn_def, "name", 4);
                if (!ts_node_is_null(name)) {
                    char* handler = cbm_node_text(ctx->arena, name, ctx->source);
                    if (handler && handler[0] && !cbm_is_keyword(handler, ctx->language)) {
                        CBMCall reg;
                        reg.callee_name = handler;
                        reg.enclosing_func_qn = ctx->module_qn;  // module-level
                        reg.dispatch_kind = "route_register";
                        cbm_calls_push(&ctx->result->calls, ctx->arena, reg);
                    }
                }
            }
        }
    }
}
```

The `cbm_walk_up_to` / `cbm_find_child_of_type` helpers may already
exist; if not, add them as small static helpers in the same file.

**Decision gate for Pattern B:** If the upward-walk + parent lookup
proves more invasive than expected (e.g., the existing AST walk
doesn't carry the parent pointers we need), STOP and roll Pattern B
to v0.4. Pattern A alone satisfies the success criterion.

---

## Pattern C (DEFERRED to v0.4) — `@login_required` functools.wraps closure

**Finding:** `@login_required` (no args) above `def login_view():`
applies `login_required(login_view)`, which returns a `wrapper`
closure. The wrapper calls `func(*args, **kwargs)` where `func` is
the closed-over parameter of the outer `login_required` function.
For an inbound trace on `login_view` to find `wrapper` as caller,
the extractor must resolve the closure-bound name `func` back to
`login_view` — i.e. intra-function scope tracking that the current
C extractor does not do.

This requires either:
1. A new scope-tracking pass at the C extractor (~1-2 weeks),
2. A Go-side post-processing pass that runs after the registry is
   built and walks `@<decorator> def <fn>:` decoration relationships
   to emit edges (~3-5 days but introduces a new Go pass),
3. A LSP-style binding pass for Python (similar to the Go LSP work
   referenced in `pipeline_cbm.go::runGoLSPCrossFileResolution`)
   that does intra-function symbol resolution (multi-week).

Per `scope-discipline.md` ("infrastructure investment requires
repeated friction evidence"): one decorator family is not enough
evidence to justify a new pass. v0.4 will revisit Pattern C once
either (a) the flask-tiny F003 question becomes blocking for a
specific consumer or (b) at least one additional pattern is found
that needs the same machinery.

flask-tiny F003 stays at recall=0.0 after v0.3 ships. The baseline
doc explicitly documents this as a known-failure-deferred condition.

---

## File-by-file change list

| File | Lines | Change |
|---|---|---|
| `internal/cbm/extract_calls.c` | ~310, ~464 | Add Pattern A emission blocks in both `walk_calls` and `handle_calls`. Add Pattern B emission block (single site; the unified handler is the production path). Add static helpers `cbm_python_flask_hook_label` and `cbm_python_flask_route_callee` (+ optional `cbm_walk_up_to` / `cbm_find_child_of_type` if not extant). |
| `internal/cbm/cbm.h` | 111-121 | Extend the `dispatch_kind` comment block to list the new labels (`before_request_hook`, `route_register`, etc.). NO struct schema change. |
| `internal/pipeline/pipeline_cbm.go` | 470-479 | Extend `dispatchKindConfidence` switch to map the new labels → `"high"`. |
| `internal/cbm/cbm_test.go` | new tests | `TestPythonFlaskBeforeRequestTracked` + symmetric tests for `.after_request` / `.errorhandler` / etc. `TestPythonFlaskRouteDecoratorTracked` for Pattern B (if shipped). |
| `internal/pipeline/dispatch_tag_test.go` | new tests | `TestDispatchKindConfidence_BeforeRequestHook` + per-label assertions for the new kinds. |
| `internal/pipeline/INDIRECT_CALLS_DESIGN.md` | updated 2026-05-23 | Already updated alongside this plan (PR refresh on `claude/ecstatic-pasteur-ebWx8`). |
| `CLAUDE.md` | top-of-file Resolver env vars table OR a new "Indirect dispatch synthesis" subsection | Document the v0.3 dispatch_kind label set so callers reading the CALLS edge properties know what to expect. Optional but recommended. |

**Files that do NOT change** (explicit non-list to forestall scope
creep):
- `internal/tools/trace.go` — `traceConfidenceBand` already handles
  resolved/unresolved-call-count math the same way for synthesized
  vs direct CALLS edges.
- `internal/store/edges.go` — no new edge type. Property addition
  on existing CALLS edges fits the existing schema.
- `internal/pipeline/pipeline.go::INDIRECT_CALLS re-type` (lines
  2074-2087) — distinct mechanism (non-callable target demotion),
  not touched by v0.3.

---

## Test plan

### CBM-level (extractor produces synthesized Call)

Minimum fixture per pattern. Each fixture is a single Python file
under `internal/cbm/testdata/` (or inline in the `_test.go` if the
existing convention uses inline sources).

1. **Pattern A**: Flask app with `app.before_request(log_request)`.
   Expect: 3 `CBMCall` entries (`Flask(__name__)`, `app.before_request(...)`,
   synthesized `log_request` with `dispatch_kind="before_request_hook"`).
2. **Pattern A negative**: `app.before_request(lambda: ...)`. The
   first arg is not a bare identifier — no synthesized call.
3. **Pattern A allowlist**: `bp.after_request(log_response)`. Synthesized
   call with `dispatch_kind="after_request_hook"`.
4. **Pattern B**: `@auth_bp.route("/login")` above `def login_view():`.
   Synthesized call to `login_view` with `dispatch_kind="route_register"`.
5. **Pattern B negative**: `@some.thing("/p")` (not a Flask route).
   Allowlist excludes; no synthesized call.
6. **Existing-pattern regression**: a fixture with `executor.submit(fn)`
   continues to produce the v0.1 synthesized call. Pin via existing
   `TestPythonExecutorSubmitTracked` (and similar).

### Pipeline-level (resulting CALLS edges land correctly)

7. Pipeline-level test that indexes a tiny fixture (Pattern A + B
   sources from above) and asserts CALLS edge with
   `properties.dispatch_kind = "before_request_hook"` exists from
   `<module>.create_app` to `<module>.log_request`, with
   `properties.confidence = "high"`.
8. `dispatchKindConfidence` switch coverage per label
   (`TestDispatchKindConfidence_BeforeRequestHook` etc).

### Regression: flask-tiny baseline (binary gate)

9. Run `harness/runners/run_code_graph_trace.py` against the v0.3
   build (see Pattern A Step 5 invocation). Expected:
   - F001 / F002 recall@10 = 1.0 (no regression on direct calls)
   - F003 recall@10 = 0.0 (Pattern C deferred)
   - F004 recall@10 = 1.0 (Pattern A succeeded)
   - Aggregate recall@10 = 0.75 (3 of 4)

10. Loc-Bench regression: re-run `bench/research/eval_locbench_*.py`
    at iter=2; confirm file=86.0% / class=84.5% / func=73.5% baseline
    holds within ±2pp sampling noise (per CLAUDE.md methodology
    caveats — bootstrap CI on n=200 is wide).

---

## Success criteria (priority order)

1. **flask-tiny F004 recall@10 ≥ 0.5** (Pattern A landed). This is
   the binary gate; nothing else ships without this.
2. **flask-tiny F001 + F002 recall@10 = 1.0** (no sanity-check
   regression).
3. **No regression on existing CBM unit tests** —
   `TestPythonExecutorSubmitTracked`, the getattr tests, the Depends
   tests all pass unchanged. Pattern A/B emission is additive on the
   call-walk, not a rewrite of existing branches.
4. **No regression on existing Loc-Bench iter=2 baseline**
   (file=86.0, class=84.5, func=73.5; ±2pp tolerance per CLAUDE.md
   methodology caveats).
5. **(Stretch) flask-tiny F003 recall@10 ≥ 0.5** — only if Pattern
   C lands in this PR, which is NOT planned. F003 staying at 0.0 is
   acceptable per the deferred-to-v0.4 framing.

---

## Risks

1. **Risk:** False positives from over-broad pattern matching —
   e.g., `my_thread_pool.before_request(some_log_fn)` not Flask.
   **Mitigation:** The suffix allowlist is intentionally narrow (7
   exact Flask hook names + the route variants). Other matches don't
   trigger emission. If precision is a concern downstream, the Go
   resolver still drops the synthesized edge when the target isn't
   resolvable to a Function/Method node, so false-positive blast
   radius is bounded.

2. **Risk:** `@login_required`-style closure resolution is harder
   than Pattern A/B and inflates v0.3 scope. **Mitigation:** Pattern
   C is explicitly deferred to v0.4 in this plan; the design doc
   carries the deferral. flask-tiny F003 stays at recall=0.0 and
   the baseline doc documents this as acknowledged-incomplete.

3. **Risk:** `dispatch_kind` property accumulation on CALLS edges
   could complicate downstream queries that filter by
   `properties.confidence`. **Mitigation:** Same as v0.1 — emission
   adds property values from a known small set; downstream readers
   (e.g., `trace_call_path::traceConfidenceBand`) only consult the
   existing `unresolved_call_count` / resolved-count math, which
   doesn't change.

4. **Risk:** Pattern B's `enclosing_func_qn = module_qn` framing
   produces inbound trace results that are less useful than the
   user expects ("called from <module>" instead of "from a
   framework registration"). **Mitigation:** Document the design
   choice prominently in the dispatch_kind comment block in
   `cbm.h`. If user feedback indicates the module-level edge is
   unhelpful, the alternative (decorated-function self-edge) is a
   one-line change in Pattern B's emission block.

5. **Risk:** Tree-sitter Python grammar may not surface the
   `decorated_definition` node shape the plan assumes for Pattern B.
   **Mitigation:** Pre-verify by running an AST dump (e.g.
   `internal/pipeline/asttest`-style test) on a fixture before
   writing the emission code. If the grammar shape differs, the
   Pattern B emission strategy needs adjustment — and per the
   in-plan decision gate, defer Pattern B to v0.4 rather than
   inflating scope.

---

## Non-goals

1. **NOT addressing FastAPI** (`@app.get`, `Depends`-deeper-than-v0.1).
   FastAPI's `Depends(fn)` is already v0.1-handled. FastAPI's
   path-method decorators (`@app.get("/p")`) are structurally
   similar to Flask `@app.get` shortcut and would be covered if the
   Pattern B allowlist included FastAPI's app + router objects —
   but that's a v0.4 expansion, not a v0.3 scope item.
2. **NOT addressing class-based views** (Flask-RESTful `Resource`
   subclasses, Django CBVs). These dispatch through `as_view()` and
   request a different pattern. Defer.
3. **NOT retyping CALLS to INDIRECT_CALLS edges.** v0.3 keeps the
   v0.1 convention: synthesized call → `CALLS` edge with
   `properties.dispatch_kind` + `properties.confidence`. The
   separate `INDIRECT_CALLS` edge re-type at
   `pipeline.go:2074-2087` is untouched.
4. **NOT adding new C-extractor data structures.** `CBMCall` schema
   is unchanged. New helper functions allowed if they're static and
   live next to the emission sites.
5. **NOT shipping the v0.4 closure-resolution pass.** Pattern C is
   explicitly deferred.

---

## Cross-references

- Design doc (updated alongside this plan):
  `internal/pipeline/INDIRECT_CALLS_DESIGN.md`
- Architectural precedent (executor.submit Call-synthesis):
  `internal/cbm/extract_calls.c:286-309` (walk_calls),
  `internal/cbm/extract_calls.c:441-464` (handle_calls)
- CBMCall.dispatch_kind field:
  `internal/cbm/cbm.h:108-121`
- Go-side tag/confidence wiring:
  `internal/pipeline/pipeline_cbm.go:432-479`
- Regression gate baseline:
  a private Markdown knowledge-base repo →
  `harness/baselines/2026-05-23-code-graph-flask-tiny-baseline.md`
- Regression-gate runner:
  a private Markdown knowledge-base repo →
  `harness/runners/run_code_graph_trace.py`
