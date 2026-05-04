# Family A measurement-discipline gates

> Pre-publication assertions on the harness's pure functions. One leg
> of the three-leg stool from the 2026-05-04 incident-backport
> experiment. See
> `~/Documents/knowledge-base/research/2026-05-04-incident-backport-experiment.md`
> for the full design.

## What's in scope

Family A catches **logic bugs in the harness itself** — the scorer,
the oracle, and the contracts between them and the rest of the
pipeline. Of the 7 documented instrument incidents in the last 7 days,
Family A catches 3:

| Incident | Bug | Where caught |
|---|---|---|
| #2 (CBM/resolver QN mismatch, 2026-05-02) | CBM emitted `pkg.func`; resolver indexed `pkg.go.func` | **Cross-component contract test** — see "Deferred" section |
| #3 (oracle drops `recv.method`, 2026-05-02) | Oracle dropped recv.method calls in dotted-form edge case | `bench/accuracy/tools/oracle-go-ast/main_test.go` (existing TestY5_* and TestY6_* coverage) |
| #4 (class_hit module-level, 2026-05-04) | Scorer's else branch left class_hit=False for module-level GTs | `bench/research/test_score_entities.py::TestModuleLevelGT` and `::TestInvariants::test_func_hit_implies_class_hit` |

## What's shipped (this PR)

### `bench/research/test_score_entities.py` — scorer fixtures (27 tests)

Pure pytest unit tests on
`eval_locbench_compare.py::score_entities`. Categories:

- `TestNormalizePath` — path normalization edge cases
- `TestModuleLevelGT` — ACC-012 regression (3 tests)
- `TestClassMethodGT` — class-method GT shapes (3 tests)
- `TestCaseInsensitive` — case folding
- `TestProjectPrefix` — code-graph QN prefix containment (2 tests)
- `TestMixedGT` — multiple GT entries with mixed shapes (2 tests)
- `TestDegenerate` — empty inputs, malformed GT, missing fields (5 tests)
- `TestDottedFunc` — nested classes / dotted function names
- `TestPathNormalization` — backslash handling at match time (2 tests)
- `TestInvariants` — monotonicity invariants (3 tests):
  - `class_hit ⟹ file_hit`
  - `func_hit ⟹ class_hit` (catches ACC-012 directly)
  - `func_hit ⟹ file_hit`

**Verification of effectiveness:** before shipping, the scorer fix
for ACC-012 was temporarily reverted to confirm the new tests catch
the bug. Result: 9 of 27 tests fail, including the
`test_func_hit_implies_class_hit` invariant. Tests genuinely catch
the regression. Restored fix; all 27 pass.

### CI integration

`.github/workflows/accuracy-regression.yml` now runs:
- Python pytest scorer fixtures (new) — fails any PR that breaks
  `score_entities` invariants
- Go oracle unit tests (new explicit step; was implicit) — fails any
  PR that breaks oracle Y.5/Y.6 receiver-substitution

## Deferred — cross-component contract test (incident 2)

Incident 2 (CBM/resolver QN format mismatch) requires a test that
runs CBM extraction AND resolver indexing on the same fixture and
asserts the QN sets agree. This is genuine integration testing and
deserves its own focused PR.

**Proposed design (next-step PR):**

```go
// internal/pipeline/cbm_resolver_qn_contract_test.go
package pipeline

import "testing"

// TestCBMResolverQNContract_Go — for a small Go fixture, verify that
// the QNs CBM emits at definition-time are exactly the QNs the
// resolver looks up at call-resolution-time. Without this contract,
// any drift between the two components shows up as systematic recall
// loss in same-package CALLS edges (incident 2, 2026-05-02).
func TestCBMResolverQNContract_Go(t *testing.T) {
    fixture := writeTempGoModule(t, /* small package with 2 free fns + 1 method */)
    cbmEmitted := runCBMExtraction(t, fixture)
    resolverIndexed := runResolverIndexing(t, fixture)
    
    // For each definition CBM emitted, the resolver must index it
    // under the SAME QN string (no transformation between the two).
    for qn := range cbmEmitted.definitions {
        if _, ok := resolverIndexed.byName[qn]; !ok {
            t.Errorf("QN %q emitted by CBM but not indexed by resolver", qn)
        }
    }
}
```

**Effort estimate (deferred PR):** 4-6 hours to set up the temp-module
fixture infrastructure and wire CBM + resolver invocation, plus 2-3
hours for the multiple shape variants (free function, method, package
init, type method).

**Why deferred from this PR:** the contract test requires understanding
CBM's C-bindings invocation and the resolver's `byName` index API, and
the right home for it (cbm package vs pipeline package vs new contract
package) is itself a small design question. Better as its own focused
PR than tacked onto this one.

## Three-leg stool: this is leg 1 of 3

Per the 2026-05-04 back-port experiment, the full measurement-discipline
pay-down requires three independent gates:

1. **Family A: scorer + oracle fixtures + contract test** ← this PR
   - Catches logic bugs in harness pure functions
   - Catches 3/7 documented incidents
2. **Family C: mechanical refusal gate in benchmark report generation** (next, ~½ day)
   - Catches non-monotone hierarchies, missing sampled-edge logs, missing comparator-equivalence notes
   - Backstops 2/7 incidents
3. **Family B: provenance manifest required for any published number** (next, ~½ day)
   - Catches stale-baseline / cache-reuse / version-mismatch bugs
   - Catches 4/7 documented incidents (the LARGEST count of any single gate)

Cannot defer Family B — it catches more incidents than Family A.
Family C ships fast. The full sequence is ~3 days of work for the
measurement-discipline pay-down, after which Phase B (calibration
head) and Phase C (RepoMem) of the broader-production maturation plan
resume.
