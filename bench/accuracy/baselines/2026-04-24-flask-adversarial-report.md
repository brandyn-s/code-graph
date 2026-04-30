# code-graph accuracy baseline — flask-adversarial

- **Date**: 2026-04-24
- **Fixture SHA**: `2ac89889f4cc330eabd50f295dcef02828522c69` (short: `2ac8988`)
- **Project name**: `c-Users-user-Documents-bench-fixtures-flask`

## Summary

Four metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached).
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|
| CALLS | pycg | 93 / 278 | 0.180 / 0.538 / 0.270 | 0.543 / 0.538 / 0.540 | 0.543 / 0.538 / 0.540 |
| IMPORTS | ast | 97 / 48 | 0.042 / 0.021 / 0.028 | 0.333 / 0.021 / 0.039 | 0.333 / 0.021 / 0.039 |
| HTTP_CALLS | opus+sonnet (not yet run) | — / — | — (pending) | — | — |

## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 46

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  src.flask.app.Flask.__init__ --> src.flask.app.Flask.send_static_file
  src.flask.app.Flask.__init__ --> src.flask.sansio.blueprints.BlueprintSetupState.add_url_rule
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
  examples.celery.src.task_app.create_app --> src.flask.config.Config.from_mapping
  examples.celery.src.task_app.create_app --> src.flask.config.Config.from_prefixed_env
  examples.celery.src.task_app.views --> src.flask.ctx._AppCtxGlobals.get
  examples.celery.src.task_app.views.add --> src.flask.ctx._AppCtxGlobals.get
  examples.celery.src.task_app.views.process --> src.flask.ctx._AppCtxGlobals.get
  examples.celery.src.task_app.views.result --> src.flask.ctx._AppCtxGlobals.get
  src.flask.__main__ --> src.flask.cli.main
  src.flask.app.Flask.__init__ --> src.flask.app.Flask.send_static_file
  src.flask.app.Flask.__init__ --> src.flask.sansio.blueprints.BlueprintSetupState.add_url_rule
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
  src.flask.config --> src.flask.json
  src.flask.json.provider --> src.flask.json
  src.flask.logging --> src.flask.logging
  tests.test_json --> src.flask.json.provider
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  examples.celery.src.task_app.views --> examples.celery.src.task_app
  examples.celery.src.task_app.views --> examples.celery.src.task_app.tasks
  examples.javascript.js_example.views --> examples.javascript.js_example
  examples.tutorial.flaskr.auth --> examples.tutorial.flaskr.db
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.auth
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.db
  src.flask.__main__ --> src.flask.cli
  src.flask.app --> src.flask
  src.flask.app --> src.flask.cli
  src.flask.app --> src.flask.ctx
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  examples.celery.make_celery --> examples.celery.src.task_app
  examples.javascript.js_example --> examples.javascript.js_example.views
  examples.javascript.tests.conftest --> examples.javascript.js_example
  examples.tutorial.tests.conftest --> examples.tutorial.flaskr
  examples.tutorial.tests.conftest --> examples.tutorial.flaskr.db
  examples.tutorial.tests.test_auth --> examples.tutorial.flaskr.db
  examples.tutorial.tests.test_blog --> examples.tutorial.flaskr.db
  examples.tutorial.tests.test_db --> examples.tutorial.flaskr.db
  examples.tutorial.tests.test_factory --> examples.tutorial.flaskr
  src.flask.config --> src.flask.json
```

**Raw-exact false negatives**:
```
  examples.celery.src.task_app.views --> examples.celery.src.task_app
  examples.celery.src.task_app.views --> examples.celery.src.task_app.tasks
  examples.javascript.js_example.views --> examples.javascript.js_example
  examples.tutorial.flaskr.auth --> examples.tutorial.flaskr.db
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.auth
  examples.tutorial.flaskr.blog --> examples.tutorial.flaskr.db
  src.flask.__main__ --> src.flask.cli
  src.flask.app --> src.flask
  src.flask.app --> src.flask.cli
  src.flask.app --> src.flask.ctx
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).