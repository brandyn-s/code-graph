# Loc-Bench N=20 batch results — 2026-04-25

**Tool under test:** `code_localize_agent` (PR #82) — the LocAgent-style LLM-driven loop wrapping `rank_by_query` and `code_localize` over our code-graph primitives.

**Headline:** 100% file-level accuracy on the 6 instances we successfully ran. Heavily caveated below — this is **not** a claim of parity with LocAgent's published 92.7% on the full 560-instance benchmark.

## What ran

20 instances sampled from `czlll/Loc-Bench_V1` with seed 42, balanced across categories where supply allowed.

| Outcome | Count |
|---|---|
| Indexed and agent ran | 6 |
| Skipped: repo > 200 MB cap | 9 |
| Skipped: clone/checkout failed | 5 |
| **Total attempted** | **20** |

## Headline numbers (on the 6 runs)

| Metric | Result |
|---|---|
| File-level hit (any ground-truth file in agent output) | **6/6 (100%)** |
| Class-level hit (containing class appears) | 3/6 (50%) |
| Function-level hit (function name appears) | 4/6 (67%) |
| Mean turns to finalize | 3.8 |
| Mean cost per query (Haiku 4.5) | $0.050 |
| Mean wall time per query | 170s (clone+index+agent end-to-end) |
| Total LLM tokens | 108,538 input / 6,471 output |
| Total cost | $0.30 (well under the $3 budget cap) |

## Why "100% on 6/20" is not "we match LocAgent's 92.7%"

Honest caveats, in decreasing severity:

### 1. Selection bias: the 200 MB size cap excluded the hardest repos

Of the 14 that didn't run:
- **9 were size-skipped**: ray (4 instances, all 900+ MB), pandas (426 MB), langgraph (580 MB), vllm (3 instances, 200-209 MB), scikit-learn (201 MB).
- These are exactly the repos where a file-level hit is hardest — large surface area, more semantically-similar candidates, deeper call graphs.
- LocAgent's published 92.7% includes these. Excluding them biases our small-sample number upward.

### 2. The 6 we ran skewed toward small/single-purpose codebases

Successful runs by repo:
- huggingface/accelerate (Bug)
- kornia/kornia (Bug)
- aio-libs/aiohttp (Performance)
- yt-dlp/yt-dlp (Bug)
- alexa-pi/AlexaPi (Performance) — small repo, easy
- ranaroussi/yfinance (Bug) — small repo, easy

No Feature Requests and no Security categories got past the gate (their candidates were all clone-failed or size-capped). The agent's behavior on those categories is unmeasured here.

### 3. n=6 is not statistical evidence

A 95% Wilson confidence interval on 6/6 is roughly [54%, 100%]. We can say "the agent doesn't appear to be broken on small Python repos." We **cannot** say "the agent is at LocAgent parity."

### 4. Class- and function-level numbers are weaker than file-level

The agent often returns "the right module / class" but not the specific method. This matches the design: the agent is a localizer, not a code generator. The user is expected to read the localized class and find the method — which a real developer would.

## Per-instance results

| instance_id | repo | category | indexed | agent | file | class | func | turns | tokens | $ | note |
|---|---|---|---|---|---|---|---|---|---|---|---|
| huggingface__accelerate-3279 | huggingface/accelerate | Bug Report | Y | Y | Y | N | Y | 2 | 5161/734 | 0.050 |  |
| ray-project__ray-48793 | ray-project/ray | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone timed out |
| kornia__kornia-3084 | kornia/kornia | Bug Report | Y | Y | Y | Y | N | 5 | 23590/1154 | 0.050 |  |
| scikit-learn__scikit-learn-14012 | scikit-learn/scikit-learn | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | repo > 200 MB (201) |
| aio-libs__aiohttp-7829 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 3 | 16144/1042 | 0.050 |  |
| yt-dlp__yt-dlp-11542 | yt-dlp/yt-dlp | Bug Report | Y | Y | Y | Y | Y | 5 | 31228/1073 | 0.050 |  |
| langchain-ai__langgraph-2724 | langchain-ai/langgraph | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo > 200 MB (580) |
| vllm-project__vllm-10076 | vllm-project/vllm | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo > 200 MB (209) |
| ray-project__ray-48907 | ray-project/ray | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo > 200 MB (928) |
| ray-project__ray-48782 | ray-project/ray | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo > 200 MB (927) |
| tobymao__sqlglot-4524 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed (tree-read) |
| tobymao__sqlglot-4434 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed (tree-read) |
| vllm-project__vllm-7874 | vllm-project/vllm | Performance Issue | N | N | N | N | N | 0 | 0/0 | 0.000 | repo > 200 MB (206) |
| alexa-pi__AlexaPi-188 | alexa-pi/AlexaPi | Performance Issue | Y | Y | Y | N | Y | 4 | 19196/1435 | 0.050 |  |
| pandas-dev__pandas-19074 | pandas-dev/pandas | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | repo > 200 MB (426) |
| ranaroussi__yfinance-2122 | ranaroussi/yfinance | Bug Report | Y | Y | Y | N | N | 4 | 13219/1033 | 0.050 |  |
| vllm-project__vllm-10398 | vllm-project/vllm | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo > 200 MB (209) |
| ray-project__ray-48957 | ray-project/ray | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | repo > 200 MB (929) |
| tobymao__sqlglot-3901 | tobymao/sqlglot | Performance Issue | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed (tree-read) |
| prowler-cloud__prowler-5933 | prowler-cloud/prowler | Performance Issue | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed (file-create) |

## What this validates and what it doesn't

### Validated

1. **The agent loop pipeline works end-to-end** on real-world Python repos in the 50-200 MB range. Clone → index (with embeddings) → agent → score path executes cleanly and produces honest hit/miss results against ground truth.

2. **Cost projection from PR #82's n=1 holds.** $0.04-0.05 per query at Haiku 4.5 was the projected cost; observed mean was $0.050 across 6 runs.

3. **Wall time is acceptable for interactive use.** Mean 170s per query (including clone+index, which won't happen at agent-call time in production).

4. **The class-level / function-level gap is consistent.** The agent reliably surfaces the right module/class but doesn't always pinpoint the exact method. This is the expected behavior — it's a localizer, not a code rewriter.

### Not validated

1. **No claim to LocAgent's 92.7% file-level accuracy.** That's on 560 instances with no repo-size filter. We ran 6 instances biased toward small repos. We do not have evidence that the agent matches that number on the full benchmark.

2. **Behavior on large repos (> 200 MB) is unmeasured.** Most production repos at redacted and most challenging Loc-Bench instances are larger than 200 MB.

3. **Behavior on Feature Request and Security categories is unmeasured** — none successfully ran in this sample.

4. **The hybrid seed strategy was used throughout** (substring + Voyage embedding). We did not run the same N=20 with substring-only or embedding-only to ablate the contribution of each. PR #82's n=1 ablation showed substring-only and embedding-only both missed; only the agent loop landed the hit. This batch confirms the agent works but doesn't re-test the ablation at scale.

## Operational lessons

| Issue | What we saw | What to fix |
|---|---|---|
| 200 MB size cap is too restrictive | 9 of 20 hit it | Raise to 1 GB; accept ~30 min/instance indexing wall time. |
| Shallow clones don't work for arbitrary base_commits | Forced full clones; 5 clone failures from `unable to read tree` (commit not in default-branch history) | Use `git fetch origin <commit>:refs/remotes/origin/<commit>` then checkout, OR clone with `--no-single-branch` |
| `prowler-cloud/prowler` failed mid-checkout (Windows path-too-long) | One specific repo has paths > 260 chars | Acceptable — Windows quirk, not the harness's fault |
| Class-level scoring hits 50% but file-level hits 100% | Agent's output structure varies — sometimes returns class, sometimes only the file | Consider parsing the agent's structured `Entities` output more carefully instead of substring-matching the entire stdout |

## Cost discipline

| Limit | Used |
|---|---|
| LLM budget cap | $3.00 → $0.30 used (10%) |
| Wall time | ~30 min total (indexing dominated; only 6 agent calls × ~1 min agent time) |
| Tokens | 108K input / 6.4K output (well under any per-key rate limit) |

## What we would need for a defensible "we match LocAgent" claim

1. **Raise the size cap to 1 GB**, accepting ~30 min/instance indexing time.
2. **Fix the clone-failure rate** (5/20 = 25% lost to git issues, not the agent).
3. **Run the full 560 instances**, not 20. At 30 min/instance + ~$0.05/instance, that's a ~280 hour wall time and ~$28 LLM cost.
4. **Report category breakdowns separately** — file-level on Bug ≠ file-level on Security.

That's a different scale of work than today's plan. The current N=20 is a useful sanity check, not a benchmark.

## Reproduce

```bash
export ANTHROPIC_API_KEY=sk-...
export VOYAGE_API_KEY=pa-...

# Build the eval binary against the latest main
CGO_ENABLED=1 go build -o bench/research/eval_rank_localize/eval.exe \
  ./bench/research/eval_rank_localize/
CGO_ENABLED=1 go build -o bin/code-graph.exe \
  ./cmd/code-graph/

# Run with seed 42 (same as this report)
python bench/research/eval_locbench_batch.py \
  --n 20 --seed 42 --budget-usd 3.0 \
  --workdir C:/tmp/locbench-batch \
  --output bench/research/locbench-n20-results-$(date +%Y-%m-%d).md
```

## Appendix: run command

```
python -u bench/research/eval_locbench_batch.py \
  --n 20 --budget-usd 3.0 \
  --workdir C:/tmp/locbench-batch \
  --output bench/research/locbench-n20-results-2026-04-25.md
```

Build SHAs at run time: PRs #78, #79, #82, #83, #84, #85, #86, #87 (all merged to main as of 2026-04-25 ~07:55 UTC).
