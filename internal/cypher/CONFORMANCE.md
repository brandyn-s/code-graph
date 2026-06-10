# Cypher Conformance Corpus (Plan 1 Phase D2)

The roundtable's single-source finding (Opus, partially conceded by
Grok and GPT): "Custom read-only Cypher subset has no published-corpus
differential testing — query-planner drift could be silent."

## Scope of code-graph's Cypher subset

code-graph implements a **read-only subset of openCypher** for graph
queries against the indexed code graph. Supported features:

- `MATCH (n:Label)` — node patterns with optional label
- `WHERE` — comparison ops (=, <>, <, >, <=, >=), regex (=~), STARTS WITH, ENDS WITH, CONTAINS, AND/OR (one operator per clause — mixing is rejected, no precedence), IS NULL, IS NOT NULL, IN [list]
- `RETURN` — projection, COUNT(*), DISTINCT, ORDER BY, LIMIT
- Variable-length paths: `(a)-[:REL*1..3]->(b)`, including `*0..N` (zero-length path binds the start node) and untyped `[*1..3]` (traverses all edge types)
- Directional relationships: `-[:REL]->`, `<-[:REL]-`, `-[:REL]-`
- Multiple relationship types: `-[:REL_A|REL_B]->`
- Edge property filters: `r.confidence`, `r.url_path`, etc.

Known dialect deviations from openCypher (deliberate, documented):

- `*N` is shorthand for `*1..N`, NOT openCypher's "exactly N hops".
- `OFFSET` / `SKIP` are not implemented (an earlier revision of this doc
  claimed OFFSET; that was doc drift — it never parsed).
- Variable-length expansion returns reachable nodes per BFS (deduped),
  not one row per distinct path, and does not bind a relationship
  variable.

**Explicitly NOT supported** (out of scope for read-only graph queries):

- Write operations: `CREATE`, `DELETE`, `MERGE`, `SET`, `REMOVE`
- Function calls: `count(n)` with parens (only `COUNT(*)` is supported), `length(...)`, `size(...)`, etc.
- Aggregations beyond COUNT(*): `sum`, `avg`, `min`, `max`, `collect`, etc.
- `WITH` clauses (intermediate projection)
- `UNION` / `UNION ALL`
- `OPTIONAL MATCH`
- Subqueries
- Parameterized queries (`$param`)
- User-defined procedures

## Conformance corpus

The corpus lives in `internal/cypher/cypher_conformance_test.go` (the
`positiveCypherFixtures` / `negativeCypherFixtures` tables) and consists
of two parts:

1. **Positive fixtures** (queries that must parse + plan + execute
   successfully): each has the query text, the expected node count
   from the executor on a known fixture graph, and a comment
   describing what feature is tested.

2. **Negative fixtures** (queries that must be rejected at parse OR
   plan time): each has the query text, the expected error category
   (lex / parse / plan), and a substring the error must contain.

The harness `cypher_conformance_test.go` runs every fixture and
asserts the expected outcome. A failure means either the planner
drifted (positive case now errors, or negative case now passes) OR
the fixture is wrong. The first run captures baselines; subsequent
runs compare.

## Why this matters

The roundtable identified that we have lexer / parser / executor
tests but no **end-to-end conformance** with documented features.
Without a conformance corpus:

- New parser changes can silently break an unrelated feature
  (variable-length paths still parse; the planner regresses on
  RETURN DISTINCT).
- The "Supported features" list in this doc and in `tools.go`'s
  Cypher tool description can drift from what the code actually
  supports.
- Operators reading the supported-feature list have no machine-
  checkable assertion.

The conformance corpus is the assertion. If we drop a feature
intentionally, fixture is updated. If we drop one accidentally, the
fixture catches it.

## What we DO NOT vendor (yet)

The roundtable mentioned the **openCypher TCK** (Technology
Compatibility Kit) as a possible source. The TCK is the gold standard
but covers features we explicitly DON'T support (writes, function
calls, aggregations). Vendoring the read-only subset of TCK would
require:

1. Filtering TCK fixtures by feature tags to find read-only ones
   compatible with our subset.
2. Translating TCK's Gherkin format to our Go test harness format.
3. Maintaining the fork as TCK upstream evolves.

This is a multi-week workstream not in scope for Phase D2. The
hand-curated corpus in `internal/cypher/conformance/` covers the
features we actually use; expanding to filtered-TCK is a follow-up.

## Discrepancy resolution history

### Initial corpus run (2026-05-05)

The first run revealed 6 discrepancies between what
`internal/tools/tools.go` claimed and what the parser accepted:

- 3 documented features that didn't parse: `IS NULL` / `IS NOT NULL`,
  `ENDS WITH`, `COUNT(*)`.
- 3 write keywords that parsed when they shouldn't (read-only-ness
  was only enforced at planner level): `DELETE`, `SET`, `MERGE`.

### Resolved 2026-05-06 (Plan 3 Phase A)

All 6 discrepancies resolved.

**Documented features now parse + execute:**

- `IS NULL` / `IS NOT NULL` — lexer reserves `IS` and `NULL` keywords;
  parser handles both forms after a property reference; executor
  evaluates true when the property is absent (nil) or empty string
  (the on-disk representation of absent optional string properties).
- `ENDS WITH` — parallel implementation to `STARTS WITH`. Lexer
  reserves `ENDS`; parser handles `ENDS WITH`; executor uses
  `strings.HasSuffix`; SQL pushdown emits `LIKE '%val'`.
- `COUNT(*)` — `parseCountItem` extended to accept `*` as the COUNT
  argument (openCypher standard form). The previous `COUNT(variable)`
  form is retained.

**Write keywords now rejected at parse-time:**

- `CREATE`, `DELETE`, `SET`, `MERGE`, `REMOVE` — lexer reserves these
  keywords; parser explicitly rejects with the message
  `"<keyword> not supported in read-only Cypher subset (pos N)"`.
- Defense in depth: planner-level rejection still in place (writes
  never reach the store). This is purely a documentation-accuracy +
  clearer-error-message change.
- Trailing-token tolerance also fixed: previously `MATCH (n) DELETE n`
  silently parsed (the trailing `DELETE n` was dropped). Now
  `parseQuery` rejects any non-EOF token after the RETURN clause.

### What this proved

The conformance corpus's first run surfaced 6 discrepancies that
would have continued to drift undetected. The second run (after
Plan 3 Phase A) verifies parser + executor + tool description are
now in sync, with 22 positive fixtures + 7 negative fixtures
passing.

### Resolved 2026-05-09 (Phase B: COUNT(DISTINCT) + labels())

The 8-Phase Multi-Month Arc Plan
(`~/Documents/knowledge-base/plans/2026-05-09-code-search-code-graph-multi-month-arc.md`)
identified two dialect gaps observed during the prior session:

- `COUNT(DISTINCT x)` — `COUNT(DISTINCT var)` counts unique bindings;
  `COUNT(DISTINCT var.prop)` counts unique property values across bindings.
  Implemented via a new `Distinct bool` field on `ReturnItem` and
  set-based counting in `buildGroups`. SQL pushdown is disabled when
  `countItem.Distinct` is true (the join-aggregate over-counts for DISTINCT
  semantics); the Go path tracks distinct values via a per-group set.
- `labels(node)` built-in function — returns the node's label as a
  single-element string array (consistent with standard openCypher; nodes
  in code-graph have one label each). Implemented via a new
  `parseFunctionCallItem` parser path that handles `labels(var)` in RETURN
  items, and a `LABELS` short-circuit in `resolveItemValue`.
- 6 positive fixtures + 2 negative fixtures added to the conformance corpus
  (covering basic forms, with-alias, group-by interaction, COUNT(DISTINCT *)
  rejection, and unknown-function rejection).
- 5 new executor tests pin =~ / CONTAINS / STARTS WITH / ENDS WITH on
  `file_path` returning correct results; the prior session's "0 results"
  observation on those operators was an issue at the regex-syntax / data-
  shape layer caller-side, not an engine bug.

Full WITH/aggregation support remains a separate workstream (the existing
2026-05-07 error-fast path stands).

### Resolved 2026-05-07 (B1: IN operator)

The PSM test battery (2026-05-07) found that `IN` was documented in
some user-facing references but produced parse errors. Resolved:

- `IN [list]` — lexer reserves `IN`; parser handles
  `expr IN '[' string|number, ... ']'`; executor compares actual
  property value against each list entry via the same string
  formatting used by `=`. List values may be string or number
  literals; mixed types in the same list are accepted (numeric
  properties match numeric list entries by string equality).
- Empty list (`IN []`) is rejected at parse time — always-false and
  almost certainly user error.
- 3 positive fixtures (string list, number list, single-element
  list) and 3 negative fixtures (empty, unterminated, non-literal
  values) pin the new behavior.

### Resolved 2026-05-07 (B2: WITH clause error-fast)

The PSM test battery (2026-05-07) reported confusion when callers
attempted aggregation queries via `WITH ... COUNT(*)`. The previous
generic "unexpected trailing token" error did not surface the cause
(no WITH-clause support) or the workarounds. Resolved:

- `WITH` between MATCH and WHERE/RETURN now rejected at parse time
  with a clear actionable error: names the gap (`WITH clause not
  supported`), and lists three workarounds:
    1. `RETURN COUNT(*)` directly when the entire MATCH should be counted
    2. `search_graph` with `min_degree`/`max_degree` for fan-in/fan-out
    3. post-process raw rows in the caller
- `STARTS WITH` and `ENDS WITH` continue to parse normally (the WITH
  token is consumed inside parseCondition before clause boundaries).
- 3 negative fixtures (after MATCH, after WHERE, simple pass-through).
- 4 unit tests pin error-message contents (gap-name + workarounds).

Full WITH/aggregation support is a separate workstream. The error-
fast path eliminates the silent-truncation confusion until then.

### Resolved 2026-06-10 (TCK survey: vendored corpus + 3 fixes)

A 324-scenario subset of the openCypher TCK (clauses: match, match-where,
return, return-orderby, return-skip-limit) is now vendored at
`internal/cypher/tck/` and run by `tck_survey_test.go` on every test run,
with every scenario's verdict pinned in `tck/baseline.tsv` (drift in either
direction fails the test). Its first run found three bugs that both this
conformance corpus and the same-day executor sweep had missed:

- **`LIMIT 0` returned up to max_rows rows** — `applyLimit` conflated an
  explicit zero with "no LIMIT clause". `ReturnClause.HasLimit` now
  distinguishes them; `LIMIT 0` is a valid empty result.
- **`LIMIT 1.7` was silently accepted** — the ignored `strconv.Atoi` error
  produced `LIMIT 0` (which then meant "no limit"). Non-integer LIMIT is
  now a parse error.
- **`n.id` returned the internal SQLite row ID, shadowing a user property
  named `id`** (openCypher has no built-in `id` property — `id(n)` is a
  function). User properties named `id` now win; the row ID remains the
  fallback when no such property exists. Same rule on edges.

Current survey state (see `tck/baseline.tsv` for the per-scenario
inventory): 42 pass (33 PASS + 9 PASS_ERROR), 27 fail — every failure is a
known deviation: relationship isomorphism unenforced (an edge can bind two
relationship variables in one MATCH), variable-length relationship
variables bind nil instead of a relationship list, empty-string-as-null,
no `WHERE n:Label` predicates, no inline relationship properties
(`[r:T {k: 'v'}]` — node inline props ARE supported), undefined RETURN
variables yield null instead of erroring, and COUNT-in-ORDER-BY requires
an alias. 209 scenarios are out-of-scope features (WITH, OPTIONAL MATCH,
expressions, functions, parameters, multi-pattern MATCH) per the subset
definition above.

### Resolved 2026-06-10 (executor correctness sweep)

An end-to-end review of the executor's optimization layer (predicate
pushdown, JOIN fusion, SQL aggregate fast path) found eight
silently-wrong-results bugs, all reproduced empirically and fixed.
Regression tests live in `executor_regression_test.go`; the common
thread was fast paths validated for the happy shape with the fallback
shapes never cross-checked against each other.

**Execution fixes (silent wrong results):**

- `WHERE a OR b` on a single-node pattern was pushed into SQL as
  `AND a AND b` — OR queries returned the intersection (usually zero
  rows). Pushdown now respects the operator: all-pushable OR becomes a
  single parenthesized SQL OR group; otherwise all conditions are
  evaluated in Go with OR.
- The JOIN-fusion fast path mutated the shared plan (replacing the
  expand step with a marker), so in a multi-project store every project
  after the first skipped expansion and leaked scan-only bindings.
  Fusion is now tracked per-execution.
- Ungrouped `RETURN COUNT(*)` on the fused scan+expand path emitted
  ` GROUP BY ` with no terms — invalid SQL, every project skipped,
  empty result. GROUP BY is now conditional, and an ungrouped COUNT
  over an empty match returns one row with 0 (openCypher) on both the
  SQL and Go paths.
- The SQL aggregate path assumed COUNT was the last RETURN item;
  `RETURN COUNT(*) AS c, a.name` wrote the count into `a.name` and
  dropped `c`. The column is now located by item position. Per-project
  aggregate rows are also merged by group key (summing counts) to match
  the Go path.
- A two-hop pattern (`[scan, expand, expand]` plan) satisfied the
  3-step fusible-aggregate shape with the middle hop silently ignored.
  The middle step must now be a WHERE filter.
- The batch expand path deduplicated by target node, collapsing
  parallel edges (e.g. CALLS + INVOKES between the same pair) — so
  multiplicity and COUNT(r) disagreed with the fused-JOIN/SQL paths.
  Dedup is now by edge ID; all paths return one row per edge.
- Anonymous nodes (`(:Module)-[:DEFINES]-(b)`, anonymous chain
  intermediates) produced empty results on the non-fused path: the
  binding was never stored, so expansion found no source. Anonymous
  bindings now chain through expansion (and are excluded from default
  projection).
- Untyped variable-length patterns (`-[*1..3]->`) silently traversed
  CALLS edges only; they now traverse all edge types, consistent with
  untyped fixed-length expansion. `*0..N` now binds the start node at
  hop 0. BFS reports truncation (`TraverseResult.Truncated`) instead
  of silently clipping aggregates, uses the per-query binding cap
  rather than max_rows, and its recursive CTE uses UNION (frontier
  dedup) so cyclic graphs can't explode the path count.

**New parse-time rejections (previously silently misinterpreted):**

- Mixed `AND`/`OR` in one WHERE clause (was: collapsed to flat OR).
- `*0` (was: silently unbounded), explicit `..0` upper bounds, and
  inverted ranges like `*3..1`.
- Multiple COUNT items in RETURN (was: all but the last dropped).

**Documentation accuracy:**

- `<>` (not-equals) was documented here and in the `query_graph` tool
  description but never implemented (it lexed as `<` then `>` and
  produced a parse error). Now implemented end-to-end (lexer token,
  parser, Go evaluation, SQL pushdown on both scan and aggregate
  paths).
- The OFFSET claim in this doc was drift — removed (never parsed).
- Fixture-location references updated: the corpus lives in
  `cypher_conformance_test.go`, not a `conformance/` directory.

## Adding a fixture

1. Add the query text + expected outcome to the fixture tables in
   `internal/cypher/cypher_conformance_test.go`.
2. Run `go test ./internal/cypher/ -run TestCypherConformance -v`
   to verify it passes against the current planner.
3. Commit. The CI runs on every PR that touches `internal/cypher/`.

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-05-codegraph-and-cross-tool-recommendations.md` Phase D2
- Roundtable single-source finding: `~/Documents/roundtables/2026-05-05-code-graph/results/META_SYNTHESIS.md` (Opus's "Cypher subset has no conformance corpus")
- Tool description: `internal/tools/tools.go` (the user-facing Cypher feature list — must stay in sync with this doc)
- Existing tests: `internal/cypher/cypher_test.go` (lexer + parser + executor unit tests, complementary to the conformance corpus)
