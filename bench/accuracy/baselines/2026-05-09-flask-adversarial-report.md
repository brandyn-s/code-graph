# code-graph accuracy baseline — flask-adversarial

- **Date**: 2026-05-09
- **Fixture SHA**: `2ac89889f4cc330eabd50f295dcef02828522c69` (short: `2ac8988`)
- **Project name**: `c-Users-user-Documents-bench-fixtures-flask`

## Summary

Five metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned (all bands)**: restricted to edges whose caller is in the oracle's analyzed-caller set, INCLUDING speculative-janusian band emissions (Janusian-ambiguous cross-package edges). Reflects the recall-friendly operating point — what consumers see by default if they don't filter `confidence_band`.
- **Scope-aligned (high-confidence)**: same scope, EXCLUDING `confidence_band=speculative-janusian` edges. Reflects the precision-friendly operating point for consumers who filter to high-trust edges only.
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

Two operating points are reported because the resolver emits Janusian-ambiguous edges (multiple cross-package candidates with same simple name) at the speculative-janusian band rather than dropping them. Find-one-function-reliably queries should filter to high-confidence; blast-radius / impact-analysis queries should use all-bands.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 (all) | Scope-aligned P/R/F1 (high-conf) | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|---|
| CALLS | pycg | 93 / 302 | 0.146 / 0.473 / 0.223 | 0.512 / 0.473 / 0.492 | 0.512 / 0.473 / 0.492 | 0.512 / 0.473 / 0.492 |
| IMPORTS | ast | 97 / 219 | 0.055 / 0.124 / 0.076 | 0.089 / 0.124 / 0.103 | 0.089 / 0.124 / 0.103 | 0.089 / 0.124 / 0.103 |
| HTTP_CALLS | opus+sonnet (not yet run) | — / — | — (pending) | — | — |

## Caller-kind stratified precision

Each CALLS edge is tagged with the AST scope of its caller (`function-body`, `method-body`, `file-block`, `package-init-block`, `var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The harness reads this property and stratifies precision by it. The **ghost-caller FP rate** is the share of FPs whose caller is a package-level scope rather than a real function/method — alarms above 5%.

### CALLS

| Kind | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `method-body` | 37 | 35 | 0.514 | 72 |

**Package-block caller FP rate**: 0.0238 (1 of 42 FPs)

**Caller-kind complement legitimacy** (function/method-body share of all scope-aligned edges): 0.6222 (84 of 135)

### IMPORTS

> Note: caller_node_kind not yet emitted by the indexed binary; metrics skipped.


## Janusian ambiguity stratified precision

Each CALLS edge carries the resolver's pre-tie-break candidate cardinality (`candidate_set_size`). A call site with >= 2 candidates is **Janusian** — the resolver picked among alternatives. Step 2's LLM-Judge taxonomy predicted `same_named_method_disambiguation` (60% of judged FPs) concentrates on Janusian sites; the precision split below tests that hypothesis on real-fixture data. LSP-resolved edges carry size=1 by definition (LSP returns one target without enumerating alternates), so the Janusian signal lives in the registry strategies.

### CALLS

**method_set_ambiguity_index** — share of call sites with >= 2 candidates:

| Project | Ambiguous sites | Total sites | Index |
|---|---:|---:|---:|
| __all__ | 9 | 36 | 0.2500 |

**janusian_site_precision_split** — precision conditional on call-site ambiguity:

| Bucket | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `ambiguous` | 0 | 11 | 0.0000 | 11 |
| `unambiguous` | 44 | 31 | 0.5867 | 75 |

**janusian_precision_gap** (unambiguous − ambiguous precision): +0.5867. Positive = unambiguous sites resolve more accurately, consistent with Step 2's prediction. Negative or near-zero = ambiguity is not the dominant FP driver.


## CALLS modal split — by edge-kind union

After PR #121, the CALLS family is partitioned into:
- `CALLS` — real-to-real (precision-relevant)
- `CALLS_EXTERNAL` — real-to-stub (LSP-resolved external)
- `CALLS_PSEUDO` — synthetic module-default caller

Each row below recomputes precision/recall/F1 against the same
PyCG oracle but with a different union on the measured side.
Headline `results.CALLS` is the `real_only` row.

| Union | Measured | Exact P/R/F1 | Suffix-3 P/R/F1 | Scope-aligned P/R/F1 |
|---|---|---|---|---|
| real_only | 302 | 0.146 / 0.473 / 0.223 | 0.146 / 0.473 / 0.223 | 0.512 / 0.473 / 0.492 |
| real_plus_external | 302 | 0.146 / 0.473 / 0.223 | 0.146 / 0.473 / 0.223 | 0.512 / 0.473 / 0.492 |
| real_plus_pseudo | 302 | 0.146 / 0.473 / 0.223 | 0.146 / 0.473 / 0.223 | 0.512 / 0.473 / 0.492 |
| all_calls_family | 302 | 0.146 / 0.473 / 0.223 | 0.146 / 0.473 / 0.223 | 0.512 / 0.473 / 0.492 |

Diverging rows expose how each non-real population dilutes the aggregate. Most accuracy regressions live in `real_only`; the other rows are diagnostic.

## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 46

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  src.flask.app.Flask.__init__ --> src.flask.app.Flask.send_static_file
  src.flask.app.Flask.__init__ --> src.flask.sansio.app.App.add_url_rule
  src.flask.app.Flask.handle_exception --> src.flask.sansio.app.App._find_error_handler
  src.flask.app.Flask.handle_http_exception --> src.flask.sansio.app.App._find_error_handler
  src.flask.app.Flask.handle_user_exception --> src.flask.sansio.app.App._find_error_handler
  src.flask.app.Flask.handle_user_exception --> src.flask.sansio.app.App.trap_http_exception
  src.flask.app.Flask.make_default_options_response --> examples.tutorial.flaskr.blog.update
  src.flask.app.Flask.make_response --> examples.tutorial.flaskr.blog.update
  src.flask.app.Flask.make_response --> src.flask.json.provider.JSONProvider.response
  src.flask.app.Flask.process_response --> src.flask.ctx.AppContext._get_session
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  src.flask.app --> src.flask.typing.TypeVar
  src.flask.app.Flask --> src.flask.sessions.SecureCookieSessionInterface
  src.flask.app.Flask.__init__ --> src.flask.cli.AppGroup.AppGroup
  src.flask.app.Flask.app_context --> src.flask.ctx.AppContext
  src.flask.app.Flask.do_teardown_appcontext --> src.flask.helpers._CollectErrors
  src.flask.app.Flask.do_teardown_appcontext --> src.flask.signals.appcontext_tearing_down.send
  src.flask.app.Flask.do_teardown_request --> src.flask.helpers._CollectErrors
  src.flask.app.Flask.do_teardown_request --> src.flask.signals.request_tearing_down.send
  src.flask.app.Flask.finalize_request --> src.flask.signals.request_finished.send
  src.flask.app.Flask.full_dispatch_request --> src.flask.signals.request_started.send
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  examples.celery.src.task_app.create_app --> examples.celery.src.task_app.celery_init_app
  examples.celery.src.task_app.views.add --> src.flask.ctx._AppCtxGlobals.get
  examples.celery.src.task_app.views.process --> src.flask.ctx._AppCtxGlobals.get
  src.flask.app.Flask.__init__ --> src.flask.app.Flask.send_static_file
  src.flask.app.Flask.__init__ --> src.flask.sansio.app.App.add_url_rule
  src.flask.app.Flask.create_jinja_environment --> examples.tutorial.flaskr.blog.update
  src.flask.app.Flask.handle_exception --> src.flask.sansio.app.App._find_error_handler
  src.flask.app.Flask.handle_http_exception --> src.flask.sansio.app.App._find_error_handler
  src.flask.app.Flask.handle_user_exception --> src.flask.sansio.app.App._find_error_handler
  src.flask.app.Flask.handle_user_exception --> src.flask.sansio.app.App.trap_http_exception
```

**Raw-exact false negatives**:
```
  src.flask.app --> src.flask.typing.TypeVar
  src.flask.app.Flask --> src.flask.sessions.SecureCookieSessionInterface
  src.flask.app.Flask.__init__ --> src.flask.cli.AppGroup.AppGroup
  src.flask.app.Flask.app_context --> src.flask.ctx.AppContext
  src.flask.app.Flask.do_teardown_appcontext --> src.flask.helpers._CollectErrors
  src.flask.app.Flask.do_teardown_appcontext --> src.flask.signals.appcontext_tearing_down.send
  src.flask.app.Flask.do_teardown_request --> src.flask.helpers._CollectErrors
  src.flask.app.Flask.do_teardown_request --> src.flask.signals.request_tearing_down.send
  src.flask.app.Flask.finalize_request --> src.flask.signals.request_finished.send
  src.flask.app.Flask.full_dispatch_request --> src.flask.signals.request_started.send
```

### IMPORTS

Oracle analyzed callers: 26

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  examples.celery.src.task_app.views --> src.flask
  examples.javascript.js_example.views --> examples.javascript.js_example.app
  examples.javascript.js_example.views --> src.flask
  examples.tutorial.flaskr.auth --> examples.tutorial.flaskr.db.get_db
  examples.tutorial.flaskr.auth --> src.flask
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.auth.login_required
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.db.get_db
  examples.tutorial.flaskr.blog --> src.flask
  src.flask.__main__ --> src.flask.cli.main
  src.flask.app --> src.flask.ctx.AppContext
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  examples.celery.src.task_app.views --> examples.celery.src.task_app
  examples.javascript.js_example.views --> examples.javascript.js_example
  examples.tutorial.flaskr.auth --> examples.tutorial.flaskr.db
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.auth
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.db
  src.flask.__main__ --> src.flask.cli
  src.flask.app --> src.flask
  src.flask.app --> src.flask.ctx
  src.flask.app --> src.flask.debughelpers
  src.flask.app --> src.flask.globals
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  examples.celery.make_celery --> examples.celery.src.task_app
  examples.celery.src.task_app --> src.flask
  examples.celery.src.task_app.views --> src.flask
  examples.javascript.js_example --> examples.javascript.js_example.views
  examples.javascript.js_example --> src.flask
  examples.javascript.js_example.views --> examples.javascript.js_example.app
  examples.javascript.js_example.views --> src.flask
  examples.javascript.tests.conftest --> examples.javascript.js_example
  examples.javascript.tests.test_js_example --> src.flask
  examples.tutorial.flaskr --> src.flask
```

**Raw-exact false negatives**:
```
  examples.celery.src.task_app.views --> examples.celery.src.task_app
  examples.javascript.js_example.views --> examples.javascript.js_example
  examples.tutorial.flaskr.auth --> examples.tutorial.flaskr.db
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.auth
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.db
  src.flask.__main__ --> src.flask.cli
  src.flask.app --> src.flask
  src.flask.app --> src.flask.ctx
  src.flask.app --> src.flask.debughelpers
  src.flask.app --> src.flask.globals
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).