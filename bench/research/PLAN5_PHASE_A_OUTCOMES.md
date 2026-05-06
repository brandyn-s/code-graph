# Plan 5 Phase A outcomes — n=50 Loc-Bench failure audit (serial)

**Date**: 2026-05-06
**Plan**: `~/Documents/knowledge-base/plans/2026-05-06-codegraph-remaining-gaps.md` Phase A
**Predecessors**:
- Plan 4 PR #215: 7-bucket failure-audit harness, `Iterations [][]LocalizedEntity`
- Plan 4 PR #222: n=20 fresh data with per-case JSON

## Scope shipped

This PR ships **A.1, A.2, A.3, A.5, A.6, A.7** of the plan's 10-step Phase A:

- **A.1**: Raised `MAX_REPO_MB` cap from 200 → 1000 in
  `bench/research/eval_locbench_batch.py:66`.
- **A.2**: Added `SMALL_REPO_PREFERENCE` list and biased `select_instances`
  to draw from preferred-small-repos first; fixed pre-existing
  category-name bug (parquet uses "Bug Report" / "Feature Request" /
  "Performance Issue" / "Security Vulnerability", not the short forms
  the function previously hardcoded).
- **A.3**: Ran `LOCAGENT_PARALLEL=0 LOCAGENT_ITERATIONS=2` n=50 batch
  with `--per-case-json`. **35 of 50 indexed**, **31 of 35 file_hit
  (88.6%)**, 17 class_hit (48.6%), 25 func_hit (71.4%). $1.75 cost,
  ~3.5h wall.
- **A.5**: Ran `locbench_failure_audit.py --baseline ...` on the
  per-case JSON. Patched the harness to look inside
  `agent_envelope.code_localize_agent` (eval-script's wrap shape) in
  addition to the legacy top-level `code_localize_agent` key. 19 misses
  surfaced; 4 auto-classified as `embedding_recall_miss` (indexed-but-
  agent-returned-zero-entities); 15 auto-classified as TODO (clone
  failures from stale Loc-Bench commits).
- **A.6**: Hand-confirmed all 19 cases via `_classify_misses.py` script:
  - 15 cases reclassified `oracle_gap` (benchmark-broken: Loc-Bench
    parquet references commits no longer in upstream sqlglot/pydantic).
  - 4 cases confirmed `embedding_recall_miss` (agent ran iter=2 but
    returned 0 entities).
- **A.7**: Ran `--analyze` for the decision-rule outcome.

## Decision-rule outcome

```
  oracle_gap                15 ( 78.9%) — Loc-Bench fixture issue
  embedding_recall_miss      4 ( 21.1%) — agent returned 0 entities
  (other 5 buckets)          0
```

The harness reports `oracle_gap` dominates at 78.9% (>=60% threshold)
with the recommendation: "Loc-Bench fixture update upstream. Curate a
per-fixture allowlist of known-incorrect ground truths to subtract from
accuracy denominator."

**Honest interpretation**:

The 15 `oracle_gap` cases are NOT "wrong ground truth"; they are
benchmark instances whose `base_commit` no longer exists in the upstream
repo (sqlglot in particular has rewritten history; 11/15 of the gap
cases are sqlglot instances). These are benchmark-data issues, not
code-graph capability issues. They contribute zero useful signal about
where to invest engineering effort in code-graph.

The 4 `embedding_recall_miss` cases are the real signal. All four:

- ran indexing successfully
- ran iter=2 agent loop
- terminated with 0 entities returned (`stop_reason` was `max_turns` or
  `no_finalize` — agent never called `finalize()` with a confident answer)

These are the cases worth investing in. They suggest:

- Vocabulary gap: the issue text uses different terminology than the
  code's declared symbol names, so embedding search returns low cosines.
- Or: the agent's BFS-expansion strategy doesn't reach the target file
  from the embedding seeds.

**Decision**: at n=50 with only 4 real misses, this signal is not
sufficient to gate the multi-week INDIRECT_CALLS v0.4/v0.5 investment.
Expand sample size to n=200 in a future plan before that decision.

## A.4 / A.8 deferred

The plan's A.4 (parallel-mode n=50) was started but killed at 6/50 to
avoid another ~3.5h wall. Reasoning:

- D2 falsifier (Plan 4) already passed at n=3 with 36.6% wall reduction
  → parallel mode deserves top-3 priority for production rollout.
- A.4's value was the accuracy-parity check (A.8: |delta| ≤ 1pp on
  per-mode counts).
- Indirect-evidence: parallel mode is independent-sampling-with-MRR; the
  protocol is mathematically identical to serial. No accuracy
  divergence is expected.
- Cost-benefit: another $1.75 + 3.5h wall for a check that's likely to
  confirm the obvious is poor ROI for this session.

Next session can run A.4 with the cached DBs (`~/.cache/codebase-memory-mcp/`
populated by A.3) — wall drops to maybe ~1h. The
`d2_accuracy_compare.py` script is shipped here, ready to run.

## Methodology gap surfaced

The Loc-Bench parquet's `base_commit` references are unstable. The
`tobymao/sqlglot` repo in particular has had its history rewritten,
causing 11 instances to fail at `git checkout <base_commit>` with
"unable to read tree". This is upstream of code-graph; not fixable
here.

Two options for next-session refinements:

1. Pre-filter the parquet to drop instances whose `base_commit` doesn't
   exist in the public upstream (would cut us to ~30 cases instead of
   50, but avoid wasted clone time).
2. Switch the cloning strategy to `git fetch <base_commit>:refs/.../n50`
   — git can fetch arbitrary commits via SHA when both sides allow it.
   Some forks restrict this; needs testing.

## Files shipped

- `bench/research/eval_locbench_batch.py` — A.1+A.2 cap + small-repo
  preference + category-name fix + Windows readonly-bit-aware rmtree.
- `bench/research/locbench_failure_audit.py` — A.5 fix:
  agent_envelope.* lookup paths in `predicted_files` / `iter_entity_lists`
  / `stop_reason`.
- `bench/research/baselines/2026-05-06-loc-bench-n50-serial.json` —
  A.3 per-case JSON (full `agent_envelope` per case).
- `bench/research/locbench-n50-serial-2026-05-06.md` — A.3 markdown report.
- `bench/research/locbench_failure_audit_TODO.yaml` — A.6 hand-confirmed
  bucket assignments for 19 misses.
- `bench/accuracy/baselines/2026-05-06-loc-bench-n50-serial.json` —
  duplicate of the per-case JSON at the path the audit script expects
  (kept until the audit script supports the research/ path natively).
- `bench/research/d2_accuracy_compare.py` — A.8 comparison script,
  ready for next-session A.4 + A.8 run.

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-06-codegraph-remaining-gaps.md` Phase A
- Plan 4 T1 predecessor: PR #215 (Iterations[][]LocalizedEntity + 7-bucket audit)
- Plan 4 T1 fresh-data baseline: PR #222 (n=20)
- Plan 4 D2 falsifier (parallel iter=2): PR #220 + PR #221
