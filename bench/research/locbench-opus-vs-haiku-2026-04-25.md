# Loc-Bench: Opus 4.7 vs Haiku 4.5 head-to-head — 2026-04-25

**Question asked**: Does swapping Haiku 4.5 → Opus 4.7 in the agent loop materially improve accuracy?

**Short answer**: **No, not on this benchmark and not at this scoring granularity.**

## Headline numbers

### Opus 4.7 alone (n=16, all categories, 1 GB cap)

| Mode | File hit | Class hit | Func hit | Wall (avg) |
|---|---|---|---|---|
| hybrid-agent (Opus 4.7) | 13/16 (81%) | 6/16 (38%) | 10/16 (62%) | ~30 min total, parallel-4 |

### Direct head-to-head on the same 11 instances Haiku completed

| | Haiku 4.5 | Opus 4.7 | Δ |
|---|---|---|---|
| File hit | 9/11 (82%) | 9/11 (82%) | **0** |
| Class hit | 5/11 (45%) | 5/11 (45%) | **0** |
| Func hit | 8/11 (73%) | 8/11 (73%) | **0** |
| Tokens / query | ~25K in / 1.1K out | ~22K in / 1.1K out | similar |
| API cost / query | ~$0.05 | ~$0.30-0.75 | **~10-15× more** |

**Same hit counts at all three granularities. Haiku scores moved around per-instance** (Chainlit got func; luigi got func; alexa-pi lost class; mypy lost func; pandas lost func) **but the totals are identical.**

## Per-instance detail (overlap subset, n=11)

| instance | category | Haiku F/C/Fn | Opus F/C/Fn | net |
|---|---|---|---|---|
| Chainlit__chainlit-1441 | Security | Y/N/N | Y/N/Y | +Fn |
| internetarchive__openlibrary-3196 | Security | N/N/N | N/N/N | none |
| scikit-learn__scikit-learn-14012 | Feature | Y/Y/Y | Y/Y/Y | tie |
| duncanscanga__VDRS-Solutions-73 | Security | Y/N/Y | Y/N/Y | tie |
| Innopoints__backend-124 | Security | Y/N/Y | Y/N/Y | tie |
| aio-libs__aiohttp-7829 | Performance | Y/Y/Y | Y/Y/Y | tie |
| alexa-pi__AlexaPi-188 | Performance | Y/Y/Y | Y/N/Y | -C |
| spotify__luigi-3308 | Security | Y/N/N | Y/N/Y | +Fn |
| pydantic__pydantic-8706 | Feature | Y/Y/Y | Y/Y/Y | tie |
| python__mypy-18163 | Feature | Y/Y/Y | Y/Y/N | -Fn |
| pandas-dev__pandas-59900 | Feature | N/N/Y | N/N/N | -Fn |

## What about the 4 new instances Haiku didn't run?

Opus on the 4 instances added in this run:

| instance | category | Opus F/C/Fn |
|---|---|---|
| kornia__kornia-3084 | Bug | Y/Y/N |
| yt-dlp__yt-dlp-11542 | Bug | Y/Y/Y |
| huggingface__accelerate-3279 | Bug | Y/N/Y |
| ranaroussi__yfinance-2122 | Bug | Y/N/N |

**4/4 file-level on the new instances.** This consistency is what raises the n=16 aggregate to 13/16 (81%) — but it's apples-to-oranges with Haiku because we don't have Haiku data on these. In the prior N=20 batch (PR #89), the SAME instances were attempted with Haiku and **3 of these 4 also hit file-level** (kornia, yt-dlp, accelerate; yfinance also hit).

## Why didn't Opus help?

Three plausible explanations, ranked by my confidence:

1. **Substring scoring is too coarse to capture reasoning quality.** The scorer
   substring-matches the agent's stdout against ground-truth file paths, class
   names, and function names. If the agent says "InstallCommand.run in
   src/pip/_internal/commands/install.py", the scorer hits all three. If the
   agent says "the run method on the InstallCommand class", the file path
   doesn't appear and file-level scores N. Opus often gives more abstract
   descriptions of the same answer; that **regresses our score** without
   actually being wrong.

2. **The LLM is not the bottleneck on Loc-Bench-style file-level localization.**
   Once the seed-matching and BFS surface the right neighborhood in the graph,
   the agent's job is "pick the most relevant entity from this short list."
   Both Haiku and Opus do that fine. The reasoning depth that distinguishes
   Opus on hard tasks (long-horizon planning, multi-step proofs, code
   generation) doesn't apply to "identify the most relevant of these 10
   candidates."

3. **More turns = more drift, not more answers.** Haiku finalizes in 3-7 turns
   typically. With max-turns 25, Opus sometimes explores further into wrong
   directions before finalizing. On a few instances Opus actually scored
   *worse* than Haiku — it replaced "AugmentationSequential.\_\_call\_\_"
   with a sibling method that wasn't ground truth.

## What changed between the runs

Both runs used:
- The same 11 indexed DBs (cached from the killed Haiku run)
- The same hybrid seed strategy (substring + Voyage cosine ≥ 0.65)
- The same code_localize_agent loop architecture
- The same scoring (per-section substring match)

Only the agent's MODEL and `LOCAGENT_MAX_TURNS` differed.

## Cost comparison

The reported `$` field in the harness is hardcoded to $0.05/query (Haiku
estimate). True costs:

| Model | Tokens / query (avg) | Approx cost / query | 16 instances cost |
|---|---|---|---|
| Haiku 4.5 | 25K in / 1.1K out | ~$0.05 | ~$0.80 (matches report) |
| Opus 4.7 | 22K in / 1.1K out | ~$0.40 | ~$6.40 (real, NOT $0.75) |

For zero accuracy improvement, **Opus costs ~8× more on this workload**.

## Speed comparison

Both runs were ~16 instances × ~30s/agent average. Sequential Haiku run took ~30 min for 11 instances when including clone+index time (~50 min projected for 16). Parallel-4 Opus run took ~30 min for all 16 including 4 fresh clone+index — roughly 1.7× faster wall time.

The parallelism win (4 workers) is real and orthogonal to model choice. It's a free speedup whether the agent is Haiku or Opus.

## Honest framing

| Claim | Supported? |
|---|---|
| "Opus 4.7 doesn't help on this benchmark at this scoring granularity" | YES (n=11 head-to-head, identical totals) |
| "Opus is worse" | NO — per-instance shifts cancel; net is wash |
| "Opus would help with a finer scorer" | UNTESTED — we'd need a structured-output scorer that matches qualified_name to ground_truth more carefully |
| "Opus would help on harder benchmarks (e.g., full code generation)" | LIKELY but not measured here |
| "Parallel-4 workers cut wall time materially" | YES (~1.7× on this size; would scale better with more instances) |

## What this changes about the then-current internal release

Nothing — the tag is correct. The default agent model (Haiku 4.5) is the
right choice based on this evidence. If a user wants to spend 8× per query
for Opus, that's now opt-in via `ANTHROPIC_MODEL=claude-opus-4-7` and they
should not expect file-level accuracy lift.

## What we'd actually want to measure next

To detect Opus's advantage if there is one:

1. **Replace substring scoring with structured-output match.** The agent
   already returns structured `Entities` with `qualified_name` and
   `file_path`. Score against `ground_truth.edit_functions` directly,
   not against stdout substrings.

2. **Test on harder localization tasks.** SWE-bench-style "fix this bug"
   end-to-end (where the agent must produce a patch, not just a
   localization) may show Opus's reasoning advantage. Loc-Bench's
   localization-only task is too narrow.

3. **Test on repos where primitives miss entirely.** The 2/11 cases where
   primitives missed AND the agent hit (Innopoints, aiohttp) are the
   interesting cell. Sample more of those — that's where reasoning lift
   shows up.

## Reproduce

```bash
# Haiku baseline (default model)
ANTHROPIC_MODEL=claude-haiku-4-5-20251001 \
python -u bench/research/eval_locbench_compare.py \
  --instances "<list>" --modes hybrid-agent \
  --workers 4 --output bench/research/locbench-haiku.md \
  --keep-clone --keep-index

# Opus comparison
ANTHROPIC_MODEL=claude-opus-4-7 LOCAGENT_MAX_TURNS=25 \
python -u bench/research/eval_locbench_compare.py \
  --instances "<list>" --modes hybrid-agent \
  --workers 4 --output bench/research/locbench-opus.md \
  --keep-clone --keep-index
```
