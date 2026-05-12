# Loc-Bench multi-mode comparison — 2026-05-12 00:54

**Binary:** MRR=2 (n=200 confirmation)
**Modes compared:** hybrid-agent
**Repo size cap:** 5000 MB

## Provenance manifest

| field | value |
|---|---|
| harness_sha | `d15d26ef298e` |
| scorer_schema | `2` |
| eval_bin_sha | `1f4eb8795802` |
| index_bin_sha | `0c694e998f8f` |
| dataset_sha | `8df0833c2c12` |
| agent_iterations | `2` |
| modes | `hybrid-agent` |
| max_mb | `5000` |
| n_attempted | `200` |
| n_indexed | `142` |
| timestamp_utc | `2026-05-12T05:54:08Z` |

## ⚠ Report invariants check

**This report is REFUSED for publication or external comparison** until the violations below are either fixed or explicitly accepted with the appropriate override flag. The data below is preserved for debugging only — do not cite these numbers.

- REFUSE: dominant-cell on hybrid-agent.file: category 'Bug Report' holds 12/28 = 43% of misses (≥30% threshold). Per the verify-instrument-before-fix T1 rule, sample 3-5 misses from this cell and classify INSTRUMENT vs REAL before publishing. Pass `--allow-unexplained-cells` for exploratory runs only.
- REFUSE: dominant-cell on hybrid-agent.file: category 'Performance Issue' holds 9/28 = 32% of misses (≥30% threshold). Per the verify-instrument-before-fix T1 rule, sample 3-5 misses from this cell and classify INSTRUMENT vs REAL before publishing. Pass `--allow-unexplained-cells` for exploratory runs only.
- REFUSE: dominant-cell on hybrid-agent.class: category 'Bug Report' holds 12/28 = 43% of misses (≥30% threshold). Per the verify-instrument-before-fix T1 rule, sample 3-5 misses from this cell and classify INSTRUMENT vs REAL before publishing. Pass `--allow-unexplained-cells` for exploratory runs only.
- REFUSE: dominant-cell on hybrid-agent.class: category 'Performance Issue' holds 9/28 = 32% of misses (≥30% threshold). Per the verify-instrument-before-fix T1 rule, sample 3-5 misses from this cell and classify INSTRUMENT vs REAL before publishing. Pass `--allow-unexplained-cells` for exploratory runs only.
- REFUSE: dominant-cell on hybrid-agent.func: category 'Bug Report' holds 16/37 = 43% of misses (≥30% threshold). Per the verify-instrument-before-fix T1 rule, sample 3-5 misses from this cell and classify INSTRUMENT vs REAL before publishing. Pass `--allow-unexplained-cells` for exploratory runs only.

## Aggregate

Instances attempted: 200 | Indexed: 142

| Mode | Attempted | File hits | Class hits | Func hits | Total $ |
|---|---|---|---|---|---|
| hybrid-agent | 142 | 114/142 (80%) | 114/142 (80%) | 105/142 (74%) | $6.35 |

## Funnel (Fix 5: stratified failure reporting)

| Stage | n | % of attempted |
|---|---:|---:|
| Attempted | 200 | 100.0% |
| Cloned | 142 | 71.0% |
| Indexed | 142 | 71.0% |
| Agent ran | 142 | 71.0% |

### Failure breakdown by stage

| Failure mode | n |
|---|---:|
| clone failed | 58 |

### Failure breakdown by category × stage

| Category | clone failed |
|---|---:|
| Bug Report | 12 |
| Feature Request | 3 |
| Performance Issue | 15 |
| Security Vulnerability | 28 |

> **Methodology caveat**: n_indexed / n_attempted = 142/200 (71%). Aggregate hit-rates above are computed against the indexed subset, NOT the attempted set. Results are NOT directly comparable to baselines where indexed == attempted unless the missing instances are random wrt difficulty. See failure breakdown above to assess.

## Per-instance details

| instance | category | size (MB) | indexed | hybrid-agent F/C/Fn | note |
|---|---|---|---|---|---|
| bridgecrewio__checkov-6909 | Bug Report | 0 | Y | Y/Y/Y |  |
| PrefectHQ__prefect-16117 | Bug Report | 74 | Y | Y/Y/Y |  |
| scipy__scipy-22106 | Bug Report | 103 | Y | Y/Y/Y |  |
| flet-dev__flet-4384 | Bug Report | 30 | Y | Y/Y/N |  |
| Qiskit__qiskit-13552 | Bug Report | 0 | Y | Y/Y/N |  |
| ray-project__ray-48756 | Bug Report | 0 | Y | Y/Y/Y |  |
| spotify__luigi-3324 | Bug Report | 0 | Y | Y/Y/Y |  |
| locustio__locust-2976 | Bug Report | 0 | Y | Y/Y/Y |  |
| dask__dask-11541 | Bug Report | 0 | Y | Y/Y/N |  |
| vllm-project__vllm-10903 | Bug Report | 0 | Y | Y/Y/Y |  |
| python__mypy-18164 | Bug Report | 0 | Y | Y/Y/Y |  |
| flet-dev__flet-4425 | Bug Report | 0 | Y | Y/Y/Y |  |
| huggingface__diffusers-10185 | Bug Report | 0 | Y | Y/Y/Y |  |
| huggingface__trl-2433 | Bug Report | 0 | Y | Y/Y/Y |  |
| tobymao__sqlglot-4415 | Bug Report | 0 | Y | Y/Y/Y |  |
| certbot__certbot-10043 | Bug Report | 0 | Y | Y/Y/N |  |
| huggingface__diffusers-10067 | Bug Report | 0 | Y | Y/Y/Y |  |
| getmoto__moto-8342 | Bug Report | 0 | Y | Y/Y/Y |  |
| django__django-18795 | Bug Report | 0 | Y | N/N/N |  |
| Zulko__moviepy-2253 | Bug Report | 0 | Y | Y/Y/Y |  |
| keras-team__keras-20443 | Bug Report | 0 | Y | Y/Y/Y |  |
| ray-project__ray-49221 | Bug Report | 318 | Y | N/N/N |  |
| prowler-cloud__prowler-5856 | Bug Report | 0 | Y | Y/Y/Y |  |
| keras-team__keras-20550 | Bug Report | 0 | Y | N/N/N |  |
| nltk__nltk-3335 | Bug Report | 0 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60543 | Bug Report | 0 | Y | N/N/N |  |
| DS4SD__docling-330 | Bug Report | 0 | Y | N/N/N |  |
| huggingface__trl-2450 | Bug Report | 0 | Y | Y/Y/Y |  |
| sympy__sympy-27288 | Bug Report | 0 | Y | N/N/N |  |
| DS4SD__docling-528 | Bug Report | 0 | Y | Y/Y/Y |  |
| prowler-cloud__prowler-5653 | Bug Report | 0 | Y | N/N/N |  |
| kornia__kornia-3084 | Bug Report | 0 | Y | Y/Y/Y |  |
| bridgecrewio__checkov-6895 | Bug Report | 95 | Y | N/N/N |  |
| django__django-18906 | Bug Report | 0 | Y | N/N/N |  |
| ray-project__ray-49236 | Bug Report | 0 | Y | Y/Y/Y |  |
| UKPLab__sentence-transformers-3073 | Bug Report | 0 | Y | Y/Y/Y |  |
| jax-ml__jax-25511 | Bug Report | 0 | Y | Y/Y/Y |  |
| ray-project__ray-48891 | Bug Report | 0 | Y | Y/Y/Y |  |
| yt-dlp__yt-dlp-11644 | Bug Report | 0 | Y | Y/Y/Y |  |
| langchain-ai__langgraph-2724 | Bug Report | 0 | Y | Y/Y/Y |  |
| rq__rq-2138 | Bug Report | 0 | Y | N/N/N |  |
| vllm-project__vllm-10347 | Bug Report | 0 | Y | Y/Y/Y |  |
| vllm-project__vllm-11073 | Bug Report | 0 | Y | N/N/N |  |
| getmoto__moto-8316 | Bug Report | 0 | Y | Y/Y/Y |  |
| ray-project__ray-49071 | Bug Report | 0 | Y | N/N/N |  |
| django__django-18785 | Bug Report | 0 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60310 | Bug Report | 0 | Y | Y/Y/Y |  |
| yt-dlp__yt-dlp-11750 | Bug Report | 0 | Y | Y/Y/Y |  |
| langchain-ai__langgraph-2571 | Bug Report | 0 | Y | Y/Y/Y |  |
| vllm-project__vllm-10536 | Bug Report | 0 | Y | Y/Y/Y |  |
| pydantic__pydantic-8706 | Feature Request | 0 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-14012 | Feature Request | 0 | Y | Y/Y/Y |  |
| vllm-project__vllm-11138 | Feature Request | 0 | Y | Y/Y/Y |  |
| pandas-dev__pandas-59900 | Feature Request | 0 | Y | Y/Y/N |  |
| python__mypy-18163 | Feature Request | 0 | Y | Y/Y/Y |  |
| pandas-dev__pandas-22762 | Feature Request | 0 | Y | Y/Y/Y |  |
| dask__dask-10750 | Feature Request | 0 | Y | Y/Y/Y |  |
| vllm-project__vllm-9617 | Feature Request | 0 | Y | Y/Y/Y |  |
| vllm-project__vllm-7783 | Feature Request | 0 | Y | Y/Y/Y |  |
| django__django-18752 | Feature Request | 0 | Y | Y/Y/Y |  |
| jax-ml__jax-19710 | Feature Request | 0 | Y | N/N/N |  |
| huggingface__transformers-35453 | Feature Request | 0 | Y | Y/Y/Y |  |
| pandas-dev__pandas-19074 | Feature Request | 0 | Y | N/N/N |  |
| django__django-18435 | Feature Request | 0 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-6116 | Feature Request | 0 | Y | Y/Y/Y |  |
| dask__dask-11434 | Feature Request | 0 | Y | Y/Y/Y |  |
| jax-ml__jax-25787 | Feature Request | 0 | Y | Y/Y/Y |  |
| pydantic__pydantic-10789 | Feature Request | 0 | Y | Y/Y/Y |  |
| Lightning-AI__pytorch-lightning-20403 | Feature Request | 0 | Y | Y/Y/Y |  |
| matplotlib__matplotlib-29265 | Feature Request | 0 | Y | Y/Y/Y |  |
| numpy__numpy-17394 | Feature Request | 0 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60277 | Feature Request | 0 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-17443 | Feature Request | 0 | Y | Y/Y/Y |  |
| pandas-dev__pandas-60247 | Feature Request | 0 | Y | Y/Y/Y |  |
| bridgecrewio__checkov-6826 | Feature Request | 95 | Y | Y/Y/Y |  |
| django__django-18654 | Feature Request | 55 | Y | N/N/N |  |
| jax-ml__jax-22049 | Feature Request | 0 | Y | Y/Y/N |  |
| huggingface__transformers-34279 | Feature Request | 100 | Y | N/N/N |  |
| PrefectHQ__prefect-14693 | Feature Request | 74 | Y | N/N/N |  |
| scikit-learn__scikit-learn-10280 | Feature Request | 21 | Y | N/N/N |  |
| jax-ml__jax-25114 | Feature Request | 48 | Y | Y/Y/Y |  |
| pandas-dev__pandas-29944 | Feature Request | 58 | Y | Y/Y/Y |  |
| ray-project__ray-48957 | Feature Request | 302 | Y | Y/Y/Y |  |
| huggingface__transformers-22498 | Feature Request | 68 | Y | Y/Y/Y |  |
| scikit-learn__scikit-learn-30128 | Feature Request | 37 | Y | Y/Y/Y |  |
| huggingface__diffusers-9815 | Feature Request | 35 | Y | N/N/N |  |
| PrefectHQ__prefect-13259 | Feature Request | 74 | Y | Y/Y/Y |  |
| Qiskit__qiskit-13141 | Feature Request | 44 | Y | Y/Y/N |  |
| vllm-project__vllm-9390 | Feature Request | 14 | Y | Y/Y/Y |  |
| numpy__numpy-8206 | Feature Request | 23 | Y | Y/Y/Y |  |
| huggingface__transformers-34507 | Feature Request | 101 | Y | Y/Y/Y |  |
| roboflow__supervision-1739 | Feature Request | 100 | Y | Y/Y/Y |  |
| tobymao__sqlglot-4434 | Feature Request | 100 | Y | Y/Y/N |  |
| scikit-learn__scikit-learn-16948 | Feature Request | 26 | Y | Y/Y/Y |  |
| sqlfluff__sqlfluff-6399 | Feature Request | 19 | Y | Y/Y/Y |  |
| django__django-18820 | Feature Request | 55 | Y | Y/Y/Y |  |
| flet-dev__flet-4388 | Feature Request | 30 | Y | Y/Y/Y |  |
| huggingface__diffusers-10262 | Feature Request | 38 | Y | Y/Y/Y |  |
| Qiskit__qiskit-12214 | Feature Request | 36 | Y | Y/Y/Y |  |
| jax-ml__jax-19909 | Feature Request | 42 | Y | Y/Y/Y |  |
| aio-libs__aiohttp-9762 | Performance Issue | 5 | Y | Y/Y/Y |  |
| Standard-Labs__real-intent-27 | Performance Issue | 0 | Y | Y/Y/Y |  |
| zulip__zulip-14091 | Performance Issue | 69 | Y | Y/Y/Y |  |
| modin-project__modin-6951 | Performance Issue | 39 | Y | N/N/N |  |
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
| ray-project__ray-26818 | Performance Issue | 126 | Y | Y/Y/N |  |
| pulp__pulp_rpm-3224 | Performance Issue | 2 | Y | Y/Y/Y |  |
| aio-libs__aiohttp-9766 | Performance Issue | 4 | Y | Y/Y/Y |  |
| django__django-17904 | Performance Issue | 54 | Y | Y/Y/Y |  |
| PlasmaPy__PlasmaPy-2542 | Performance Issue | 30 | Y | Y/Y/Y |  |
| modin-project__modin-6980 | Performance Issue | 39 | Y | Y/Y/Y |  |
| rapidsai__dask-cuda-98 | Performance Issue | 0 | Y | Y/Y/Y |  |
| Open-MSS__MSS-1967 | Performance Issue | 12 | Y | N/N/N |  |
| sgkit-dev__sgkit-447 | Performance Issue | 40 | Y | Y/Y/Y |  |
| webcompat__webcompat.com-2731 | Performance Issue | 11 | Y | Y/Y/Y |  |
| modin-project__modin-7400 | Performance Issue | 41 | Y | Y/Y/Y |  |
| twisted__klein-773 | Performance Issue | 1 | Y | Y/Y/Y |  |
| TagStudioDev__TagStudio-735 | Performance Issue | 12 | Y | Y/Y/Y |  |
| AzureAD__microsoft-authentication-library-for-python-454 | Performance Issue | 1 | Y | N/N/N |  |
| scikit-learn__scikit-learn-25186 | Performance Issue | 27 | Y | Y/Y/Y |  |
| UXARRAY__uxarray-1151 | Performance Issue | 116 | Y | Y/Y/Y |  |
| django__django-18508 | Performance Issue | 54 | Y | Y/Y/Y |  |
| celery__django-celery-beat-835 | Performance Issue | 1 | Y | Y/Y/Y |  |
| django__django-18105 | Performance Issue | 54 | Y | N/N/N |  |
| zulip__zulip-31168 | Performance Issue | 126 | Y | N/N/N |  |
| modin-project__modin-4391 | Performance Issue | 72 | Y | N/N/N |  |
| alexa-pi__AlexaPi-188 | Performance Issue | 1 | Y | Y/Y/Y |  |
| streamlit__streamlit-9472 | Performance Issue | 175 | Y | N/N/N |  |
| UXARRAY__uxarray-1117 | Performance Issue | 116 | Y | Y/Y/Y |  |
| matrix-org__synapse-8744 | Performance Issue | 35 | Y | N/N/N |  |
| scipy__scipy-5647 | Performance Issue | 0 | N | - | clone failed |
| kedro-org__kedro-4367 | Performance Issue | 0 | N | - | clone failed |
| numba__numba-9757 | Performance Issue | 0 | N | - | clone failed |
| home-assistant__core-136739 | Performance Issue | 0 | N | - | clone failed |
| pandas-dev__pandas-60461 | Performance Issue | 0 | N | - | clone failed |
| mlflow__mlflow-13390 | Performance Issue | 0 | N | - | clone failed |
| ivadomed__ivadomed-1081 | Performance Issue | 0 | N | - | clone failed |
| JoinMarket-Org__joinmarket-clientserver-1180 | Performance Issue | 0 | N | - | clone failed |
| django__django-17984 | Performance Issue | 0 | N | - | clone failed |
| spotify__luigi-3308 | Security Vulnerability | 7 | Y | Y/Y/Y |  |
| duncanscanga__VDRS-Solutions-73 | Security Vulnerability | 0 | N | - | clone failed |
| internetarchive__openlibrary-3196 | Security Vulnerability | 0 | N | - | clone failed |
| Innopoints__backend-124 | Security Vulnerability | 0 | N | - | clone failed |
| Chainlit__chainlit-1441 | Security Vulnerability | 0 | N | - | clone failed |
| mathesar-foundation__mathesar-3117 | Security Vulnerability | 0 | N | - | clone failed |
| jazzband__django-two-factor-auth-390 | Security Vulnerability | 0 | N | - | clone failed |
| Chainlit__chainlit-1575 | Security Vulnerability | 0 | N | - | clone failed |
| streamlit__streamlit-9754 | Security Vulnerability | 0 | N | - | clone failed |
| django__django-13134 | Security Vulnerability | 0 | N | - | clone failed |
| sancus-tee__sancus-compiler-36 | Security Vulnerability | 0 | N | - | clone failed |
| jobatabs__textec-53 | Security Vulnerability | 0 | N | - | clone failed |
| plone__plone.restapi-859 | Security Vulnerability | 0 | N | - | clone failed |
| sopel-irc__sopel-2285 | Security Vulnerability | 0 | N | - | clone failed |
| okta__okta-jwt-verifier-python-59 | Security Vulnerability | 0 | N | - | clone failed |
| gitpython-developers__GitPython-1636 | Security Vulnerability | 0 | N | - | clone failed |
| mesonbuild__meson-11366 | Security Vulnerability | 0 | N | - | clone failed |
| latchset__jwcrypto-195 | Security Vulnerability | 0 | N | - | clone failed |
| pypa__pip-13085 | Security Vulnerability | 0 | N | - | clone failed |
| matchms__matchms-backup-187 | Security Vulnerability | 0 | N | - | clone failed |
| django__django-5605 | Security Vulnerability | 0 | N | - | clone failed |
| openwisp__openwisp-users-286 | Security Vulnerability | 0 | N | - | clone failed |
| mlflow__mlflow-10923 | Security Vulnerability | 0 | N | - | clone failed |
| home-assistant__core-15182 | Security Vulnerability | 0 | N | - | clone failed |
| fortra__impacket-1636 | Security Vulnerability | 0 | N | - | clone failed |
| JackPlowman__repo_standards_validator-137 | Security Vulnerability | 0 | N | - | clone failed |
| airbnb__knowledge-repo-558 | Security Vulnerability | 0 | N | - | clone failed |
| rucio__rucio-4930 | Security Vulnerability | 0 | N | - | clone failed |
| micropython__micropython-lib-947 | Security Vulnerability | 0 | N | - | clone failed |
| ranaroussi__yfinance-2122 | Bug Report | 0 | N | - | clone failed |
| huggingface__diffusers-9885 | Bug Report | 0 | N | - | clone failed |
| django__django-19009 | Performance Issue | 0 | N | - | clone failed |
| python__mypy-18160 | Bug Report | 0 | N | - | clone failed |
| django__django-17874 | Performance Issue | 0 | N | - | clone failed |
| tobymao__sqlglot-4524 | Bug Report | 0 | N | - | clone failed |
| scikit-learn__scikit-learn-8478 | Feature Request | 0 | N | - | clone failed |
| wandb__wandb-9011 | Bug Report | 0 | N | - | clone failed |
| DS4SD__docling-442 | Bug Report | 0 | N | - | clone failed |
| phidatahq__phidata-1563 | Bug Report | 0 | N | - | clone failed |
| sympy__sympy-27325 | Feature Request | 0 | N | - | clone failed |
| roboflow__supervision-1698 | Bug Report | 0 | N | - | clone failed |
| dask__dask-11479 | Performance Issue | 0 | N | - | clone failed |
| django__django-18616 | Feature Request | 0 | N | - | clone failed |
| Ouranosinc__xclim-477 | Performance Issue | 0 | N | - | clone failed |
| dask__dask-11609 | Performance Issue | 0 | N | - | clone failed |
| BerriAI__litellm-6915 | Bug Report | 0 | N | - | clone failed |
| SYSTRAN__faster-whisper-1141 | Bug Report | 0 | N | - | clone failed |
| hyeneung__tech-blog-hub-site-49 | Performance Issue | 0 | N | - | clone failed |
| keras-team__keras-20626 | Bug Report | 0 | N | - | clone failed |
| dask__dask-11491 | Bug Report | 0 | N | - | clone failed |

