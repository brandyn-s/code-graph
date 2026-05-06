# Cypher Conformance Corpus (Plan 1 Phase D2)

The roundtable's single-source finding (Opus, partially conceded by
Grok and GPT): "Custom read-only Cypher subset has no published-corpus
differential testing — query-planner drift could be silent."

## Scope of code-graph's Cypher subset

code-graph implements a **read-only subset of openCypher** for graph
queries against the indexed code graph. Supported features:

- `MATCH (n:Label)` — node patterns with optional label
- `WHERE` — comparison ops (=, <>, <, >, <=, >=), regex (=~), STARTS WITH, ENDS WITH, CONTAINS, AND/OR, IS NULL, IS NOT NULL
- `RETURN` — projection, COUNT(*), DISTINCT, ORDER BY, LIMIT, OFFSET
- Variable-length paths: `(a)-[:REL*1..3]->(b)`
- Directional relationships: `-[:REL]->`, `<-[:REL]-`, `-[:REL]-`
- Multiple relationship types: `-[:REL_A|REL_B]->`
- Edge property filters: `r.confidence`, `r.url_path`, etc.

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

The corpus lives at `internal/cypher/conformance/` and consists of
two parts:

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

## Discrepancies discovered by initial corpus run (2026-05-05)

The first run of the conformance corpus revealed **6 discrepancies**
between what `internal/tools/tools.go` claims is supported and what
the parser actually accepts. These are documented in fixture
comments (commented-out blocks) and listed here for visibility:

### Documented features that don't parse

1. **`IS NULL` / `IS NOT NULL`** — `tools.go` claims support; parser
   rejects with "expected comparison operator, got IS". Either:
   - (a) extend parser to accept `IS NULL` / `IS NOT NULL`, OR
   - (b) remove from claimed feature list in `tools.go`.
   First reading: aspirational claim. Recommended action: remove
   from docs OR ship parser support.

2. **`ENDS WITH`** — `tools.go` claims support; parser fails. Likely
   parallel to STARTS WITH coverage; investigate parser to see if
   it's a one-line addition or a deeper gap.

3. **`COUNT(*)`** — claimed in `tools.go`, fails the conformance
   syntax. The unit test `TestParseReturnWithCount` passes, so the
   parser DOES handle some COUNT shape; the conformance fixture's
   exact syntax may differ. Investigate before claiming "supported"
   externally.

### Write keywords that PARSE but shouldn't

4-6. **`DELETE`, `SET`, `MERGE`**: parser accepts these without
error. Read-only-ness is enforced at the EXECUTOR / planner level
(no executor exists for write operations), so the security property
holds. But "Read-only subset" is documented at parse-time, which
isn't quite accurate. Either:
- (a) extend parser to reject write keywords (defense in depth), OR
- (b) update docs to "Read-only enforced at planner level".

These are NOT security issues — writes never reach the store. They
ARE documentation accuracy issues that the conformance corpus is
designed to catch.

### What this proves

The conformance corpus's first run already justified its existence.
17 features verified working; 6 discrepancies surfaced. Without the
corpus, the documented feature list would have continued to drift.

## Adding a fixture

1. Create the query text + expected outcome in
   `internal/cypher/conformance/fixtures.go`.
2. Run `go test ./internal/cypher/ -run TestCypherConformance -v`
   to verify it passes against the current planner.
3. Commit. The CI runs on every PR that touches `internal/cypher/`.

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-05-codegraph-and-cross-tool-recommendations.md` Phase D2
- Roundtable single-source finding: `~/Documents/roundtables/2026-05-05-code-graph/results/META_SYNTHESIS.md` (Opus's "Cypher subset has no conformance corpus")
- Tool description: `internal/tools/tools.go` (the user-facing Cypher feature list — must stay in sync with this doc)
- Existing tests: `internal/cypher/cypher_test.go` (lexer + parser + executor unit tests, complementary to the conformance corpus)
