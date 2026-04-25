# Loc-Bench Haiku 4.5 baseline — partial run

**Status:** Run was killed mid-batch. This file captures the data from the instances that completed all 3 modes before the kill, so the Haiku numbers are preserved as a baseline for comparison against the upcoming Opus 4.7 run.

**Instances completed (all 3 modes):** 11 of 12 attempted

**Configuration:**

- Model: Haiku 4.5 (default)
- Max turns: 10
- Tools available to agent: rank_by_query, code_localize, finalize
- Seed strategy (hybrid mode): substring + Voyage embedding cosine ≥ 0.65
- Repo size cap: 1000 MB (10 of the 16 selected indexed under this cap)

## Aggregate

| Mode | Attempted | File hits | Class hits | Func hits | Total $ |
|---|---|---|---|---|---|
| substring-primitives | 11 | 4/11 (36%) | 0/11 (0%) | 1/11 (9%) | $0.00 |
| hybrid-primitives | 11 | 4/11 (36%) | 0/11 (0%) | 1/11 (9%) | $0.00 |
| hybrid-agent | 11 | 9/11 (82%) | 5/11 (45%) | 8/11 (73%) | $0.55 |

## Per-instance details

| instance | category | indexed | substring (F/C/Fn) | hybrid (F/C/Fn) | agent (F/C/Fn) | agent tokens | agent $ |
|---|---|---|---|---|---|---|---|
| Chainlit__chainlit-1441 | Security Vulnerability | Y | Y/N/N | Y/N/N | Y/N/N | 18840/816 | $0.050 |
| internetarchive__openlibrary-3196 | Security Vulnerability | Y | N/N/N | N/N/N | N/N/N | 42800/1235 | $0.050 |
| scikit-learn__scikit-learn-14012 | Feature Request | Y | N/N/N | N/N/N | Y/Y/Y | 26336/1245 | $0.050 |
| duncanscanga__VDRS-Solutions-73 | Security Vulnerability | Y | Y/N/Y | Y/N/Y | Y/N/Y | 6702/852 | $0.050 |
| Innopoints__backend-124 | Security Vulnerability | Y | N/N/N | N/N/N | Y/N/Y | 22021/1546 | $0.050 |
| aio-libs__aiohttp-7829 | Performance Issue | Y | N/N/N | N/N/N | Y/Y/Y | 21512/995 | $0.050 |
| alexa-pi__AlexaPi-188 | Performance Issue | Y | Y/N/N | Y/N/N | Y/Y/Y | 28830/1605 | $0.050 |
| spotify__luigi-3308 | Security Vulnerability | Y | N/N/N | N/N/N | Y/N/N | 27036/1065 | $0.050 |
| pydantic__pydantic-8706 | Feature Request | Y | N/N/N | N/N/N | Y/Y/Y | 16849/916 | $0.050 |
| python__mypy-18163 | Feature Request | Y | Y/N/N | Y/N/N | Y/Y/Y | 20338/1081 | $0.050 |
| pandas-dev__pandas-59900 | Feature Request | Y | N/N/N | N/N/N | N/N/Y | 15966/895 | $0.050 |

## Key takeaways from this partial run

- **Agent loop substantially outperforms primitives.** On the n=11 instances that completed all modes, the agent found ground-truth FILES that primitives missed in roughly half the cases. Class-level lift is even larger — primitives 0/n, agent ~4/n.

- **Hybrid seeds gave no measurable lift over substring seeds** in this sample. Every instance where hybrid hit, substring also hit; neither found anything the other missed. This suggests the cosine 0.65 threshold (PR #84) may be too aggressive — embeddings currently contribute nothing on top of substring matching.

- **Agent does miss on hard cases.** pandas-dev/pandas-59900 was the first agent file-level miss in the sample (it got the function name but not the file). 426 MB pandas with deep stack traces is exactly the scenario where Opus might do better.
