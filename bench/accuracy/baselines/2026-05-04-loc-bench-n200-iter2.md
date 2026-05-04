# Loc-Bench n=200 iter=2 baseline — 2026-05-04

> Phase A2 of the broader-production maturation plan
> ([[plans/2026-05-04-code-graph-phase-a-execution]]). Confirms the
> +11pp file lift seen at n=18 (apples-to-apples filter on a throttled
> n=50 iter=2 run, 2026-05-03) and surfaces a much larger class-level
> lift that **closes the Phase A diagnosis question by data**.

## Apples-to-apples comparison vs defended single-shot baseline

The defended n=200 single-shot baseline cited in CLAUDE.md prior to this
run was:

| Metric | Single-shot (n=200) | iter=2 (n=200) | Δ |
|---|---:|---:|---:|
| File Acc@10 | 82.5% | **86.0%** | **+3.5pp** |
| Class Acc@10 | 46.5% | **84.5%** | **+38.0pp** |
| Func Acc@10 | 61.0% | **73.5%** | **+12.5pp** |

iter=2 produces the expected monotonic ordering `file ≥ class ≥ func`.
The non-monotone class-gap (`func > class`) that motivated Phase A's
diagnosis has disappeared at iter=2.

## Phase A verdict (closed by A2 data, A3-A5 moot)

**The class-gap was an iteration-count artifact**, not an instrument
bug AND not a real system gap:

- Single-shot (iter=1) class-level prediction was unstable — the agent
  often picked a semantically-near-but-not-canonical class
- MRR aggregation across iter=2 stabilizes on the right class (+38pp lift)
- Func lift is only +12.5pp because iter=2 doesn't fix the cases where
  the gold-truth function lives in a multi-function class (the agent
  picks the right class but the wrong specific method)

This was NOT one of the three pre-registered branches (INSTRUMENT /
REAL / mixed). The iter-count-artifact branch is what the data actually
shows.

**A3-A5 cancelled** — the Phase A class-gap diagnosis is unnecessary
when iter=2 closes the gap entirely. **Phase D class-level fix
cancelled** — there is no class-level fix to do at iter=2.

Phase A is closed. Next: Phase B (calibration head) or Phase C (memory).

## Cost & wall

- **Cost**: $9.60 ($0.048/query average; 3 instances ran $0.00 due to
  errors/retries on the agent path)
- **Wall**: ~5 hours background, 4 workers
- **Indexed**: 200/200 (no clone failures — yesterday's iter=1 throttle
  pattern of 32/50 did NOT recur today)

## Aggregate (raw eval output)

Instances attempted: 200 | Indexed: 200

| Mode | Attempted | File hits | Class hits | Func hits | Total $ |
|---|---|---|---|---|---|
| hybrid-agent | 200 | 171/200 (86%) | 169/200 (84%) | 147/200 (74%) | $9.60 |

## Per-instance details

| instance | category | size (MB) | indexed | hybrid-agent F/C/Fn | note |
|---|---|---|---|---|---|
| bridgecrewio__checkov-6909 | Bug Report | 95 | Y | N/N/N |  |
| PrefectHQ__prefect-16117 | Bug Report | 74 | Y | Y/Y/Y |  |
| scipy__scipy-22106 | Bug Report | 103 | Y | Y/Y/Y |  |
| flet-dev__flet-4384 | Bug Report | 30 | Y | Y/Y/N |  |
| Qiskit__qiskit-13552 | Bug Report | 45 | Y | Y/Y/Y |  |
| ray-project__ray-48756 | Bug Report | 301 | Y | Y/Y/Y |  |
| spotify__luigi-3324 | Bug Report | 7 | Y | Y/Y/Y |  |
| locustio__locust-2976 | Bug Report | 4 | Y | Y/Y/Y |  |
| dask__dask-11541 | Bug Report | 28 | Y | Y/Y/Y |  |
| vllm-project__vllm-10903 | Bug Report | 17 | Y | N/N/N |  |
| python__mypy-18164 | Bug Report | 19 | Y | Y/Y/Y |  |
| flet-dev__flet-4425 | Bug Report | 30 | Y | Y/Y/Y |  |
| huggingface__diffusers-10185 | Bug Report | 37 | Y | Y/Y/Y |  |
| huggingface__trl-2433 | Bug Report | 3 | Y | Y/Y/Y |  |
| tobymao__sqlglot-4415 | Bug Report | 100 | Y | N/N/N |  |
| certbot__certbot-10043 | Bug Report | 6 | Y | Y/Y/Y |  |
| huggingface__diffusers-10067 | Bug Report | 36 | Y | N/N/N |  |
| getmoto__moto-8342 | Bug Report | 80 | Y | Y/Y/Y |  |
| django__django-18795 | Bug Report | 55 | Y | Y/Y/Y |  |
| Zulko__moviepy-2253 | Bug Report | 178 | Y | Y/Y/Y |  |
| keras-team__keras-20443 | Bug Report | 9 | Y | N/N/N |  |
| ray-project__ray-49221 | Bug Report | 318 | Y | N/N/N |  |
| prowler-cloud__prowler-5856 | Bug Report | 51 | Y | N/N/N |  |
| keras-team__keras-20550 | Bug Report | 9 | Y | Y/Y/Y |  |
| nltk__nltk-3335 | Bug Report | 11 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60543 | Bug Report | 65 | Y | Y/Y/Y |  |
| DS4SD__docling-330 | Bug Report | 53 | Y | Y/Y/Y |  |
| huggingface__trl-2450 | Bug Report | 3 | Y | Y/Y/Y |  |
| sympy__sympy-27288 | Bug Report | 38 | Y | Y/Y/Y |  |
| DS4SD__docling-528 | Bug Report | 57 | Y | Y/Y/N |  |
| prowler-cloud__prowler-5653 | Bug Report | 51 | Y | N/N/N |  |
| kornia__kornia-3084 | Bug Report | 13 | Y | Y/Y/Y |  |
| bridgecrewio__checkov-6895 | Bug Report | 95 | Y | Y/Y/N |  |
| django__django-18906 | Bug Report | 55 | Y | Y/Y/Y |  |
| ray-project__ray-49236 | Bug Report | 318 | Y | Y/Y/Y |  |
| UKPLab__sentence-transformers-3073 | Bug Report | 36 | Y | Y/Y/Y |  |
| jax-ml__jax-25511 | Bug Report | 49 | Y | Y/Y/Y |  |
| ray-project__ray-48891 | Bug Report | 302 | Y | N/N/N |  |
| yt-dlp__yt-dlp-11644 | Bug Report | 14 | Y | Y/Y/Y |  |
| langchain-ai__langgraph-2724 | Bug Report | 127 | Y | Y/Y/Y |  |
| rq__rq-2138 | Bug Report | 3 | Y | Y/Y/N |  |
| vllm-project__vllm-10347 | Bug Report | 16 | Y | N/N/N |  |
| vllm-project__vllm-11073 | Bug Report | 19 | Y | Y/Y/Y |  |
| getmoto__moto-8316 | Bug Report | 80 | Y | Y/Y/Y |  |
| ray-project__ray-49071 | Bug Report | 302 | Y | Y/Y/Y |  |
| django__django-18785 | Bug Report | 55 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60310 | Bug Report | 65 | Y | Y/Y/Y |  |
| yt-dlp__yt-dlp-11750 | Bug Report | 14 | Y | Y/Y/N |  |
| langchain-ai__langgraph-2571 | Bug Report | 127 | Y | Y/Y/Y |  |
| vllm-project__vllm-10536 | Bug Report | 16 | Y | Y/Y/Y |  |
| pydantic__pydantic-8706 | Feature Request | 9 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-14012 | Feature Request | 22 | Y | Y/Y/Y |  |
| vllm-project__vllm-11138 | Feature Request | 19 | Y | Y/Y/Y |  |
| pandas-dev__pandas-59900 | Feature Request | 65 | Y | Y/Y/Y |  |
| python__mypy-18163 | Feature Request | 19 | Y | Y/Y/Y |  |
| pandas-dev__pandas-22762 | Feature Request | 43 | Y | Y/Y/N |  |
| dask__dask-10750 | Feature Request | 24 | Y | Y/Y/Y |  |
| vllm-project__vllm-9617 | Feature Request | 14 | Y | N/N/N |  |
| vllm-project__vllm-7783 | Feature Request | 11 | Y | Y/Y/Y |  |
| django__django-18752 | Feature Request | 55 | Y | Y/N/N |  |
| jax-ml__jax-19710 | Feature Request | 42 | Y | Y/Y/Y |  |
| huggingface__transformers-35453 | Feature Request | 105 | Y | Y/Y/Y |  |
| pandas-dev__pandas-19074 | Feature Request | 38 | Y | Y/Y/Y |  |
| django__django-18435 | Feature Request | 54 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-6116 | Feature Request | 19 | Y | Y/Y/Y |  |
| dask__dask-11434 | Feature Request | 27 | Y | Y/Y/Y |  |
| jax-ml__jax-25787 | Feature Request | 49 | Y | Y/Y/Y |  |
| pydantic__pydantic-10789 | Feature Request | 10 | Y | Y/Y/Y |  |
| Lightning-AI__pytorch-lightning-20403 | Feature Request | 38 | Y | Y/Y/Y |  |
| matplotlib__matplotlib-29265 | Feature Request | 100 | Y | Y/N/N |  |
| numpy__numpy-17394 | Feature Request | 32 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60277 | Feature Request | 65 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-17443 | Feature Request | 27 | Y | Y/Y/N |  |
| pandas-dev__pandas-60247 | Feature Request | 65 | Y | Y/Y/Y |  |
| bridgecrewio__checkov-6826 | Feature Request | 95 | Y | Y/Y/Y |  |
| django__django-18654 | Feature Request | 55 | Y | Y/Y/Y |  |
| jax-ml__jax-22049 | Feature Request | 45 | Y | Y/Y/Y |  |
| huggingface__transformers-34279 | Feature Request | 100 | Y | Y/Y/Y |  |
| PrefectHQ__prefect-14693 | Feature Request | 74 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-10280 | Feature Request | 21 | Y | Y/Y/Y |  |
| jax-ml__jax-25114 | Feature Request | 48 | Y | Y/Y/Y |  |
| pandas-dev__pandas-29944 | Feature Request | 58 | Y | Y/Y/Y |  |
| ray-project__ray-48957 | Feature Request | 302 | Y | Y/Y/Y |  |
| huggingface__transformers-22498 | Feature Request | 68 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-30128 | Feature Request | 29 | Y | Y/Y/Y |  |
| huggingface__diffusers-9815 | Feature Request | 35 | Y | N/N/N |  |
| PrefectHQ__prefect-13259 | Feature Request | 74 | Y | Y/Y/Y |  |
| Qiskit__qiskit-13141 | Feature Request | 44 | Y | Y/Y/N |  |
| vllm-project__vllm-9390 | Feature Request | 14 | Y | Y/Y/Y |  |
| numpy__numpy-8206 | Feature Request | 23 | Y | Y/Y/Y |  |
| huggingface__transformers-34507 | Feature Request | 101 | Y | N/N/N |  |
| roboflow__supervision-1739 | Feature Request | 100 | Y | Y/Y/Y |  |
| tobymao__sqlglot-4434 | Feature Request | 100 | Y | Y/Y/N |  |
| scikit-learn__scikit-learn-16948 | Feature Request | 26 | Y | Y/Y/Y |  |
| sqlfluff__sqlfluff-6399 | Feature Request | 19 | Y | Y/Y/Y |  |
| django__django-18820 | Feature Request | 12 | Y | Y/Y/N |  |
| flet-dev__flet-4388 | Feature Request | 30 | Y | Y/Y/Y |  |
| huggingface__diffusers-10262 | Feature Request | 38 | Y | Y/Y/Y |  |
| Qiskit__qiskit-12214 | Feature Request | 36 | Y | Y/Y/Y |  |
| jax-ml__jax-19909 | Feature Request | 42 | Y | Y/Y/Y |  |
| aio-libs__aiohttp-9762 | Performance Issue | 5 | Y | Y/Y/Y |  |
| Standard-Labs__real-intent-27 | Performance Issue | 0 | Y | Y/Y/Y |  |
| zulip__zulip-14091 | Performance Issue | 69 | Y | Y/Y/Y |  |
| modin-project__modin-6951 | Performance Issue | 39 | Y | Y/Y/N |  |
| Deltares__imod-python-1159 | Performance Issue | 5 | Y | Y/Y/Y |  |
| huggingface__optimum-benchmark-266 | Performance Issue | 1 | Y | N/N/N |  |
| ckan__ckan-8226 | Performance Issue | 48 | Y | Y/Y/Y |  |
| vllm-project__vllm-7874 | Performance Issue | 12 | Y | Y/Y/Y |  |
| SYSTRAN__faster-whisper-1198 | Performance Issue | 42 | Y | Y/Y/Y |  |
| UXARRAY__uxarray-1144 | Performance Issue | 116 | Y | Y/Y/Y |  |
| py-pdf__pypdf-2656 | Performance Issue | 24 | Y | Y/Y/Y |  |
| ultralytics__ultralytics-17728 | Performance Issue | 7 | Y | Y/Y/Y |  |
| freedomofpress__securedrop-client-944 | Performance Issue | 12 | Y | Y/Y/Y |  |
| Bears-R-Us__arkouda-1969 | Performance Issue | 11 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-13290 | Performance Issue | 22 | Y | Y/Y/Y |  |
| NCSU-High-Powered-Rocketry-Club__AirbrakesV2-151 | Performance Issue | 148 | Y | Y/Y/Y |  |
| ray-project__ray-26818 | Performance Issue | 43 | Y | N/N/N |  |
| pulp__pulp_rpm-3224 | Performance Issue | 2 | Y | Y/Y/Y |  |
| aio-libs__aiohttp-9766 | Performance Issue | 4 | Y | Y/Y/Y |  |
| django__django-17904 | Performance Issue | 12 | Y | Y/Y/Y |  |
| PlasmaPy__PlasmaPy-2542 | Performance Issue | 30 | Y | Y/Y/Y |  |
| modin-project__modin-6980 | Performance Issue | 39 | Y | Y/Y/Y |  |
| rapidsai__dask-cuda-98 | Performance Issue | 0 | Y | Y/Y/Y |  |
| Open-MSS__MSS-1967 | Performance Issue | 12 | Y | N/N/N |  |
| sgkit-dev__sgkit-447 | Performance Issue | 40 | Y | Y/Y/Y |  |
| webcompat__webcompat.com-2731 | Performance Issue | 11 | Y | N/N/N |  |
| modin-project__modin-7400 | Performance Issue | 41 | Y | Y/Y/Y |  |
| twisted__klein-773 | Performance Issue | 1 | Y | Y/Y/Y |  |
| TagStudioDev__TagStudio-735 | Performance Issue | 12 | Y | Y/Y/Y |  |
| AzureAD__microsoft-authentication-library-for-python-454 | Performance Issue | 1 | Y | N/N/N |  |
| scikit-learn__scikit-learn-25186 | Performance Issue | 27 | Y | Y/Y/Y |  |
| UXARRAY__uxarray-1151 | Performance Issue | 116 | Y | Y/Y/Y |  |
| django__django-18508 | Performance Issue | 54 | Y | Y/Y/Y |  |
| celery__django-celery-beat-835 | Performance Issue | 1 | Y | Y/Y/Y |  |
| django__django-18105 | Performance Issue | 54 | Y | Y/Y/Y |  |
| zulip__zulip-31168 | Performance Issue | 39 | Y | Y/Y/N |  |
| modin-project__modin-4391 | Performance Issue | 72 | Y | N/N/N |  |
| alexa-pi__AlexaPi-188 | Performance Issue | 1 | Y | Y/Y/Y |  |
| streamlit__streamlit-9472 | Performance Issue | 175 | Y | Y/Y/Y |  |
| UXARRAY__uxarray-1117 | Performance Issue | 116 | Y | Y/Y/Y |  |
| matrix-org__synapse-8744 | Performance Issue | 35 | Y | Y/Y/N |  |
| scipy__scipy-5647 | Performance Issue | 50 | Y | N/N/N |  |
| kedro-org__kedro-4367 | Performance Issue | 148 | Y | Y/Y/Y |  |
| numba__numba-9757 | Performance Issue | 16 | Y | Y/Y/Y |  |
| home-assistant__core-136739 | Performance Issue | 23 | Y | Y/Y/N |  |
| pandas-dev__pandas-60461 | Performance Issue | 65 | Y | N/N/N |  |
| mlflow__mlflow-13390 | Performance Issue | 619 | Y | Y/Y/Y |  |
| ivadomed__ivadomed-1081 | Performance Issue | 2 | Y | Y/Y/Y |  |
| JoinMarket-Org__joinmarket-clientserver-1180 | Performance Issue | 8 | Y | Y/Y/Y |  |
| django__django-17984 | Performance Issue | 12 | Y | Y/Y/N |  |
| spotify__luigi-3308 | Security Vulnerability | 7 | Y | Y/Y/Y |  |
| duncanscanga__VDRS-Solutions-73 | Security Vulnerability | 0 | Y | Y/Y/Y |  |
| internetarchive__openlibrary-3196 | Security Vulnerability | 13 | Y | Y/Y/N |  |
| Innopoints__backend-124 | Security Vulnerability | 0 | Y | Y/Y/N |  |
| Chainlit__chainlit-1441 | Security Vulnerability | 7 | Y | Y/Y/Y |  |
| mathesar-foundation__mathesar-3117 | Security Vulnerability | 26 | Y | Y/Y/Y |  |
| jazzband__django-two-factor-auth-390 | Security Vulnerability | 1 | Y | Y/Y/N |  |
| Chainlit__chainlit-1575 | Security Vulnerability | 7 | Y | Y/Y/N |  |
| streamlit__streamlit-9754 | Security Vulnerability | 184 | Y | N/N/N |  |
| django__django-13134 | Security Vulnerability | 48 | Y | Y/Y/Y |  |
| sancus-tee__sancus-compiler-36 | Security Vulnerability | 0 | Y | N/N/N |  |
| jobatabs__textec-53 | Security Vulnerability | 0 | Y | Y/Y/Y |  |
| plone__plone.restapi-859 | Security Vulnerability | 2 | Y | Y/Y/Y |  |
| sopel-irc__sopel-2285 | Security Vulnerability | 3 | Y | Y/Y/Y |  |
| okta__okta-jwt-verifier-python-59 | Security Vulnerability | 0 | Y | Y/Y/N |  |
| gitpython-developers__GitPython-1636 | Security Vulnerability | 2 | Y | Y/Y/Y |  |
| mesonbuild__meson-11366 | Security Vulnerability | 15 | Y | Y/Y/Y |  |
| latchset__jwcrypto-195 | Security Vulnerability | 0 | Y | N/N/N |  |
| pypa__pip-13085 | Security Vulnerability | 23 | Y | Y/Y/Y |  |
| matchms__matchms-backup-187 | Security Vulnerability | 1 | Y | Y/Y/Y |  |
| django__django-5605 | Security Vulnerability | 40 | Y | Y/Y/Y |  |
| openwisp__openwisp-users-286 | Security Vulnerability | 1 | Y | Y/Y/Y |  |
| mlflow__mlflow-10923 | Security Vulnerability | 217 | Y | N/N/N |  |
| home-assistant__core-15182 | Security Vulnerability | 17 | Y | Y/Y/Y |  |
| fortra__impacket-1636 | Security Vulnerability | 2 | Y | Y/Y/N |  |
| JackPlowman__repo_standards_validator-137 | Security Vulnerability | 0 | Y | Y/Y/Y |  |
| airbnb__knowledge-repo-558 | Security Vulnerability | 3 | Y | Y/Y/Y |  |
| rucio__rucio-4930 | Security Vulnerability | 10 | Y | Y/Y/Y |  |
| micropython__micropython-lib-947 | Security Vulnerability | 3 | Y | Y/Y/Y |  |
| ranaroussi__yfinance-2122 | Bug Report | 5 | Y | Y/Y/Y |  |
| huggingface__diffusers-9885 | Bug Report | 36 | Y | Y/Y/Y |  |
| django__django-19009 | Performance Issue | 55 | Y | Y/Y/Y |  |
| python__mypy-18160 | Bug Report | 19 | Y | N/N/N |  |
| django__django-17874 | Performance Issue | 54 | Y | Y/Y/Y |  |
| tobymao__sqlglot-4524 | Bug Report | 101 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-8478 | Feature Request | 21 | Y | N/N/N |  |
| wandb__wandb-9011 | Bug Report | 119 | Y | Y/Y/Y |  |
| DS4SD__docling-442 | Bug Report | 55 | Y | Y/Y/Y |  |
| phidatahq__phidata-1563 | Bug Report | 6 | Y | Y/Y/Y |  |
| sympy__sympy-27325 | Feature Request | 38 | Y | Y/Y/Y |  |
| roboflow__supervision-1698 | Bug Report | 100 | Y | Y/Y/Y |  |
| dask__dask-11479 | Performance Issue | 27 | Y | Y/Y/Y |  |
| django__django-18616 | Feature Request | 55 | Y | Y/Y/Y |  |
| Ouranosinc__xclim-477 | Performance Issue | 71 | Y | Y/Y/Y |  |
| dask__dask-11609 | Performance Issue | 28 | Y | N/N/N |  |
| BerriAI__litellm-6915 | Bug Report | 154 | Y | Y/Y/Y |  |
| SYSTRAN__faster-whisper-1141 | Bug Report | 42 | Y | Y/Y/N |  |
| hyeneung__tech-blog-hub-site-49 | Performance Issue | 4 | Y | N/N/N |  |
| keras-team__keras-20626 | Bug Report | 9 | Y | Y/Y/Y |  |
| dask__dask-11491 | Bug Report | 27 | Y | Y/Y/Y |  |

