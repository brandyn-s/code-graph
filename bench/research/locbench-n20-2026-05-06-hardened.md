# Loc-Bench N=20 batch results — 2026-05-06 18:26

## Summary

- Instances attempted: 20
- Indexed successfully: 14
- Agent ran: 14
- File-level hit (any ground-truth file in output): 13
- Class-level hit: 5
- Function-level hit: 11
- Total LLM tokens: 2,505,832 input, 48,971 output
- Estimated cost: $0.70
- File-level accuracy (vs LocAgent's published 92.7%): 92.9% (13/14)

## Per-instance results

| instance_id | repo | category | indexed | agent | file | class | func | turns | tokens | $ | note |
|---|---|---|---|---|---|---|---|---|---|---|---|
| huggingface__accelerate-3248 | huggingface/accelerate | Bug Report | Y | Y | Y | N | Y | 14 | 98939/2834 | 0.050 |  |
| tobymao__sqlglot-4480 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| huggingface__accelerate-3279 | huggingface/accelerate | Bug Report | Y | Y | Y | N | Y | 13 | 118574/3674 | 0.050 |  |
| tobymao__sqlglot-4524 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| tobymao__sqlglot-4366 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| tobymao__sqlglot-3167 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| pydantic__pydantic-10374 | pydantic/pydantic | Feature Request | Y | Y | Y | N | Y | 11 | 75007/2475 | 0.050 |  |
| tobymao__sqlglot-4434 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| pydantic__pydantic-10789 | pydantic/pydantic | Feature Request | Y | Y | Y | Y | N | 34 | 497352/4403 | 0.050 |  |
| tobymao__sqlglot-3417 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| aio-libs__aiohttp-7829 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 13 | 143036/3558 | 0.050 |  |
| aio-libs__aiohttp-9692 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 22 | 209341/3634 | 0.050 |  |
| aio-libs__aiohttp-9766 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 12 | 56609/2865 | 0.050 |  |
| pydantic__pydantic-10601 | pydantic/pydantic | Performance Issue | Y | Y | Y | N | Y | 17 | 193068/3823 | 0.050 |  |
| aio-libs__aiohttp-9767 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 11 | 58518/2841 | 0.050 |  |
| spotify__luigi-3308 | spotify/luigi | Security Vulnerability | Y | Y | Y | N | Y | 8 | 37276/2224 | 0.050 |  |
| duncanscanga__VDRS-Solutions-73 | duncanscanga/VDRS-Solutions | Security Vulnerability | Y | Y | Y | N | Y | 13 | 104439/3551 | 0.050 |  |
| internetarchive__openlibrary-3196 | internetarchive/openlibrary | Security Vulnerability | Y | Y | N | N | N | 34 | 630322/6381 | 0.050 |  |
| Innopoints__backend-124 | Innopoints/backend | Security Vulnerability | Y | Y | Y | N | N | 15 | 118904/3755 | 0.050 |  |
| Chainlit__chainlit-1441 | Chainlit/chainlit | Security Vulnerability | Y | Y | Y | N | Y | 16 | 164447/2953 | 0.050 |  |
