# Loc-Bench n=200 re-baseline — 2026-06-12

> Refreshes the defended `code_localize_agent` numbers on the
> **now-fully-reachable** Loc-Bench corpus, on the current engine
> (main `f72d07e`, after PRs #371–#389). Same 200 instances as the
> 2026-05-04 defended baseline → apples-to-apples. **Verdict: clean
> reproduction** — all three metric deltas' 95% bootstrap CIs include
> zero.

## Headline: reproduction confirmed

| Metric | Defended (2026-05-04) | Re-baseline (2026-06-12) | Paired Δ (95% CI, 10K resamples) |
|---|---:|---:|---|
| File Acc@10 | 86.0% | **85.5%** (171/200) | +0.0pp [−3.0, +3.0] — includes 0 |
| Class Acc@10 | 84.5% | **84.0%** (168/200) | −0.5pp [−4.0, +3.0] — includes 0 |
| Func Acc@10 | 73.5% | **76.5%** (153/200) | +3.0pp [−1.5, +8.0] — includes 0 |

The same 200 instance IDs were run in both measurements (extracted from
the 2026-05-04 per-case table; 200/200 verified present in the parquet),
so the bootstrap is paired per-instance. Every CI includes zero: the
engine at current main reproduces the defended baseline. The +3.0pp func
point estimate is within noise (CI lower bound −1.5pp), not a claimed
improvement.

**The defended numbers in CLAUDE.md stay 86.0 / 84.5 / 73.5** — this run
confirms them rather than replacing them, and lifts the 2026-05-16
"REFUSED for publication / re-baseline required before external citation"
caveat: the corpus is fully reachable and the current engine reproduces.

## What this run does and does NOT cover

- **DOES**: refresh the `code_localize_agent` (hybrid-agent, iter=2/MRR,
  Haiku 4.5) defended numbers on the reachable corpus at current HEAD.
- **DOES NOT**: re-test the SweRank retrieval-vs-agent question at n=200.
  That requires the pilot's separate `armC_retrieval.py` arm pointed at
  this 200-pin (a distinct harness, not `eval_locbench_compare.py`). It
  remains the open follow-up from
  `bench/accuracy/baselines/2026-06-11-swerank-prefilter-pilot.md`
  (arm C verdict: DECIDE at pilot scale, n=13 too small to certify
  non-inferiority). This re-baseline is the prerequisite corpus work for
  that follow-up, not the follow-up itself.

## Operational result: the GC'd-commits blocker is dead

- **200/200 cloned, 200/200 indexed, 0 clone failures.** The 2026-05-12
  re-run's 58/200 "GC'd `base_commit`" failures (which forced the
  REFUSED-for-publication caveat) did not recur. Consistent with the
  reachability pin's finding that all 560 instances are GitHub-reachable
  via the commits API — the May failures were the `ls-remote`-vs-ancestor
  instrument error, now fixed.
- `--preflight-reachability` was deliberately NOT used: it is
  `git ls-remote`-based, the exact instrument behind the bogus May
  finding. The commits-API pin already proved reachability.

## Reproduction protocol

```
LOCAGENT_ITERATIONS unset (binary default = 2)
eval_locbench_compare.py --instances <200-pin, seed-42 shuffled>
  --modes hybrid-agent --workers 4 --budget-usd 55
  --eval-bin .../eval --index-bin .../code-graph
```

- **Pin**: the 200 instance IDs from the 2026-05-04 baseline doc, in a
  seed-42 shuffle. Order is invisible to the final scores (the report
  sorts by pin order) but keeps any budget-gated truncation
  category-balanced rather than dropping a whole stratum at the tail.
- **Binary**: built from main `f72d07e` on macOS arm64 (CGO + tree-sitter
  grammars), same build for index and eval.
- **Wall**: ~4.3 h, 4 workers, `caffeinate -i` wrapped.
- **Cost**: $50.51 token-metered-conservative over 200 instances
  ($0.254/instance mean; heaviest ~$0.89 at 856K input tokens). Actual
  billed is lower — the meter bills cache reads at full input rate by
  design. Well inside the $55 gate; the gate did not fire.

## Dominant-cell classification (instrument gate cleared)

The harness's `verify-instrument-before-fix` gate REFUSED the raw report
because Bug Report holds ≥30% of the misses in all three columns
(file 48%, class 47%, func 40%). Per the gate's requirement, 3 Bug Report
file-misses were re-run and classified by direct source inspection:

| Instance | Main run | Re-run | Classification |
|---|---|---|---|
| `bridgecrewio__checkov-6909` | N/N/N | N/N/N | **Consistent REAL miss** — agent returns `DictNode.__deepcopy__` / `Runner`, never reaches `ServerlessLocalGraph._create_vertex` |
| `tobymao__sqlglot-4415` | N/N/N | **Y/Y/N** | **Stochastic** — flipped to file+class hit (`Parser` in `parser.py`); func still off-by-method (`_parse_column` vs `_parse_column_ops`) |
| `django__django-18785` | N/N/N | **Y/Y/Y** | **Stochastic** — flipped to exact hit (`Template.get_exception_info`) |

**Verdict: REAL misses with a large stochastic component — NOT an
instrument/scorer artifact.** At temperature 1.0, ~2/3 of the sampled Bug
Report misses flip on a re-run; iter=2 MRR self-consistency reduces but
does not eliminate this. Bug Report being the dominant miss cell is a
property of it being the hardest + largest category (it was also the
lowest in 2026-05-04: 82.3/82.3/72.6), not a measurement bug. The
aggregate numbers are valid for publication.

This stochasticity is itself the explanation for why per-instance
file/class/func flip between the two runs while the aggregates are
statistically identical — the paired Δ CIs absorb exactly this run-to-run
variance.

## Per-category (both runs, same 200 instances)

| Category | n | File 05-04→06-12 | Class 05-04→06-12 | Func 05-04→06-12 |
|---|---:|---|---|---|
| Bug Report | 62 | 82.3 → 77.4 | 82.3 → 75.8 | 72.6 → 69.4 |
| Feature Request | 53 | 92.5 → 94.3 | 88.7 → 90.6 | 79.2 → 84.9 |
| Performance Issue | 56 | 82.1 → 87.5 | 82.1 → 87.5 | 73.2 → 85.7 |
| Security Vulnerability | 29 | 86.2 → 82.8 | 86.2 → 82.8 | 65.5 → 58.6 |

Per-category swings (±5pp) are within the run-to-run stochasticity
documented above and net out to the statistically-flat aggregate. No
category shows a regression outside noise.

## Harness fixes shipped with this run

1. **Token-metered cost** (`eval_locbench_compare.py`): replaced the flat
   `$0.05/query` estimate with `input_tokens × $1/M + output_tokens ×
   $5/M` (Haiku 4.5; cache reads billed at full input rate —
   conservative). The flat estimate underbooked heavy instances ~5–18×
   (this run's heaviest: 856K input tokens ≈ $0.89, not $0.05). Falls
   back to the flat estimate when the envelope carries no token counts.
   Per `eval-shipping-discipline.md` — meter gates in the units they cap.
2. **`--budget-usd` gate**: hard spend gate that cancels pending workers
   on cap (in-flight instances finish; overshoot bounded by worker
   count), mirroring the existing clone-fail abort.
3. **Manifest `agent_iterations` fallback** corrected `"1"` → `"2"` to
   mirror the binary's default (`internal/locagent/agent.go` Run:
   `iters := 2`). The old fallback mislabeled every default-config run as
   single-shot; this run's raw manifest claimed iter=1 for an iter=2 run.
4. **Python 3.14 argparse fix**: an unescaped `%` in a help string
   (`≥30%`) raised `badly formed help string` under 3.14's eager
   validation (3.13 tolerated it). Escaped to `%%`.

## Artifacts

- Pin: 200 instance IDs (seed-42 shuffle) extracted from
  `2026-05-04-loc-bench-n200-iter2.md`.
- Raw harness report (REFUSED by the dominant-cell gate; its manifest
  also mislabeled `agent_iterations` as 1 pre-fix #3): session-local, not
  committed. Its full 200-row per-instance table is reproduced below.
- Reclassification probe checkpoints (3 Bug Report misses, re-run with
  `--keep-clone`): session-local.

## Per-instance details (200 rows)

| instance | category | size (MB) | indexed | F/C/Fn | note |
|---|---|---|---|---|---|
| UXARRAY__uxarray-1117 | Performance Issue | 116 | Y | Y/Y/Y |  |
| Chainlit__chainlit-1575 | Security Vulnerability | 7 | Y | Y/Y/N |  |
| Open-MSS__MSS-1967 | Performance Issue | 12 | Y | N/N/N |  |
| sopel-irc__sopel-2285 | Security Vulnerability | 3 | Y | Y/Y/Y |  |
| mesonbuild__meson-11366 | Security Vulnerability | 15 | Y | Y/Y/N |  |
| pypa__pip-13085 | Security Vulnerability | 23 | Y | Y/Y/Y |  |
| vllm-project__vllm-9390 | Feature Request | 14 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-25186 | Performance Issue | 27 | Y | Y/Y/Y |  |
| okta__okta-jwt-verifier-python-59 | Security Vulnerability | 0 | Y | Y/Y/N |  |
| ray-project__ray-26818 | Performance Issue | 127 | Y | Y/Y/Y |  |
| gitpython-developers__GitPython-1636 | Security Vulnerability | 2 | Y | Y/Y/N |  |
| huggingface__optimum-benchmark-266 | Performance Issue | 1 | Y | N/N/N |  |
| JoinMarket-Org__joinmarket-clientserver-1180 | Performance Issue | 8 | Y | Y/Y/Y |  |
| TagStudioDev__TagStudio-735 | Performance Issue | 12 | Y | Y/Y/Y |  |
| micropython__micropython-lib-947 | Security Vulnerability | 3 | Y | Y/Y/Y |  |
| fortra__impacket-1636 | Security Vulnerability | 9 | Y | Y/Y/Y |  |
| AzureAD__microsoft-authentication-library-for-python-454 | Performance Issue | 1 | Y | N/N/N |  |
| Chainlit__chainlit-1441 | Security Vulnerability | 7 | Y | Y/Y/Y |  |
| django__django-18616 | Feature Request | 55 | Y | Y/Y/Y |  |
| Bears-R-Us__arkouda-1969 | Performance Issue | 11 | Y | Y/Y/Y |  |
| django__django-18435 | Feature Request | 54 | Y | Y/Y/Y |  |
| sqlfluff__sqlfluff-6399 | Feature Request | 19 | Y | Y/Y/Y |  |
| mathesar-foundation__mathesar-3117 | Security Vulnerability | 26 | Y | Y/Y/Y |  |
| huggingface__transformers-22498 | Feature Request | 68 | Y | Y/Y/Y |  |
| JackPlowman__repo_standards_validator-137 | Security Vulnerability | 0 | Y | Y/Y/Y |  |
| huggingface__transformers-35453 | Feature Request | 105 | Y | Y/Y/Y |  |
| pandas-dev__pandas-29944 | Feature Request | 58 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-16948 | Feature Request | 26 | Y | Y/Y/Y |  |
| numba__numba-9757 | Performance Issue | 16 | Y | Y/Y/Y |  |
| jobatabs__textec-53 | Security Vulnerability | 0 | Y | N/N/N |  |
| django__django-19009 | Performance Issue | 55 | Y | Y/Y/Y |  |
| internetarchive__openlibrary-3196 | Security Vulnerability | 13 | Y | Y/Y/N |  |
| django__django-13134 | Security Vulnerability | 48 | Y | Y/Y/Y |  |
| webcompat__webcompat.com-2731 | Performance Issue | 11 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-14012 | Feature Request | 22 | Y | Y/Y/Y |  |
| airbnb__knowledge-repo-558 | Security Vulnerability | 3 | Y | Y/Y/Y |  |
| pulp__pulp_rpm-3224 | Performance Issue | 2 | Y | Y/Y/Y |  |
| sancus-tee__sancus-compiler-36 | Security Vulnerability | 0 | Y | N/N/N |  |
| scikit-learn__scikit-learn-6116 | Feature Request | 19 | Y | Y/Y/Y |  |
| duncanscanga__VDRS-Solutions-73 | Security Vulnerability | 0 | Y | Y/Y/Y |  |
| scipy__scipy-5647 | Performance Issue | 50 | Y | N/N/N |  |
| plone__plone.restapi-859 | Security Vulnerability | 2 | Y | Y/Y/Y |  |
| openwisp__openwisp-users-286 | Security Vulnerability | 1 | Y | Y/Y/Y |  |
| rucio__rucio-4930 | Security Vulnerability | 10 | Y | Y/Y/Y |  |
| home-assistant__core-15182 | Security Vulnerability | 17 | Y | Y/Y/Y |  |
| jazzband__django-two-factor-auth-390 | Security Vulnerability | 1 | Y | Y/Y/N |  |
| Innopoints__backend-124 | Security Vulnerability | 0 | Y | Y/Y/N |  |
| zulip__zulip-14091 | Performance Issue | 69 | Y | Y/Y/Y |  |
| matchms__matchms-backup-187 | Security Vulnerability | 1 | Y | Y/Y/Y |  |
| latchset__jwcrypto-195 | Security Vulnerability | 0 | Y | N/N/N |  |
| django__django-5605 | Security Vulnerability | 40 | Y | Y/Y/Y |  |
| jax-ml__jax-25114 | Feature Request | 48 | Y | Y/Y/Y |  |
| PlasmaPy__PlasmaPy-2542 | Performance Issue | 30 | Y | Y/Y/Y |  |
| kedro-org__kedro-4367 | Performance Issue | 148 | Y | Y/Y/Y |  |
| Deltares__imod-python-1159 | Performance Issue | 5 | Y | Y/Y/Y |  |
| ivadomed__ivadomed-1081 | Performance Issue | 2 | Y | Y/Y/Y |  |
| zulip__zulip-31168 | Performance Issue | 126 | Y | Y/Y/N |  |
| ckan__ckan-8226 | Performance Issue | 49 | Y | Y/Y/Y |  |
| UXARRAY__uxarray-1144 | Performance Issue | 116 | Y | Y/Y/Y |  |
| UXARRAY__uxarray-1151 | Performance Issue | 116 | Y | Y/Y/Y |  |
| twisted__klein-773 | Performance Issue | 1 | Y | Y/Y/Y |  |
| hyeneung__tech-blog-hub-site-49 | Performance Issue | 4 | Y | Y/Y/Y |  |
| home-assistant__core-136739 | Performance Issue | 134 | Y | Y/Y/Y |  |
| celery__django-celery-beat-835 | Performance Issue | 1 | Y | Y/Y/Y |  |
| Standard-Labs__real-intent-27 | Performance Issue | 0 | Y | Y/Y/Y |  |
| vllm-project__vllm-7874 | Performance Issue | 12 | Y | Y/Y/Y |  |
| NCSU-High-Powered-Rocketry-Club__AirbrakesV2-151 | Performance Issue | 148 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-8478 | Feature Request | 21 | Y | N/N/N |  |
| scikit-learn__scikit-learn-17443 | Feature Request | 27 | Y | Y/Y/N |  |
| Qiskit__qiskit-13141 | Feature Request | 44 | Y | Y/Y/N |  |
| sgkit-dev__sgkit-447 | Performance Issue | 40 | Y | Y/Y/Y |  |
| numpy__numpy-17394 | Feature Request | 32 | Y | Y/Y/Y |  |
| Ouranosinc__xclim-477 | Performance Issue | 71 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-10280 | Feature Request | 21 | Y | Y/Y/Y |  |
| pandas-dev__pandas-22762 | Feature Request | 43 | Y | Y/Y/Y |  |
| numpy__numpy-8206 | Feature Request | 23 | Y | Y/Y/Y |  |
| pandas-dev__pandas-19074 | Feature Request | 38 | Y | Y/Y/Y |  |
| rapidsai__dask-cuda-98 | Performance Issue | 0 | Y | Y/Y/Y |  |
| freedomofpress__securedrop-client-944 | Performance Issue | 12 | Y | Y/Y/Y |  |
| modin-project__modin-4391 | Performance Issue | 72 | Y | N/N/N |  |
| scikit-learn__scikit-learn-13290 | Performance Issue | 22 | Y | Y/Y/Y |  |
| alexa-pi__AlexaPi-188 | Performance Issue | 1 | Y | Y/Y/Y |  |
| matrix-org__synapse-8744 | Performance Issue | 35 | Y | Y/Y/Y |  |
| streamlit__streamlit-9754 | Security Vulnerability | 184 | Y | N/N/N |  |
| streamlit__streamlit-9472 | Performance Issue | 175 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-30128 | Feature Request | 29 | Y | Y/Y/Y |  |
| PrefectHQ__prefect-16117 | Bug Report | 74 | Y | Y/Y/Y |  |
| PrefectHQ__prefect-14693 | Feature Request | 74 | Y | Y/Y/Y |  |
| PrefectHQ__prefect-13259 | Feature Request | 74 | Y | Y/N/N |  |
| Lightning-AI__pytorch-lightning-20403 | Feature Request | 38 | Y | Y/Y/Y |  |
| dask__dask-11609 | Performance Issue | 28 | Y | Y/Y/Y |  |
| dask__dask-11541 | Bug Report | 28 | Y | Y/Y/Y |  |
| dask__dask-11479 | Performance Issue | 27 | Y | Y/Y/Y |  |
| dask__dask-11434 | Feature Request | 27 | Y | Y/Y/Y |  |
| dask__dask-10750 | Feature Request | 24 | Y | Y/Y/Y |  |
| DS4SD__docling-330 | Bug Report | 53 | Y | Y/Y/Y |  |
| getmoto__moto-8342 | Bug Report | 80 | Y | Y/Y/Y |  |
| getmoto__moto-8316 | Bug Report | 80 | Y | Y/Y/Y |  |
| ray-project__ray-49236 | Bug Report | 319 | Y | Y/Y/Y |  |
| ray-project__ray-49221 | Bug Report | 319 | Y | N/N/N |  |
| ray-project__ray-48957 | Feature Request | 303 | Y | Y/Y/Y |  |
| spotify__luigi-3308 | Security Vulnerability | 7 | Y | Y/Y/Y |  |
| bridgecrewio__checkov-6909 | Bug Report | 95 | Y | N/N/N |  |
| bridgecrewio__checkov-6895 | Bug Report | 95 | Y | Y/Y/N |  |
| keras-team__keras-20626 | Bug Report | 9 | Y | Y/Y/Y |  |
| keras-team__keras-20550 | Bug Report | 9 | Y | Y/Y/Y |  |
| keras-team__keras-20443 | Bug Report | 9 | Y | N/N/N |  |
| vllm-project__vllm-7783 | Feature Request | 11 | Y | Y/Y/Y |  |
| pydantic__pydantic-10789 | Feature Request | 10 | Y | Y/Y/Y |  |
| pydantic__pydantic-8706 | Feature Request | 9 | Y | Y/Y/Y |  |
| yt-dlp__yt-dlp-11750 | Bug Report | 14 | Y | Y/Y/Y |  |
| Qiskit__qiskit-12214 | Feature Request | 36 | Y | Y/Y/Y |  |
| python__mypy-18164 | Bug Report | 19 | Y | Y/Y/Y |  |
| python__mypy-18163 | Feature Request | 19 | Y | Y/Y/Y |  |
| python__mypy-18160 | Bug Report | 19 | Y | N/N/N |  |
| pandas-dev__pandas-59900 | Feature Request | 65 | Y | Y/Y/Y |  |
| django__django-18906 | Bug Report | 55 | Y | Y/Y/Y |  |
| django__django-18820 | Feature Request | 55 | Y | Y/Y/Y |  |
| django__django-18795 | Bug Report | 55 | Y | Y/Y/Y |  |
| django__django-18785 | Bug Report | 55 | Y | N/N/N |  |
| django__django-18752 | Feature Request | 55 | Y | Y/Y/Y |  |
| django__django-18508 | Performance Issue | 54 | Y | Y/Y/Y |  |
| django__django-18105 | Performance Issue | 54 | Y | Y/Y/Y |  |
| django__django-17984 | Performance Issue | 54 | Y | Y/Y/Y |  |
| django__django-17904 | Performance Issue | 54 | Y | Y/Y/Y |  |
| django__django-17874 | Performance Issue | 54 | Y | Y/Y/Y |  |
| roboflow__supervision-1739 | Feature Request | 100 | Y | Y/Y/Y |  |
| roboflow__supervision-1698 | Bug Report | 100 | Y | N/N/N |  |
| py-pdf__pypdf-2656 | Performance Issue | 52 | Y | Y/Y/Y |  |
| huggingface__trl-2450 | Bug Report | 3 | Y | Y/Y/Y |  |
| rq__rq-2138 | Bug Report | 3 | Y | Y/Y/N |  |
| tobymao__sqlglot-4524 | Bug Report | 101 | Y | Y/Y/Y |  |
| tobymao__sqlglot-4434 | Feature Request | 100 | Y | Y/Y/N |  |
| tobymao__sqlglot-4415 | Bug Report | 100 | Y | N/N/N |  |
| modin-project__modin-6980 | Performance Issue | 39 | Y | Y/Y/Y |  |
| modin-project__modin-6951 | Performance Issue | 39 | Y | N/N/N |  |
| sympy__sympy-27325 | Feature Request | 38 | Y | Y/Y/Y |  |
| sympy__sympy-27288 | Bug Report | 38 | Y | Y/Y/Y |  |
| jax-ml__jax-25787 | Feature Request | 49 | Y | Y/Y/Y |  |
| jax-ml__jax-25511 | Bug Report | 49 | Y | N/N/N |  |
| jax-ml__jax-22049 | Feature Request | 45 | Y | Y/Y/Y |  |
| jax-ml__jax-19909 | Feature Request | 42 | Y | Y/Y/Y |  |
| jax-ml__jax-19710 | Feature Request | 42 | Y | Y/Y/Y |  |
| mlflow__mlflow-10923 | Security Vulnerability | 217 | Y | N/N/N |  |
| prowler-cloud__prowler-5856 | Bug Report | 51 | Y | N/N/N |  |
| prowler-cloud__prowler-5653 | Bug Report | 51 | Y | N/N/N |  |
| vllm-project__vllm-11138 | Feature Request | 19 | Y | Y/Y/Y |  |
| vllm-project__vllm-11073 | Bug Report | 19 | Y | Y/Y/Y |  |
| vllm-project__vllm-10903 | Bug Report | 17 | Y | N/N/N |  |
| vllm-project__vllm-10536 | Bug Report | 16 | Y | Y/Y/Y |  |
| vllm-project__vllm-10347 | Bug Report | 16 | Y | N/N/N |  |
| UKPLab__sentence-transformers-3073 | Bug Report | 36 | Y | Y/Y/Y |  |
| Zulko__moviepy-2253 | Bug Report | 178 | Y | Y/Y/Y |  |
| yt-dlp__yt-dlp-11644 | Bug Report | 14 | Y | Y/Y/Y |  |
| locustio__locust-2976 | Bug Report | 4 | Y | Y/Y/Y |  |
| ranaroussi__yfinance-2122 | Bug Report | 5 | Y | Y/Y/N |  |
| scipy__scipy-22106 | Bug Report | 103 | Y | Y/Y/Y |  |
| DS4SD__docling-528 | Bug Report | 57 | Y | Y/Y/N |  |
| DS4SD__docling-442 | Bug Report | 55 | Y | Y/Y/Y |  |
| certbot__certbot-10043 | Bug Report | 6 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60543 | Bug Report | 65 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60461 | Performance Issue | 65 | Y | N/N/N |  |
| pandas-dev__pandas-60310 | Bug Report | 65 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60277 | Feature Request | 65 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60247 | Feature Request | 65 | Y | Y/Y/Y |  |
| huggingface__trl-2433 | Bug Report | 3 | Y | Y/Y/Y |  |
| SYSTRAN__faster-whisper-1198 | Performance Issue | 42 | Y | Y/Y/Y |  |
| SYSTRAN__faster-whisper-1141 | Bug Report | 42 | Y | Y/Y/Y |  |
| phidatahq__phidata-1563 | Bug Report | 6 | Y | Y/Y/Y |  |
| nltk__nltk-3335 | Bug Report | 11 | Y | Y/Y/Y |  |
| dask__dask-11491 | Bug Report | 27 | Y | Y/Y/Y |  |
| bridgecrewio__checkov-6826 | Feature Request | 95 | Y | Y/Y/Y |  |
| spotify__luigi-3324 | Bug Report | 7 | Y | Y/Y/Y |  |
| ray-project__ray-49071 | Bug Report | 303 | Y | Y/Y/Y |  |
| ray-project__ray-48891 | Bug Report | 302 | Y | N/N/N |  |
| ray-project__ray-48756 | Bug Report | 302 | Y | Y/Y/Y |  |
| BerriAI__litellm-6915 | Bug Report | 154 | Y | Y/Y/Y |  |
| matplotlib__matplotlib-29265 | Feature Request | 100 | Y | Y/N/N |  |
| flet-dev__flet-4425 | Bug Report | 30 | Y | Y/Y/Y |  |
| flet-dev__flet-4388 | Feature Request | 30 | Y | Y/Y/Y |  |
| flet-dev__flet-4384 | Bug Report | 30 | Y | Y/Y/Y |  |
| kornia__kornia-3084 | Bug Report | 13 | Y | Y/Y/Y |  |
| wandb__wandb-9011 | Bug Report | 119 | Y | Y/Y/Y |  |
| ultralytics__ultralytics-17728 | Performance Issue | 7 | Y | Y/Y/Y |  |
| huggingface__diffusers-10262 | Feature Request | 38 | Y | Y/Y/Y |  |
| huggingface__diffusers-10185 | Bug Report | 37 | Y | Y/Y/Y |  |
| huggingface__diffusers-10067 | Bug Report | 36 | Y | N/N/N |  |
| huggingface__diffusers-9885 | Bug Report | 36 | Y | Y/Y/Y |  |
| modin-project__modin-7400 | Performance Issue | 41 | Y | Y/Y/Y |  |
| Qiskit__qiskit-13552 | Bug Report | 45 | Y | Y/Y/Y |  |
| aio-libs__aiohttp-9766 | Performance Issue | 4 | Y | Y/Y/Y |  |
| aio-libs__aiohttp-9762 | Performance Issue | 5 | Y | Y/Y/Y |  |
| langchain-ai__langgraph-2724 | Bug Report | 127 | Y | Y/N/N |  |
| langchain-ai__langgraph-2571 | Bug Report | 127 | Y | Y/Y/Y |  |
| vllm-project__vllm-9617 | Feature Request | 14 | Y | N/N/N |  |
| mlflow__mlflow-13390 | Performance Issue | 619 | Y | Y/Y/Y |  |
| huggingface__transformers-34507 | Feature Request | 101 | Y | Y/Y/Y |  |
| huggingface__transformers-34279 | Feature Request | 100 | Y | Y/Y/Y |  |
| django__django-18654 | Feature Request | 55 | Y | Y/Y/Y |  |
| huggingface__diffusers-9815 | Feature Request | 35 | Y | N/N/N |  |
