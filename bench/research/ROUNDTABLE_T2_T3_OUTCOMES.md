# Roundtable T2 + T3 outcomes

**Date**: 2026-05-06
**Source**: `~/Documents/roundtables/2026-05-06-codegraph-effort-verdict/META_SYNTHESIS.md`
**Recommendations addressed**:

- **T2**: Mine the 6/50 partial parallel data with `d2_accuracy_compare.py`
  (~30 min, free).
- **T3**: Add minimum-effective-signal gates to the 7-bucket harness;
  don't emit investment-relevant recommendations under <10-15 non-oracle
  agent-executed misses.

## T2: the "free evidence" assumption was wrong (and the fix)

### What the roundtable assumed

> N5 (META_SYNTHESIS.md): "The killed A.4 partial run (6/50 cases) is
> free, immediately-runnable evidence that has not been mined.
> `d2_accuracy_compare.py` is shipped; data exists; cost = 0."

### What I found when I tried to use it

The eval script (`eval_locbench_batch.py`) only persisted the per-case
JSON at the END of the loop (line 672 of the pre-T2 file, after the
`for _, row in selected.iterrows()` loop completed). When I killed
A.4 at 6/50, the per-case JSON was never written. The roundtable's
"data exists" was incorrect — at the time the killed run was
terminated, **zero cases of evidence had been preserved on disk**.

This is itself a harness-integrity issue. The roundtable's own
recommendation depended on a property the harness didn't guarantee.

### The fix

`eval_locbench_batch.py` now writes the per-case JSON **after every
instance**, not only at end-of-loop. Refactored the dict-builder into
`_build_per_case_dict()` so the checkpoint and the final write share
the same payload shape. KeyboardInterrupt now also triggers the
checkpoint before breaking the loop.

Verified: ran `LOCAGENT_PARALLEL=1 ... --n 10 ...` and observed the
per-case JSON growing from 1 → 7 cases incrementally; each instance's
write-back was atomic.

### Re-running T2 after the fix

Once the eval script preserved partial data, ran a fresh n=10 parallel
batch (~$0.35, ~30 min wall) against the warm DB cache from Phase A.
Result: 7/10 indexed (3 dropped to clone failures from stale Loc-Bench
commits, same pattern as Phase A).

Then ran `d2_accuracy_compare.py` against the n=35-indexed serial run
from Phase A. **6 instances were common** between the two runs (parallel
indexed 7, but only 6 of those overlap with serial's 35 since the
seed=42 sampling differs slightly between batch sizes).

```
Per-mode counts (n=6 matched subset):
  mode      serial  parallel   delta       pp
  file           6         6      +0     0.0pp
  class          3         3      +0     0.0pp
  func           6         6      +0     0.0pp

Max |delta| = 0.00pp
```

**Zero divergence on all three metrics.** This is consistent with
parallel iter=2's mathematical structure (independent-sampling-with-MRR)
producing accuracy-equivalent results to serial.

### What this evidence does and doesn't establish

**Does establish**:

- The matched-subset comparison machinery works end-to-end
  (`d2_accuracy_compare.py` actually runs against real data).
- On 6 cases drawn from the small-repo bias, parallel mode produced
  identical scoring to serial mode — i.e., the protocol-level
  refactoring did not introduce visible accuracy regressions on any
  of the 6 cases.
- Combined with the synthetic test in PR #220 and the n=3 wall-time
  comparison in PR #221, the cumulative evidence for parallel iter=2
  parity moves from "n=3 + theoretical argument" to "n=6 zero-delta
  + n=3 wall-reduction + theoretical argument."

**Does NOT establish**:

- Statistical parity. n=6 cannot reject the hypothesis that parallel
  introduces ≤16pp accuracy regression (a single-case flip = 16.7pp
  shift at n=6).
- Cross-category coverage. Of 6 matched cases, 4 are Bug Reports and
  2 are Feature Requests; Performance and Security categories have
  zero matched cases.
- Production-default safety. The roundtable's specific suggestion
  ("flip LOCAGENT_PARALLEL default from unset to 1") still requires
  a larger matched corpus.

The compare script now enforces this honestly: at n<10 common indexed
instances it returns `INSUFFICIENT_SAMPLE` instead of recommending
default-flip, regardless of how clean the delta looks.

## T3: signal-gating in the 7-bucket audit

### The problem the gate addresses

Plan 5 Phase A's audit emitted:

```
Bucket oracle_gap at 78.9% (>= 60% threshold)
-> Loc-Bench fixture update upstream. Curate a per-fixture allowlist
   of known-incorrect ground truths to subtract from accuracy denominator.
```

That recommendation was confidently shaped against a denominator that
didn't reflect actionable signal:

- 19 total classified misses
- 15 oracle_gap (clone failures from stale Loc-Bench commits — a
  benchmark-data issue, not a code-graph capability signal)
- 4 embedding_recall_miss (the only actually-actionable misses)

The 78.9% dominance is a measurement artifact: dividing by 19
(classified) instead of by 4 (actionable). Even reading the
recommendation as "the benchmark is broken, fix the benchmark" doesn't
help — the only real capability signal is 4 cases, which is below any
reasonable threshold for emitting an investment-relevant recommendation.

I had to override the harness recommendation in the outcomes doc by
hand. The roundtable correctly flagged this:

> C5 (META_SYNTHESIS.md): "The highest-leverage next action is
> harness-certification BEFORE n=200, not n=200 itself."
> T3: "Don't emit investment-relevant recommendations unless ≥N
> non-oracle, agent-executed misses with bootstrap CIs (N suggested
> ≥10-15)."

### The fix

`locbench_failure_audit.py::analyze_classified` now:

1. **Decomposes the denominator**:
   - `classified` (the old denominator) — counts every confirmed bucket
   - `actionable_total` = classified − oracle-attributed −
     non-agent-executed
   - Buckets in `ORACLE_BUCKETS` (currently `{"oracle_gap"}`) are
     subtracted because they reflect benchmark-data issues, not
     capability gaps.

2. **Reports both percentages per bucket**: e.g.,
   `oracle_gap 15 (78.9% / n/a actionable)` and
   `embedding_recall_miss 4 (21.1% / 100.0% of actionable)`.

3. **Gates the decision rule on signal adequacy**:
   - If `actionable_total < MIN_ACTIONABLE_MISSES` (default 10),
     emit `INSUFFICIENT_SIGNAL` instead of running the threshold check.
   - Otherwise apply the 60% threshold against the actionable
     denominator (not classified).

4. **Names the failure mode in the verdict**: explicit
   "Do NOT emit investment-relevant recommendations" + concrete
   suggestions (pre-filter parquet, run at n>=200).

### Verification on the real Phase A YAML

Pre-T3 output (from Plan 5 Phase A outcomes doc):

```
Bucket oracle_gap at 78.9% (>= 60% threshold)
-> Loc-Bench fixture update upstream...
```

Post-T3 output on the SAME YAML:

```
Verdict: INSUFFICIENT_SIGNAL
Only 4 actionable miss(es) (non-oracle, agent-executed) of 19 total.
Threshold for an investment-relevant recommendation is 10.
-> Do NOT emit investment-relevant recommendations. Expand corpus.
   - Pre-filter Loc-Bench parquet to drop instances whose base_commit
     isn't in upstream (cuts oracle_gap clone failures upfront).
   - Run at n>=200 against the filtered corpus.
```

### Test coverage

`test_audit_signal_gate.py` pins:

1. Phase A's 15-oracle + 4-actionable pattern → INSUFFICIENT_SIGNAL.
2. Pure-oracle distribution (50 oracle_gap, 0 actionable) →
   INSUFFICIENT_SIGNAL (does NOT emit "fixture update" recommendation).
3. Adequate-actionable + dominant non-oracle bucket → DOMINANT.
4. Adequate-actionable + split between buckets → NO_DOMINANT.
5. Boundary at exactly 10 actionable → DOMINANT (>=, not >).
6. Boundary at 9 actionable → INSUFFICIENT_SIGNAL.
7. Oracle dominance with 12 actionable → DOMINANT on the actionable
   bucket (NOT on oracle_gap, because oracle_gap is subtracted from
   the denominator).

All 7 tests pass.

## What changed (file-by-file)

- `bench/research/eval_locbench_batch.py`:
  - Extracted `_build_per_case_dict(summary)` helper.
  - Added `_checkpoint_per_case()` closure called after every instance
    AND on KeyboardInterrupt before break.
  - Final-write block reduced to one call to the helper.

- `bench/research/locbench_failure_audit.py`:
  - Added `ORACLE_BUCKETS`, `NON_AGENT_EXECUTED_BUCKETS`,
    `MIN_ACTIONABLE_MISSES` constants.
  - Added `_verdict_under_signal_gate()` returning
    `(verdict, dominant_bucket_or_None, rationale)`.
  - `analyze_classified()` now reports both classified and
    actionable percentages, and routes through the verdict gate.

- `bench/research/d2_accuracy_compare.py`:
  - Fixed `cases` vs `instances` key-name mismatch (writer/reader bug
    of the same class as the audit-harness `agent_envelope` bug).
  - Added n<MIN_COMMON_FOR_DEFAULT_FLIP gate (default 10) so the
    "flip default" recommendation requires adequate sample size.

- `bench/research/test_audit_signal_gate.py`: 7 new pytest cases.

## Cumulative output

- `bench/research/baselines/2026-05-06-loc-bench-n10-parallel-T2.json`
  — n=10 parallel run with 7 indexed (3 clone-failures); per-instance
  agent_envelope preserved.
- `bench/research/locbench-n10-parallel-T2-2026-05-06.md` — readable
  summary report.

## Cross-references

- Roundtable: `~/Documents/roundtables/2026-05-06-codegraph-effort-verdict/META_SYNTHESIS.md`
- Roundtable findings addressed: T2 (action), T3 (action), N5 (assumption corrected)
- Plan 5 Phase A outcomes (the document this work supplements):
  `bench/research/PLAN5_PHASE_A_OUTCOMES.md`
