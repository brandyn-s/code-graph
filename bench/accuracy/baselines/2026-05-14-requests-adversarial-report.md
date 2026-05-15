# code-graph accuracy baseline — requests-adversarial

- **Date**: 2026-05-14
- **Fixture SHA**: `f43f750ee10addbaa13901c1017631b4d70fd6e3` (short: `f43f750`)
- **Project name**: `c-Users-user-Documents-bench-fixtures-requests`

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
| CALLS | pycg | 194 / 282 | 0.394 / 0.572 / 0.466 | 0.841 / 0.572 / 0.681 | 0.841 / 0.572 / 0.681 | 0.841 / 0.572 / 0.681 |
| IMPORTS | ast | 55 / 151 | 0.086 / 0.236 / 0.126 | 0.106 / 0.236 / 0.146 | 0.106 / 0.236 / 0.146 | 0.106 / 0.236 / 0.146 |
| HTTP_CALLS | opus+sonnet (not yet run) | — / — | — (pending) | — | — |

## Caller-kind stratified precision

Each CALLS edge is tagged with the AST scope of its caller (`function-body`, `method-body`, `file-block`, `package-init-block`, `var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The harness reads this property and stratifies precision by it. The **ghost-caller FP rate** is the share of FPs whose caller is a package-level scope rather than a real function/method — alarms above 5%.

### CALLS

| Kind | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `method-body` | 101 | 16 | 0.863 | 117 |

**Package-block caller FP rate**: 0.0000 (0 of 21 FPs)

**Caller-kind complement legitimacy** (function/method-body share of all scope-aligned edges): 0.6140 (132 of 215)

### IMPORTS

> Note: caller_node_kind not yet emitted by the indexed binary; metrics skipped.


## Janusian ambiguity stratified precision

Each CALLS edge carries the resolver's pre-tie-break candidate cardinality (`candidate_set_size`). A call site with >= 2 candidates is **Janusian** — the resolver picked among alternatives. Step 2's LLM-Judge taxonomy predicted `same_named_method_disambiguation` (60% of judged FPs) concentrates on Janusian sites; the precision split below tests that hypothesis on real-fixture data. LSP-resolved edges carry size=1 by definition (LSP returns one target without enumerating alternates), so the Janusian signal lives in the registry strategies.

### CALLS

**method_set_ambiguity_index** — share of call sites with >= 2 candidates:

| Project | Ambiguous sites | Total sites | Index |
|---|---:|---:|---:|
| __all__ | 7 | 55 | 0.1273 |

**janusian_site_precision_split** — precision conditional on call-site ambiguity:

| Bucket | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `ambiguous` | 0 | 7 | 0.0000 | 7 |
| `unambiguous` | 111 | 14 | 0.8880 | 125 |

**janusian_precision_gap** (unambiguous − ambiguous precision): +0.8880. Positive = unambiguous sites resolve more accurately, consistent with Step 2's prediction. Negative or near-zero = ambiguity is not the dominant FP driver.


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
| real_only | 282 | 0.394 / 0.572 / 0.466 | 0.396 / 0.572 / 0.468 | 0.841 / 0.572 / 0.681 |
| real_plus_external | 282 | 0.394 / 0.572 / 0.466 | 0.396 / 0.572 / 0.468 | 0.841 / 0.572 / 0.681 |
| real_plus_pseudo | 282 | 0.394 / 0.572 / 0.466 | 0.396 / 0.572 / 0.468 | 0.841 / 0.572 / 0.681 |
| all_calls_family | 282 | 0.394 / 0.572 / 0.466 | 0.396 / 0.572 / 0.468 | 0.841 / 0.572 / 0.681 |

Diverging rows expose how each non-real population dilutes the aggregate. Most accuracy regressions live in `real_only`; the other rows are diagnostic.

## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 58

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  src.requests.api.request --> src.requests.sessions.Session.request
  src.requests.models.PreparedRequest.copy --> src.requests.models.PreparedRequest
  src.requests.models.PreparedRequest.prepare_auth --> src.requests.cookies.RequestsCookieJar.update
  src.requests.models.PreparedRequest.prepare_content_length --> src.requests.api.get
  src.requests.models.PreparedRequest.prepare_headers --> src.requests.cookies.RequestsCookieJar.items
  src.requests.models.Request.__init__ --> src.requests.cookies.RequestsCookieJar.items
  src.requests.models.Request.prepare --> src.requests.models.PreparedRequest
  src.requests.models.Response.links --> src.requests.api.get
  src.requests.sessions.Session.get_adapter --> src.requests.cookies.RequestsCookieJar.items
  src.requests.sessions.Session.merge_environment_settings --> src.requests.cookies.RequestsCookieJar.get
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  src.requests.models.PreparedRequest.copy --> src.requests.models.PreparedRequest.__init__
  src.requests.models.PreparedRequest.copy --> src.requests.structures.CaseInsensitiveDict.copy
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.encode
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.encode.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.builtin_str
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps.encode
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps.encode.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps.tell
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
  src.requests.adapters.HTTPAdapter.build_response --> src.requests.models.Response
  src.requests.adapters.HTTPAdapter.build_response --> src.requests.structures.CaseInsensitiveDict
  src.requests.adapters.HTTPAdapter.build_response --> src.requests.utils.get_encoding_from_headers
```

**Raw-exact false negatives**:
```
  src.requests.models.PreparedRequest.copy --> src.requests.models.PreparedRequest.__init__
  src.requests.models.PreparedRequest.copy --> src.requests.structures.CaseInsensitiveDict.copy
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.encode
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.encode.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests._internal_utils.to_native_string.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.builtin_str
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps.encode
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps.encode.tell
  src.requests.models.PreparedRequest.prepare_body --> src.requests.compat.json.dumps.tell
```

### IMPORTS

Oracle analyzed callers: 17

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  src.requests._internal_utils --> src.requests.compat.builtin_str
  src.requests.adapters --> src.requests.auth._basic_auth_str
  src.requests.adapters --> src.requests.compat.basestring
  src.requests.adapters --> src.requests.cookies.extract_cookies_to_jar
  src.requests.adapters --> src.requests.exceptions.ConnectTimeout
  src.requests.adapters --> src.requests.exceptions.ConnectionError
  src.requests.adapters --> src.requests.exceptions.InvalidHeader
  src.requests.adapters --> src.requests.exceptions.InvalidProxyURL
  src.requests.adapters --> src.requests.exceptions.InvalidSchema
  src.requests.adapters --> src.requests.exceptions.InvalidURL
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  src.requests._internal_utils --> src.requests.compat
  src.requests.adapters --> src.requests.auth
  src.requests.adapters --> src.requests.cookies
  src.requests.adapters --> src.requests.exceptions
  src.requests.adapters --> src.requests.models
  src.requests.adapters --> src.requests.structures
  src.requests.adapters --> src.requests.utils
  src.requests.api --> src.requests
  src.requests.auth --> src.requests._internal_utils
  src.requests.auth --> src.requests.cookies
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  src.requests._internal_utils --> src.requests.compat.builtin_str
  src.requests.adapters --> src.requests.auth._basic_auth_str
  src.requests.adapters --> src.requests.compat.basestring
  src.requests.adapters --> src.requests.cookies.extract_cookies_to_jar
  src.requests.adapters --> src.requests.exceptions.ConnectTimeout
  src.requests.adapters --> src.requests.exceptions.ConnectionError
  src.requests.adapters --> src.requests.exceptions.InvalidHeader
  src.requests.adapters --> src.requests.exceptions.InvalidProxyURL
  src.requests.adapters --> src.requests.exceptions.InvalidSchema
  src.requests.adapters --> src.requests.exceptions.InvalidURL
```

**Raw-exact false negatives**:
```
  src.requests._internal_utils --> src.requests.compat
  src.requests.adapters --> src.requests.auth
  src.requests.adapters --> src.requests.cookies
  src.requests.adapters --> src.requests.exceptions
  src.requests.adapters --> src.requests.models
  src.requests.adapters --> src.requests.structures
  src.requests.adapters --> src.requests.utils
  src.requests.api --> src.requests
  src.requests.auth --> src.requests._internal_utils
  src.requests.auth --> src.requests.cookies
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).