# Vendored openCypher TCK subset

Feature files vendored from the openCypher Technology Compatibility Kit:

- **Source**: https://github.com/opencypher/openCypher (`tck/features/clauses/`)
- **Commit**: `677cbafabb8c3c5eed458fd3b1ec0daec8d67d23` (vendored 2026-06-10)
- **License**: Apache-2.0 (each `.feature` file carries the full license
  header and the openCypher attribution notice)

Only the clause families inside code-graph's read-only Cypher subset are
vendored: `match`, `match-where`, `return`, `return-orderby`,
`return-skip-limit`. Write clauses (CREATE/MERGE/SET/...), WITH, UNION,
UNWIND, and CALL families are intentionally absent — the engine rejects
those at parse time by design.

## How it is used

`internal/cypher/tck_survey_test.go` runs every scenario through the real
engine and classifies it:

| Verdict | Meaning |
|---|---|
| `PASS` / `PASS_ERROR` | Engine matches the TCK expectation |
| `FAIL` / `FAIL_ERROR` | In-scope scenario, engine deviates — each is a known, documented deviation |
| `OUT_OF_SCOPE` | Query uses a feature the subset deliberately omits |
| `SKIP_*` | Harness limitation (Scenario Outlines, uninterpretable setup, incomparable expected values) |

The verdict for every scenario is pinned in `baseline.tsv`. The test fails
when ANY verdict changes — in either direction — so both regressions
(something passing starts failing) and silent improvements (something
failing starts passing, meaning the baseline understates conformance) are
caught, in the same spirit as the conformance corpus in
`cypher_conformance_test.go`.

## Updating the baseline

After an intentional engine change:

```bash
CBM_UPDATE_TCK_BASELINE=1 go test ./internal/cypher/ -run TestTCKSurvey -count=1
git diff internal/cypher/tck/baseline.tsv   # review the verdict changes
```

## Refreshing the vendored fixtures

```bash
git clone --depth 1 https://github.com/opencypher/openCypher /tmp/openCypher
for d in match match-where return return-orderby return-skip-limit; do
  rm -rf internal/cypher/tck/features/clauses/$d
  cp -r /tmp/openCypher/tck/features/clauses/$d internal/cypher/tck/features/clauses/
done
# then regenerate the baseline and update the commit SHA above
```

Known deviations surfaced by this corpus (beyond the OUT_OF_SCOPE feature
set) are documented in `CONFORMANCE.md`.
