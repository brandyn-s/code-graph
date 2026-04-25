# Loc-Bench multi-mode comparison — 2026-04-25 12:10

**Binary:** Haiku 4.5, structured scorer, cosine=0.65
**Modes compared:** substring-primitives, hybrid-primitives, hybrid-agent
**Repo size cap:** 1000 MB

## Aggregate

Instances attempted: 16 | Indexed: 16

| Mode | Attempted | File hits | Class hits | Func hits | Total $ |
|---|---|---|---|---|---|
| substring-primitives | 16 | 6/16 (38%) | 1/16 (6%) | 2/16 (12%) | $0.00 |
| hybrid-primitives | 16 | 6/16 (38%) | 1/16 (6%) | 2/16 (12%) | $0.00 |
| hybrid-agent | 16 | 13/16 (81%) | 6/16 (38%) | 6/16 (38%) | $0.75 |

## Per-instance details

| instance | category | size (MB) | indexed | substring-primitives F/C/Fn | hybrid-primitives F/C/Fn | hybrid-agent F/C/Fn | note |
|---|---|---|---|---|---|---|---|
| Chainlit__chainlit-1441 | Security Vulnerability | 7 | Y | Y/N/N | Y/N/N | Y/N/Y |  |
| internetarchive__openlibrary-3196 | Security Vulnerability | 13 | Y | N/N/N | N/N/N | N/N/N |  |
| scikit-learn__scikit-learn-14012 | Feature Request | 22 | Y | N/N/N | N/N/N | Y/Y/N |  |
| duncanscanga__VDRS-Solutions-73 | Security Vulnerability | 0 | Y | Y/N/Y | Y/N/Y | Y/N/Y |  |
| Innopoints__backend-124 | Security Vulnerability | 0 | Y | N/N/N | N/N/N | Y/N/Y |  |
| aio-libs__aiohttp-7829 | Performance Issue | 4 | Y | N/N/N | N/N/N | Y/Y/N |  |
| alexa-pi__AlexaPi-188 | Performance Issue | 1 | Y | Y/N/N | Y/N/N | Y/N/N |  |
| spotify__luigi-3308 | Security Vulnerability | 7 | Y | N/N/N | N/N/N | Y/N/Y |  |
| pydantic__pydantic-8706 | Feature Request | 9 | Y | N/N/N | N/N/N | Y/Y/Y |  |
| python__mypy-18163 | Feature Request | 19 | Y | Y/Y/N | Y/Y/N | Y/Y/N |  |
| pandas-dev__pandas-59900 | Feature Request | 65 | Y | N/N/N | N/N/N | N/N/N |  |
| vllm-project__vllm-11138 | Feature Request | 19 | Y | N/N/N | N/N/N | N/N/N |  |
| yt-dlp__yt-dlp-11542 | Bug Report | 14 | Y | N/N/N | N/N/N | Y/Y/N |  |
| ranaroussi__yfinance-2122 | Bug Report | 5 | Y | Y/N/N | Y/N/N | Y/N/N |  |
| huggingface__accelerate-3279 | Bug Report | 4 | Y | Y/N/Y | Y/N/Y | Y/N/Y |  |
| kornia__kornia-3084 | Bug Report | 13 | Y | N/N/N | N/N/N | Y/Y/N |  |

