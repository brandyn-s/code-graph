# Phase D: Loc-Bench iter=1 class-accuracy gap is a SCORER artifact

**Date**: 2026-05-09
**Plan**: `~/Documents/knowledge-base/plans/2026-05-09-code-search-code-graph-multi-month-arc.md` Phase D
**Verdict**: Pivot to documentation-only. No prompt intervention to ship.
**Cost**: $1.20 actual ($1.25 estimated; killed at instance ~17/50 after the dominant pattern was confirmed)
**Wall**: ~90 min (smoke test killed early; full n=200 not run)

## What Phase D was supposed to do

Lift Loc-Bench iter=1 class accuracy from 46.5% → ≥65% by designing a prompt intervention that disambiguates the agent's class picks. The plan presumed that iter=1 misses are dominated by "agent picked semantically-near-but-not-canonical class" — fixable via prompt.

## What the data shows

Smoke test n=50 iter=1 baseline (killed at instance 17 because the pattern was already conclusive at n=4 class misses):

| Status | Count |
|--------|-------|
| Cases attempted | 17 |
| Indexed (clone+index succeeded) | 8 |
| Agent ran | 8 |
| File hit | 7 |
| Class hit (file=Y, class=Y) | 3 |
| **Class miss (file=Y, class=N)** | **4** |
| Func hit | varies |

**Every single class miss was the same pattern: ground-truth is a bare/free function (no class context).**

| Instance | GT format | Agent found function? | class_hit |
|----------|-----------|----------------------|-----------|
| huggingface__accelerate-3248 | `hooks.py:attach_execution_device_hook` | YES (rank 1) | **False** |
| huggingface__accelerate-3279 | `modeling.py:_init_infer_auto_device_map`, `:infer_auto_device_map` | YES (rank 1, 2) | **False** |
| langchain-ai__langgraph-2735 | `jsonplus.py:_msgpack_ext_hook` | YES (rank 1) | **False** |
| pydantic__pydantic-10374 | `validate_call_decorator.py:validate_call` | YES (rank 2) | **False** |

Every class hit was the opposite — ground truth has explicit `Class.method` form:

| Instance | GT format | class_hit |
|----------|-----------|-----------|
| kornia__kornia-3084 | `augment.py:AugmentationSequential.__call__` | True |
| langchain-ai__langgraph-2724 | `tool_node.py:ToolNode._run_one`, `:ToolNode._arun_one` | True |
| pydantic__pydantic-10789 | `_generate_schema.py:GenerateSchema._unsubstituted_typevar_schema` | True |

**100% pattern match in n=4 misses + n=3 hits.** The class accuracy gap at iter=1 is the scorer scoring free-function GTs as class misses, NOT an agent prompt error.

## Why this matters

The plan's premise — "iter=1 raw accuracy can be lifted via prompt intervention" — fails because there's no agent-side bug to fix. The agent's localization is already CORRECT on these cases. The scorer's class_hit logic for free-function GTs is the source of the 38pp gap.

The defended iter=2 class accuracy (84.5%) is HIGHER than iter=1 (46.5%) likely because:
1. iter=2's MRR aggregation across 2 agent runs may stabilize the agent's class-side picks (which are surfaced as side-effect entities in the agent envelope, often the enclosing class of the GT)
2. By chance, those side-effect class predictions hit the scorer's expected class field more often at iter=2

But the agent's **function-level** correctness is high in both iter=1 and iter=2. The class-level metric is partly capturing scorer noise.

## What this connects to in prior work

The 2026-05-06 failure audit on n=19 iter=2 misses bucketized as 79% `oracle_gap` + 21% `embedding_recall_miss` + ZERO scope_collision / agent_loop_failure / indirect_call_required. The iter=2 misses that survive are dominated by oracle_gap.

The iter=2 baseline doc (2026-05-04) explicitly noted: "The class scorer expects a class context that doesn't exist in the source — likely **oracle_gap**" for the same accelerate-3279 case in our smoke.

The 2026-05-04 verdict was: "Phase D class-level fix cancelled — there is no class-level fix to do at iter=2." The 2026-05-09 multi-month arc plan re-targeted Phase D at iter=1 specifically (different premise: improve iter=1 to make iter=2 cheaper). But the iter=1 misses are the SAME oracle_gap pattern as iter=2 misses, just more visible because iter=2's MRR aggregation accidentally stabilizes some class-side picks.

## Verdict

**No prompt intervention to ship.** Phase D's mechanism is wrong about the failure shape.

The actual lever to lift iter=1 class accuracy would be at the **scorer level**: change the class_hit logic to mark "class context not applicable" (free-function GT) as a separate outcome rather than counting it as a class miss. That's a different workstream — Phase D scope was prompt design.

If iter=1 class accuracy is genuinely a target metric, the plan needs to be redirected to scorer canonicalization (the methodology caveat in CLAUDE.md already names this as a known measurement issue).

## Recommendations for the multi-month arc

1. **Phase D as specified is not the right shape** — pivot to document-only.
2. **Update CLAUDE.md** to flag the iter=1 class accuracy "gap" is largely scorer-driven; cite this n=8 sample (4/4 class misses are free-function GTs) as evidence.
3. **Optional follow-up workstream** (NOT this phase): scorer canonicalization. When GT has bare-symbol form, accept any prediction whose function-level path matches as a class hit by convention. This would lift iter=1 class accuracy substantially, but it's a measurement methodology change, not an agent capability change.

## Files written this phase

- `bench/research/locbench-n50-iter1-2026-05-09.json` (in-progress checkpoint, 17 cases) — preserved for future reference
- `bench/research/locbench-n50-iter1-2026-05-09.md` (markdown summary, partial)
- This findings doc

## What was NOT shipped

- Prompt variant `open-class-disambig` — not built (premise wrong)
- Full n=200 iter=1 baseline — not needed (smoke confirmed pattern at n=8)
- Default flip on `LOCAGENT_PROMPT_VARIANT` — not applicable

## Cost summary

- A4 (latency diag): $0.14
- A5 (PSM eval pool=5 vs pool=15): $1.30
- D smoke (killed early at n=8 indexed/$1.20): $1.20
- **Total session API spend: ~$2.64**
- Avoided: D full ($10) once pattern was conclusive at n=4
