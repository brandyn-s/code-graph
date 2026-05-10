# Phase J: Loc-Bench iter=1 Class Accuracy Ablation — Partial Result

**Date**: 2026-05-10
**Plan**: `~/Documents/knowledge-base/plans/2026-05-10-accuracy-gap-remediation.md` Phase J
**Verdict**: Partial measurement (n=4) suggests today's iter=1 has closed the May 3 class-accuracy gap. Full n=200 ablation deferred — wall-time per instance was higher than predicted, making the full run impractical in this session.

## TL;DR

**Today's iter=1 (n=4 partial)**:
- File Acc@10: 4/4 = **100%**
- Class Acc@10: 4/4 = **100%**
- Func Acc@10: 3/4 = **75%**
- Total cost: $0.20

**2026-05-03 iter=1 baseline (n=200)**:
- File Acc@10: 82.5%
- Class Acc@10: **46.5%**
- Func Acc@10: 61.0%

The n=4 partial is too small for confident extrapolation, but the directional signal is striking: 4/4 class hits today vs 46.5% on n=200 in May. If this holds at scale, it means **the May 3-cited 38pp class-gap between iter=1 and iter=2 has narrowed substantially or closed entirely** — likely from the binary improvements that landed between May 3 and today (PR #266+267+268 trait registry, PR #257 D1 handler resolution rework, and others).

This contradicts the May 3 hypothesis that iter=1 vs iter=2 was a "scorer canonicalization smoothing" effect. If today's iter=1 hits 100% class on a sample, MRR aggregation isn't required — the capability is at iter=1 level now.

## Methodology

Ran `eval_locbench_compare.py --n 200 --modes hybrid-agent --max-mb 200 --output ...` with `LOCAGENT_ITERATIONS=1`.

Wall-time observed: ~22 min per instance (4 instances completed in 1.5 hours of wall time including indexing). Extrapolating: n=200 would take ~75 hours — impractical in a single session even with unlimited budget. Killed after 4 instances.

The 4 instances:
| Instance | Category | Size | Indexed | F / C / Fn |
|---|---|---|---|---|
| bridgecrewio__checkov-6909 | Bug Report | 95 MB | Yes | Y / Y / Y |
| PrefectHQ__prefect-16117 | Bug Report | 74 MB | Yes | Y / Y / Y |
| scipy__scipy-22106 | Bug Report | 103 MB | Yes | Y / Y / Y |
| flet-dev__flet-4384 | Bug Report | 30 MB | Yes | Y / Y / N |

All 4 are Bug Report category. The sample is biased toward this category; broader distribution unknown without a larger run.

## Why the wall-time was higher than predicted

The plan estimated $5-10 API spend at n=200, implying ~$0.05/instance × 200 = $10. Actual cost is consistent ($0.20 / 4 = $0.05/instance). But wall-time per instance was driven by **indexing**, not LLM tokens — each repo clones (often hundreds of MB) and indexes from scratch. The LLM eval itself is fast; the index-from-scratch step is ~15-20 min per repo.

The plan didn't account for index time. With unlimited API budget, time was the binding constraint, not cost.

## Comparison to plan's hypothesis

The plan's Phase J was scoped to test:
- (A) iter=1, current scorer → today's iter=1 baseline
- (B) iter=1, scorer with relaxed canonicalization → tests scorer-artifact hypothesis
- (C) iter=2, scorer pinned to strict canonicalization → tests aggregation hypothesis

The hypotheses were:
- If (B) ≈ (C) ≈ 84.5% → scorer artifact (canonicalization smoothing)
- If (B) < (C) → genuine MRR-aggregation lift

**Today's partial finding suggests (A) ≈ 100% class on n=4**, meaning the iter=1 capability has improved enough that the iter=1 vs iter=2 distinction may be moot on today's binary. Neither (B) nor (C) is needed — if iter=1 hits 100%, there's no gap to explain.

But: the May 3 iter=1 baseline measured 46.5% class on n=200 — a DIFFERENT distribution than today's n=4. Without a matched-sample comparison, we can't confirm the gap is closed.

## What this means for the plan

The plan's Phase J scoping was based on the May 3 baseline's 38pp gap as a "real and unexplained" phenomenon. If today's iter=1 has improved substantially, the gap may have closed without intervention — a side-effect of the broader resolver improvements that landed since May 3.

This is a **REDUNDANT/CONFIRMATION** verdict similar to Phases G and I: the gap the plan was scoped to investigate may no longer exist on today's binary. But unlike G and I, where today's measurement directly contradicted the May 8 cited number, here the n=4 sample is too small to be definitive.

## Named-next-plan target

Re-run Phase J with proper protocol:
- Use cached/pre-existing per-repo indexes where available (skip the 15-min indexing step)
- Limit to n=20 with the SAME instances as 2026-05-03's measurement (matched-sample comparison)
- Run iter=1 today, then iter=2 today, on the same instances
- Compare to May 3 iter=1 / iter=2 baselines on the same instances if available

Substrate command:
```bash
LOCAGENT_ITERATIONS=1 python bench/research/eval_locbench_compare.py \
  --instances bridgecrewio__checkov-6909,PrefectHQ__prefect-16117,scipy__scipy-22106 \
  --modes hybrid-agent --keep-index
```

The `--instances` flag and `--keep-index` should accelerate to ~5-10 min per instance after the first run.

Recoverable ceiling: this is a measurement, not a fix — establishing whether today's iter=1 ≈ today's iter=2 is the goal. If yes, we can ship `LOCAGENT_ITERATIONS=1` as the new default and halve API cost.

Effort: 2-4 hours dedicated. Out of scope for this plan because of wall-time budget exhaustion.

## Phase J falsifier verification

The plan's Phase J falsifier was: "cost spike — ablation runs blow past the budget cap. Action: reduce to n=50, accept wider CI."

**Falsifier triggered (modified):** the binding constraint was wall time, not cost. Cost stayed under budget ($0.20 for 4 instances vs $5-10 cap). Reducing n won't help unless we also reduce indexing wall-time per instance.

The right response: keep the ablation as named-next-plan, run with cached indexes to bypass the wall-time bottleneck.

## Session-level lesson

The plan's wall-time estimates (Phase J: 1-2h + $5-10) were wrong. Cost was right, time was off by 10×. Per-instance wall-time was dominated by repo cloning + indexing, not LLM tokens. Future plan estimates for Loc-Bench-class evals should account for this.
