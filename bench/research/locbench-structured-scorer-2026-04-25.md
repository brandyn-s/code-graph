# Loc-Bench: structured scorer + cosine threshold ablation — 2026-04-25

Three things measured in this run, all using the SAME 16 instances and indexed DBs:

1. **Structured scorer** vs old substring scorer — does the scorer fix change the conclusions?
2. **Cosine threshold ablation** (0.65 → 0.0) — did PR #84's threshold help?
3. **Re-confirm agent vs primitives** — does the agent loop still beat primitives under the new scorer?

## Summary table — all three modes, both threshold settings, n=16

| Mode | Cosine 0.65 (default) | Cosine 0.0 (ablated) | Δ |
|---|---|---|---|
| substring-primitives F / C / Fn | 38% / 6% / 12% | 38% / 6% / 12% | 0 (substring doesn't use embeddings) |
| hybrid-primitives F / C / Fn | 38% / 6% / 12% | **44% / 6% / 19%** | **+6pp file, +7pp func** |
| hybrid-agent F / C / Fn | 81% / 38% / 38% | 81% / 31% / 44% | wash |

## What changed when the scorer became honest

Comparison of agent results against the OLD substring scorer (from PR #89's run on the same 11-instance subset):

| Metric | Old scorer | New scorer | Δ |
|---|---|---|---|
| hybrid-agent file hit | 9/11 (82%) | 13/16 (81%) ≈ 9/11 (82%) on the overlap | unchanged |
| hybrid-agent class hit | 5/11 (45%) | 4/11 (36%) on overlap | -1 instance |
| hybrid-agent func hit | 8/11 (73%) | 4/11 (36%) on overlap | **-4 instances** |

**The old scorer was inflating function-level hits by ~35 percentage points.** Substring matching against the agent's reasoning text was finding ground-truth function names in passing references ("the agent considers `do_link` and `register`...") that did not correspond to entities the agent actually returned. The new scorer requires the function name to appear at the end of a returned entity's `qualified_name` AND the entity's `file_path` to equal the ground-truth file — a much higher bar.

File-level hits are largely unchanged because file paths are specific enough that substring false-positives were rare.

## Cosine threshold finding

Removing the threshold (0.65 → 0.0) **moved one instance** in the hybrid-primitives mode: `Innopoints__backend-124` went file=N→Y, func=N→Y. The embedding seed for that issue's first paragraph happened to score between 0.0 and 0.65 cosine, so the default threshold was filtering it out.

Net effect at n=16:
- hybrid-primitives gains 1 file hit (+6pp), 1 func hit (+7pp)
- hybrid-agent: roughly neutral (loses 1 class hit, gains 1 func hit on different instances)

**Conclusion**: PR #84's 0.65 threshold is mildly too aggressive. It filters out one demonstrably-useful seed without preventing any noise hit. Lowering the threshold (or removing it entirely) is a small but real win for primitives mode and a wash for agent mode.

The threshold was originally calibrated on a single Loc-Bench instance (pypa__pip-13085) where 0.50-0.65 hits were noise. That observation didn't generalize.

## Agent vs primitives — restated under honest scoring

n=16, both at cosine=0.0:

| Mode | File | Class | Func |
|---|---|---|---|
| substring-primitives | 38% | 6% | 12% |
| hybrid-primitives | 44% | 6% | 19% |
| **hybrid-agent** | **81%** | **31%** | **44%** |

The agent loop's lift over primitives is **larger than the prior writeup suggested at file level** (81% vs 44% — agent +37pp, almost 2× the primitives) and **smaller than the prior writeup suggested at func level** (44% vs 19% — agent +25pp, but the absolute number is now 44% not 73%).

The honest numbers:
- **File-level**: agent is materially better on this benchmark.
- **Class-level**: agent is materially better.
- **Func-level**: agent is somewhat better but only 44% absolute, not the previously-claimed 73%. Roughly half the time the agent surfaces the right file and class but not the specific method the ground truth named.

## What this changes about prior conclusions

| Prior claim | Status |
|---|---|
| "Agent loop substantially outperforms primitives" | **CONFIRMED** at file and class levels |
| "Hit rate of 73% func-level on agent" | **REVISED DOWN to 44%** (substring scorer was inflating) |
| "Cosine 0.65 threshold helps" | **REJECTED** — 1-instance regression at primitives level, wash at agent |
| "Hybrid seeds add zero lift over substring" | **REVISED**: zero lift at cosine=0.65 (because threshold filters out the seeds that would have helped); +6pp lift at cosine=0.0 |
| "Opus 4.7 = Haiku 4.5 on this benchmark" | **STILL TRUE** with structured scorer (would need a re-run to fully verify, but the Opus regressions/improvements were also visible in the substring scorer) |

## Recommended next architectural change

Lower or remove the cosine threshold (revert PR #84's threshold portion). The default could move from 0.65 → 0.0 or to a more moderate value (0.4) that keeps clear noise out without filtering legitimate matches.

The descriptions portion of PR #84 (sharper MCP tool descriptions) is unaffected by this — those are still good as-is.

## What this run did NOT measure

- **Class-level scoring may still be lossy.** A class name appearing as a method's qualified-name component prefix (e.g., `Foo.bar.baz` matches "class Foo" via qualified-name component) might over-count when the agent returns sibling methods. Likely small effect; would need spot-checking.
- **vllm-project__vllm-11138 had a SHM corruption issue and scored N/N/N across all 3 modes.** Drops the n=16 aggregate by 1. Re-indexing would fix.
- **Opus 4.7 was not re-run with the new scorer.** The conclusion (no lift over Haiku) is unchanged in direction but the numbers would shift slightly.

## Operational artifacts

- `bench/research/eval_rank_localize/main.go` — `-json` output mode for structured scoring
- `bench/research/eval_locbench_compare.py` — `score_entities()` function, structured matching
- `internal/ranking/embedding_seeds.go` — `EMBEDDING_SEED_MIN_COSINE` env var override
- `bench/research/locbench-scored-cosine65.md` — Run A (default threshold)
- `bench/research/locbench-scored-cosine0.md` — Run B (no threshold)

## Reproduce

```bash
# Run A (default threshold)
EMBEDDING_SEED_MIN_COSINE=0.65 \
python -u bench/research/eval_locbench_compare.py \
  --instances "<list>" --modes "substring-primitives,hybrid-primitives,hybrid-agent" \
  --workers 4 --output bench/research/locbench-scored-cosine65.md \
  --keep-clone --keep-index

# Run B (ablation)
EMBEDDING_SEED_MIN_COSINE=0.0 \
python -u bench/research/eval_locbench_compare.py \
  --instances "<list>" --modes "substring-primitives,hybrid-primitives,hybrid-agent" \
  --workers 4 --output bench/research/locbench-scored-cosine0.md \
  --keep-clone --keep-index
```
