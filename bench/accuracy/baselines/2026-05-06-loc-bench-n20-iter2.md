# Loc-Bench n=20 with per-case-json + per-iteration data (Plan 4 T1 fresh run)

**Date**: 2026-05-06
**Plan**: Plan 4 T1 fresh-data fulfillment.
**Predecessors**:
- PR #215 (Plan 4 T1) added `Iterations [][]LocalizedEntity` to `locagent.Result` + `--per-case-json` flag to `eval_locbench_batch.py`
- This run is the first to exercise both new fields against real Loc-Bench instances.

## Headline numbers

| metric | n | accuracy |
|---|---|---|
| Instances attempted | 20 | — |
| Indexed successfully | 6 | 30% |
| Agent ran | 6 | 100% of indexed |
| File hit | 6/6 | **100.0%** |
| Class hit | 4/6 | 66.7% |
| Func hit | 5/6 | 83.3% |
| Total cost | $0.30 | well under $2.00 budget |
| Total wall | ~43 min | — |

**Caveat — sample size**: only 6 of 20 instances ran (14 dropped at index time, mostly "repo too large" — 10 instances exceeded the 200 MB cap including ray, scikit-learn, vllm, sqlglot subsets). The 100% file accuracy on n=6 is real but not directly comparable to the n=200 baseline's 86.0% Acc@10. The agent ran clean on the 6 instances it saw; the methodology bottleneck is in the eval harness's filtering, not in code-graph.

## Per-iteration data verification

`Iterations` field is present and populated for all 6 successful runs with `iterations_count=2` (matches `LOCAGENT_ITERATIONS=2` default). Per-iteration entity lists captured BEFORE MRR aggregation, exactly as the Plan 4 T1 schema specified. Sample (huggingface/accelerate-3279):

```
iter[0] entities: ['src/accelerate/utils/modeling.py', 'src/accelerate/utils/modeling.py']
iter[1] entities: ['src/accelerate/utils/modeling.py', 'src/accelerate/utils/modeling.py']
```

Both iterations converged on the same file. The 7-bucket failure-audit harness can now distinguish `rescued_by_iter_2` cases from `iter_1_was_sufficient` cases on real data.

## Failure analysis (the 2 misses)

### Class-miss: huggingface/accelerate-3279 (file=Y, class=N, func=Y)

GT: `src/accelerate/utils/modeling.py:_init_infer_auto_device_map` and `:infer_auto_device_map`. Both are free functions (no class). The agent found both functions correctly. The class scorer expects a class context that doesn't exist in the source — likely **oracle_gap** (Loc-Bench's `class` field expects something the codebase doesn't provide for free-function changes).

### Func-miss: ranaroussi/yfinance-2122 (file=Y, class=N, func=N)

GT: `yfinance/utils.py:fix_Yahoo_returning_live_separate`. Agent's top-2 predictions: `fix_Yahoo_returning_prepost_unrequested` and `format_history_metadata` — both in the right file, both named similarly to the GT function. Classic **scope_collision** — file was right, agent picked sibling functions with similar prefix. The graph's CALLS edges to the GT function would be the disambiguator; the agent's seed-matching surfaced the wrong sibling.

Bucket distribution from the n=2 misses:
- 1× `oracle_gap` (likely)
- 1× `scope_collision`

## Methodology gap surfaced

The 14-of-20 drop rate at indexing time is the dominant signal in this run. The eval_locbench_batch.py `MAX_REPO_MB=200` constant filters out:
- ray-project/ray (937 MB, 4 instances)
- vllm-project/vllm (likely large, 3 instances)
- scikit-learn/scikit-learn (201 MB, 1 instance)
- sqlglot, langgraph, alexa-pi (others varying)

To get n>=10 indexed cases per run, raise the cap to 1000 MB or pre-filter the parquet to small repos. Alternative: use a fast-mode index (already in the throughput harness) to make large repos tractable.

This is unrelated to code-graph capability — it's a benchmark-harness methodology issue.

## What the per-case JSON enables

This run's `2026-05-06-loc-bench-n20-per-case.json` (230 KB) contains the full agent envelope per case, including:
- `agent_envelope.code_localize_agent.iterations[]` — per-iteration entity lists
- `agent_envelope.code_localize_agent.entities[]` — final MRR-aggregated list
- `agent_envelope.code_localize_agent.transcript[]` — turn-by-turn tool calls
- `agent_envelope.code_localize_agent.input_tokens / output_tokens / turns / stop_reason`

`bench/research/locbench_failure_audit.py` consumes this directly. With the 7-bucket taxonomy + auto-proposal heuristics from PR #215, the audit produces per-case proposals that human-review accelerates.

## Next session

To formally complete the T1 falsifier (decision-rule outcome, ≥60% bucket dominance):

1. Raise `MAX_REPO_MB` in `eval_locbench_batch.py` to 1000 (or pre-filter parquet to small repos) so n>=20 of 20 actually run.
2. Run `--n 50 --per-case-json` again to get n~50 misses across 7 buckets.
3. Hand-confirm the auto-proposed buckets in `locbench_failure_audit_TODO.yaml`.
4. Run `--analyze` for the decision-rule outcome.

Estimated cost: ~$2.50 for the n=50 fresh run; ~30-90 min wall depending on repo sizes.

## Cross-references

- PR #215 (T1 implementation): Iterations field + --per-case-json flag + 7-bucket audit
- Plan: `~/Documents/knowledge-base/plans/2026-05-06-codegraph-followup.md` Plan 4 T1
- Per-case JSON: `bench/accuracy/baselines/2026-05-06-loc-bench-n20-per-case.json`
- Markdown report: `bench/research/locbench-n20-results-2026-05-06.md`
- Audit scaffold: `bench/research/locbench_failure_audit_TODO.yaml`
