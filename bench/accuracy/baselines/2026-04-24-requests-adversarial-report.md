# code-graph accuracy baseline — requests-adversarial

- **Date**: 2026-04-24
- **Fixture SHA**: `f43f750ee10addbaa13901c1017631b4d70fd6e3` (short: `f43f750`)
- **Project name**: `c-Users-user-Documents-bench-fixtures-requests`

## Summary

Four metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached).
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|
| CALLS | pycg | 194 / 238 | 0.361 / 0.443 / 0.398 | 0.804 / 0.443 / 0.571 | 0.804 / 0.443 / 0.571 |
| IMPORTS | ast | 55 / 26 | 0.077 / 0.036 / 0.049 | 0.100 / 0.036 / 0.053 | 0.100 / 0.036 / 0.053 |
| HTTP_CALLS | opus+sonnet (not yet run) | — / — | — (pending) | — | — |

## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 58

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  src.requests.api.request --> src.requests.api.request
  src.requests.models.PreparedRequest.copy --> src.requests.models.PreparedRequest.copy
  src.requests.models.PreparedRequest.prepare_auth --> src.requests.cookies.RequestsCookieJar.update
  src.requests.models.PreparedRequest.prepare_body --> tests.certs.expired.Makefile.all
  src.requests.models.PreparedRequest.prepare_content_length --> src.requests.api.get
  src.requests.models.PreparedRequest.prepare_headers --> src.requests.cookies.RequestsCookieJar.items
  src.requests.models.Request.__init__ --> src.requests.cookies.RequestsCookieJar.items
  src.requests.sessions.Session.get_adapter --> src.requests.cookies.RequestsCookieJar.items
  src.requests.sessions.Session.merge_environment_settings --> src.requests.cookies.RequestsCookieJar.items
  src.requests.sessions.Session.merge_environment_settings --> src.requests.sessions.Session.get
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  src.requests.api.request --> src.requests.sessions.Session
  src.requests.models.PreparedRequest.copy --> src.requests.models.PreparedRequest.__init__
  src.requests.models.PreparedRequest.copy --> src.requests.structures.CaseInsensitiveDict.copy
  src.requests.models.PreparedRequest.prepare_auth --> src.requests.auth.HTTPBasicAuth
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.encode
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.encode.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.builtin_str
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps.encode
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  src.requests --> src.requests._check_cryptography
  src.requests --> src.requests.check_compatibility
  src.requests.adapters.HTTPAdapter.__init__ --> src.requests.adapters.HTTPAdapter.init_poolmanager
  src.requests.adapters.HTTPAdapter.__setstate__ --> src.requests.adapters.HTTPAdapter.init_poolmanager
  src.requests.adapters.HTTPAdapter.__setstate__ --> src.requests.cookies.RequestsCookieJar.items
  src.requests.adapters.HTTPAdapter.build_connection_pool_key_attributes --> src.requests.adapters._urllib3_request_context
  src.requests.adapters.HTTPAdapter.build_response --> src.requests.cookies.extract_cookies_to_jar
  src.requests.adapters.HTTPAdapter.build_response --> src.requests.utils.get_encoding_from_headers
  src.requests.adapters.HTTPAdapter.close --> src.requests.cookies.RequestsCookieJar.values
  src.requests.adapters.HTTPAdapter.get_connection --> src.requests.adapters.HTTPAdapter.proxy_manager_for
```

**Raw-exact false negatives**:
```
  src.requests.api.request --> src.requests.sessions.Session
  src.requests.models.PreparedRequest.copy --> src.requests.models.PreparedRequest.__init__
  src.requests.models.PreparedRequest.copy --> src.requests.structures.CaseInsensitiveDict.copy
  src.requests.models.PreparedRequest.prepare_auth --> src.requests.auth.HTTPBasicAuth
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.encode
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.encode.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.builtin_str
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps.encode
```

### IMPORTS

Oracle analyzed callers: 17

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  tests.test_lowlevel --> src.requests
  tests.test_lowlevel --> src.requests.compat
  tests.test_requests --> src.requests
  tests.test_requests --> src.requests.adapters
  tests.test_requests --> src.requests.auth
  tests.test_requests --> src.requests.compat
  tests.test_requests --> src.requests.cookies
  tests.test_requests --> src.requests.exceptions
  tests.test_requests --> src.requests.hooks
  tests.test_requests --> src.requests.models
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  src.requests._internal_utils --> src.requests.compat
  src.requests.adapters --> src.requests.auth
  src.requests.adapters --> src.requests.compat
  src.requests.adapters --> src.requests.cookies
  src.requests.adapters --> src.requests.exceptions
  src.requests.adapters --> src.requests.models
  src.requests.adapters --> src.requests.structures
  src.requests.adapters --> src.requests.utils
  src.requests.api --> src.requests
  src.requests.auth --> src.requests._internal_utils
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  tests.conftest --> src.requests.compat
  tests.test_adapters --> src.requests.adapters
  tests.test_help --> src.requests.help
  tests.test_hooks --> src.requests.hooks
  tests.test_lowlevel --> src.requests
  tests.test_lowlevel --> src.requests.compat
  tests.test_packages --> src.requests
  tests.test_requests --> src.requests
  tests.test_requests --> src.requests.adapters
  tests.test_requests --> src.requests.auth
```

**Raw-exact false negatives**:
```
  src.requests._internal_utils --> src.requests.compat
  src.requests.adapters --> src.requests.auth
  src.requests.adapters --> src.requests.compat
  src.requests.adapters --> src.requests.cookies
  src.requests.adapters --> src.requests.exceptions
  src.requests.adapters --> src.requests.models
  src.requests.adapters --> src.requests.structures
  src.requests.adapters --> src.requests.utils
  src.requests.api --> src.requests
  src.requests.auth --> src.requests._internal_utils
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).