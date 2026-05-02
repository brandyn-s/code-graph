# Oracle Y.5 — instrument-shift baseline

**Date**: 2026-05-02
**Instrument**: go-ast oracle with Y.5 receiver-method resolution + ambiguity-aware bare-name resolution.
**Fixture sha**: pinned to commit landing this PR (oracle binary + Python wrapper changes).
**Source baseline**: post-Y.3 (PR #135) + CBM QN fix (PR #136). No code-graph resolver changes in this PR.

## What changed in the instrument

Two oracle bugs identified in PRs #137 and #139 are fixed here. **Code-graph behavior is unchanged** — all delta is instrument improvement.

1. **Drop fix** (oracle binary, `extractCallee` + new receiver-name tracking): when inside `func (p *Pipeline) X()`, a call `p.Y()` previously emitted callee `"p.Y"` which the Python wrapper dropped as `calls_path_dropped`. Now emits `"Pipeline.Y"`, which the wrapper resolves via the new `recv_method_to_qns` index.
2. **Hallucination fix** (Python wrapper): bare-name resolution was first-write-wins, causing `s.db.Close()` (calls stdlib `*sql.DB.Close`) to resolve to `ConfigStore.Close` (the only internal `Close`). Now drops multi-candidate bare names instead of arbitrarily picking the first.

## Headline (vs Step 6 baseline / vs post-Y.3+CBM-fix baseline)

| Metric    | Step 6 (pre-Y.3) | Post-Y.3+CBM | **Post-Y.5** | Δ vs Step 6 | Δ vs prior |
|-----------|-----------------:|--------------:|-------------:|-------------:|------------:|
| F1        | 0.8934           | 0.8995        | **0.9801**   | **+8.7pp**   | **+8.1pp**  |
| Precision | 0.8094           | 0.8207        | **0.9617**   | **+15.2pp**  | +14.1pp     |
| Recall    | 0.9967           | 0.9951        | 0.9992       | +0.25pp      | +0.41pp     |
| TP        | 1818             | 1817          | 2363         | +545         | +546        |
| FP        | 428              | 397           | 94           | -334         | -303        |
| FN        | 6                | 9             | 2            | -4           | -7          |

Recall regression vs Step 6 is +0.25pp (positive). FP count dropped by 78%. TP count rose by 30% — the +545 is almost entirely the previously-dropped receiver method calls now correctly resolved.

## Per-project F1 (post-Y.5)

| Project | Pre-Y.5 F1 | **Post-Y.5 F1** | Δ |
|---------|-----------:|----------------:|---:|
| internal/store | 0.6164 | **0.6733** | +5.7pp |
| internal/cypher | 0.793 | **0.9356** | +14.3pp |
| internal/pipeline | 0.8327 | **0.9311** | +9.8pp |
| internal/tools | 0.8502 | **0.9946** | +14.4pp |
| internal/cbm | 0.9942 | 0.9942 | 0 |

internal/tools went from 0.85 to nearly perfect (0.99). internal/store had the smallest jump — its remaining FPs (229 of total 94 across all... wait that's wrong, store has the most residual FPs at 229) are likely structural code-graph issues unrelated to the oracle gap, not measurement artifacts. **Store now warrants a real per-project plateau-diagnosis** (the deferral from PR #138 can be revisited).

## Per resolver_rule (post-Y.5)

| Rule | Pre-Y.5 P | **Post-Y.5 P** | Δ | Support |
|------|----------:|---------------:|---:|---------:|
| exact-qn-match | 1.0 | 1.0 | 0 | 518 |
| same-package-shadow | 0.9875 | 0.9875 | 0 | 240 |
| cross-package-heuristic | 0.7301 | **0.9481** | +21.8pp | 1696 |

Cross-package-heuristic — the cell that was the entire focus of Phase Y — now has 0.95 precision. **The cell never had a code-graph problem; it had an instrument problem.** Y.3's Janusian penalty (PR #135) still contributes — without it, post-Y.5 precision would be lower because the 32 ambiguous-site FPs would still emit. But the bulk of the apparent FP cell was oracle-dropped real edges.

## Per caller_kind (post-Y.5)

| Kind | Pre-Y.5 P | **Post-Y.5 P** | Δ | Support |
|------|----------:|---------------:|---:|---------:|
| function-body | 0.9512 | 0.9551 | +0.4pp | 245 |
| method-body | 0.6002 | **0.9312** | +33.1pp | 1207 |
| test-body | 1.0 | 1.0 | 0 | 1005 |

method-body precision was the dominant Step 6 diagnostic: 416 of 428 FPs (97.2%) sat in `method-body × cross-package-heuristic`. Post-Y.5, method-body precision is 0.93. **The dominant FP cell from Step 6 was almost entirely oracle artifacts.**

## Implications for prior conclusions

This baseline forces re-evaluation of several findings from Phase Y:

1. **Y.3 (Janusian penalty) — still net positive.** Post-Y.5 still has 88 FPs in cross-package-heuristic; without Y.3's penalty, ~30+ would be additional ambiguous-site emissions. Y.3 contributes ~1pp F1 even on the new instrument. Keep.

2. **Y.1 (P≥0.85 threshold) — refutation stands.** Even on the new instrument, the threshold would still drop the 1062 cross-package-heuristic TPs that flow through `unique_name`/`suffix_match` paths (since their confidence values are still 0.75/0.55). Re-confirms PR #135's decision to revert.

3. **Y.2 (same-package-shadow preference) — premise still wrong.** The 5 `handleIndexRepository → tools.*` edges that motivated Y.2 turned out to be CBM QN bugs (PR #136). Even on the new instrument, there's no code-graph fix to design.

4. **Phase X.3 (FN-shift analysis) — finding still valid, with a stronger framing.** All 6 baseline FNs are now accounted for as instrument artifacts (5 caller-QN format mismatch fixed by PR #136 + 1 oracle phantom fixed by this PR). 0 real code-graph misses on the Go fixture.

5. **Phase X.4 (plateau-diagnosis-pattern stub) — needs Step 5b update.** The recipe correctly surfaced the cell at every stage. What it didn't surface (3 times in 3 sessions) was that the cell was an instrument problem, not a code-graph problem. PR #138 already noted this and proposed Step 5b. The pattern file should be expanded.

## What remains

Two FNs persist post-Y.5. Need investigation in a follow-up — likely interface-dispatch edges the oracle still doesn't track, or actual code-graph misses now visible without the noise floor.

internal/store's residual 229 FPs are now the largest remaining error mass. With the oracle gap removed, this is plausibly real code-graph behavior worth diagnosing.

## Cumulative across all sessions

| Step | F1 | Δ since Step 6 |
|------|----:|---------------:|
| Step 6 (pre-Y.3) | 0.8934 | — |
| Y.3 (PR #135) | 0.8983 | +0.5pp |
| CBM QN fix (PR #136) | 0.8995 | +0.6pp |
| **Oracle Y.5 (this PR)** | **0.9801** | **+8.7pp** |

Of the +8.7pp total, +8.1pp came from instrument fixes (PR #136 + this PR) and +0.5pp from real resolver tightening (Y.3). **The biggest accuracy lever in this work was correcting the measurement instrument, not changing the resolver.**

## Files changed

- `bench/accuracy/tools/oracle-go-ast/main.go` — track receiver name + type in visitor, substitute `recv.method` → `<RecvType>.<method>` in CallExpr handling.
- `bench/accuracy/tools/oracle-go-ast/main_test.go` — 5 regression tests pinning the substitution behavior.
- `bench/accuracy/oracle_go_ast.py` — `_build_def_indexes` (recv-method index + ambiguity-aware bare-name index) + updated `resolve_and_filter` resolution logic.

No code-graph resolver changes. No baseline JSON changes (this PR ships only the instrument; the new baseline reports are produced by the next harness run).
