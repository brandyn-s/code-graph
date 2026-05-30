# Loc-Bench re-baseline (reachable corpus) + SweRank comparison arm

**Status (2026-05-30): BLOCKED ON MEASUREMENT.** The defended 86.0 / 84.5 / 73.5
(file / class / func Acc@10) numbers are a *historical* 2026-05-04 measurement;
the 2026-05-12 re-run was REFUSED for publication because 58–67 instances became
unreachable and the missing tail is category-skewed (42% Security Vulnerability —
see `bench/accuracy/baselines/2026-05-12-loc-bench-unreachable-tail-finding.md`).
This runbook is the procedure to (A) re-baseline on a *stable, defensible* corpus
and (B) position code-graph's localizer against the current Loc-Bench SOTA.

## Part A — Re-baseline on a pinned reachable corpus

The problem isn't "fewer instances", it's that "whatever is reachable today" is a
moving, skewed target. Fix: pin a stable recoverable subset once, then always
re-baseline on that exact subset.

```bash
# 1. Partition + pin. Tries GitHub, then Software Heritage (PR #299) for GC'd
#    SHAs, and reports the category skew of the unreachable tail.
python bench/accuracy/locbench_reachability.py \
    --instances bench/research/locbench_n200.json \
    --pin bench/accuracy/locbench_reachable_pin.json

# 2. If max_abs_category_skew > ~0.10, the subset is NOT representative of the
#    full n=560/ n=200 population — say so explicitly in the finding. SWH
#    recovery should pull most Security-Vulnerability force-pushes back in and
#    shrink the skew; report the post-SWH skew, not the GitHub-only skew.

# 3. Re-baseline ONLY the pinned instance ids, same structured scorer + iter=2:
LOCAGENT_ITERATIONS=2 python bench/research/eval_locbench_batch.py \
    --instances bench/accuracy/locbench_reachable_pin.json \
    --subset-key pinned_instance_ids \
    --out bench/accuracy/baselines/$(date +%F)-locbench-pinned.json
python bench/research/eval_locbench_compare.py \
    bench/accuracy/baselines/2026-05-04-loc-bench-n200-iter2.md \
    bench/accuracy/baselines/$(date +%F)-locbench-pinned.json
```

Close per rule 10: either *"re-baselined on the pinned reachable subset (n=…,
max skew Δ=…): file/class/func = …. DONE"*, or keep the 86.0/84.5/73.5 numbers
flagged historical. Whatever the result, the pin makes the **next** re-baseline
apples-to-apples — that is the durable win even before the numbers move.

## Part B — SweRank comparison arm (position against current SOTA)

code-graph's `code_localize_agent` is LocAgent-style agentic graph traversal —
the 2025 paradigm. The field moved to **retrieve-and-rerank**: SweRank
(Salesforce, arXiv 2505.07849) reports ~86.6% file Acc@5 on LocBench and beats
LocAgent on function-level, at a fraction of the agentic cost. We should know
where we stand against it on the *same pinned instances*.

```bash
# CodeRankEmbed (137M retriever) + CodeRankLLM (7B reranker), open weights:
#   https://github.com/gangiswag/SweRank
# Run it over the SAME pinned instance ids + SAME structured scorer so the
# numbers are directly comparable to Part A (not to the SweRank paper's corpus).
python bench/research/eval_rank_localize/run_swerank_arm.py \
    --instances bench/accuracy/locbench_reachable_pin.json \
    --subset-key pinned_instance_ids \
    --retriever nomic-ai/CodeRankEmbed \
    --reranker gangiswag/CodeRankLLM \
    --out bench/accuracy/baselines/$(date +%F)-swerank-pinned.json
python bench/research/eval_locbench_compare.py \
    bench/accuracy/baselines/$(date +%F)-locbench-pinned.json \
    bench/accuracy/baselines/$(date +%F)-swerank-pinned.json
```

(`run_swerank_arm.py` is not yet implemented — it is the one remaining build
step. It must reuse `eval_locbench_*`'s instance loader + structured scorer so
the two arms differ ONLY in the localizer, not the harness.)

### What the comparison tells us

- **code-graph ≥ SweRank on the pinned set** → the graph + agentic approach earns
  its cost; keep investing in receiver-type resolution (Tier-2 v0.2).
- **SweRank > code-graph** → the cheaper retrieve-rerank paradigm wins; consider
  using the graph to *seed/expand* SweRank's candidate set rather than as the
  primary localizer. Either way it's a measured decision, not an assumption.

## Why this is the right lever now

Per the broader evaluation: the localizer's headline number is currently
unreproducible and the paradigm has a newer, cheaper SOTA. Re-baselining on a
pinned corpus (A) restores a defensible number and makes future deltas
trustworthy; the SweRank arm (B) tells us whether the agentic-graph bet is still
the right one before we spend another multi-week Tier-2 cycle on it.
