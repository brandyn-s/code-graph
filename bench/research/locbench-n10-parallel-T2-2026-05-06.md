# Loc-Bench N=10 batch results — 2026-05-06 14:09

## Summary

- Instances attempted: 10
- Indexed successfully: 7
- Agent ran: 7
- File-level hit (any ground-truth file in output): 7
- Class-level hit: 3
- Function-level hit: 7
- Total LLM tokens: 822,734 input, 21,541 output
- Estimated cost: $0.35
- File-level accuracy (vs LocAgent's published 92.7%): 100.0% (7/7)

## Per-instance results

| instance_id | repo | category | indexed | agent | file | class | func | turns | tokens | $ | note |
|---|---|---|---|---|---|---|---|---|---|---|---|
| huggingface__accelerate-3248 | huggingface/accelerate | Bug Report | Y | Y | Y | N | Y | 8 | 39438/1920 | 0.050 |  |
| tobymao__sqlglot-4480 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| tobymao__sqlglot-3167 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| pydantic__pydantic-10374 | pydantic/pydantic | Feature Request | Y | Y | Y | N | Y | 12 | 90186/2631 | 0.050 |  |
| aio-libs__aiohttp-7829 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 18 | 223435/4415 | 0.050 |  |
| aio-libs__aiohttp-9692 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 11 | 74262/3011 | 0.050 |  |
| spotify__luigi-3308 | spotify/luigi | Security Vulnerability | Y | Y | Y | N | Y | 6 | 23346/1384 | 0.050 |  |
| duncanscanga__VDRS-Solutions-73 | duncanscanga/VDRS-Solutions | Security Vulnerability | Y | Y | Y | N | Y | 12 | 103076/3868 | 0.050 |  |
| ranaroussi__yfinance-2173 | ranaroussi/yfinance | Feature Request | Y | Y | Y | Y | Y | 25 | 268991/4312 | 0.050 |  |
| tobymao__sqlglot-3417 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
