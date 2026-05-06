# Loc-Bench N=50 batch results — 2026-05-06 12:48

## Summary

- Instances attempted: 50
- Indexed successfully: 35
- Agent ran: 35
- File-level hit (any ground-truth file in output): 31
- Class-level hit: 17
- Function-level hit: 25
- Total LLM tokens: 7,735,170 input, 135,979 output
- Estimated cost: $1.75
- File-level accuracy (vs LocAgent's published 92.7%): 88.6% (31/35)

## Per-instance results

| instance_id | repo | category | indexed | agent | file | class | func | turns | tokens | $ | note |
|---|---|---|---|---|---|---|---|---|---|---|---|
| huggingface__accelerate-3248 | huggingface/accelerate | Bug Report | Y | Y | Y | N | Y | 11 | 68770/2417 | 0.050 |  |
| tobymao__sqlglot-4480 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| huggingface__accelerate-3279 | huggingface/accelerate | Bug Report | Y | Y | Y | N | Y | 21 | 195934/4329 | 0.050 |  |
| tobymao__sqlglot-4524 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| tobymao__sqlglot-4366 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| langchain-ai__langgraph-2735 | langchain-ai/langgraph | Bug Report | Y | Y | Y | N | Y | 13 | 110998/3205 | 0.050 |  |
| tobymao__sqlglot-4519 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| tobymao__sqlglot-4526 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| kornia__kornia-3084 | kornia/kornia | Bug Report | Y | Y | Y | Y | Y | 17 | 202579/4288 | 0.050 |  |
| langchain-ai__langgraph-2724 | langchain-ai/langgraph | Bug Report | Y | Y | Y | Y | Y | 15 | 180638/3437 | 0.050 |  |
| tobymao__sqlglot-4523 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| ranaroussi__yfinance-2139 | ranaroussi/yfinance | Bug Report | Y | Y | N | N | N | 18 | 184850/4820 | 0.050 |  |
| tobymao__sqlglot-3167 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| pydantic__pydantic-10374 | pydantic/pydantic | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| tobymao__sqlglot-4434 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| pydantic__pydantic-10789 | pydantic/pydantic | Feature Request | Y | Y | Y | Y | N | 30 | 412568/5324 | 0.050 |  |
| tobymao__sqlglot-3417 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| pydantic__pydantic-10210 | pydantic/pydantic | Feature Request | Y | Y | Y | N | Y | 13 | 104362/3003 | 0.050 |  |
| ranaroussi__yfinance-2173 | ranaroussi/yfinance | Feature Request | Y | Y | Y | Y | Y | 24 | 273893/4624 | 0.050 |  |
| pydantic__pydantic-8706 | pydantic/pydantic | Feature Request | Y | Y | Y | Y | Y | 15 | 132896/3105 | 0.050 |  |
| pydantic__pydantic-9478 | pydantic/pydantic | Feature Request | Y | Y | Y | N | Y | 21 | 276644/4337 | 0.050 |  |
| tobymao__sqlglot-3436 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| huggingface__diffusers-9659 | huggingface/diffusers | Feature Request | Y | Y | Y | Y | N | 14 | 163814/3775 | 0.050 |  |
| vllm-project__vllm-10462 | vllm-project/vllm | Feature Request | Y | Y | Y | Y | Y | 13 | 123758/3604 | 0.050 |  |
| aio-libs__aiohttp-7829 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 16 | 190135/4064 | 0.050 |  |
| aio-libs__aiohttp-9692 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 23 | 203958/4485 | 0.050 |  |
| aio-libs__aiohttp-9766 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 12 | 69769/3280 | 0.050 |  |
| pydantic__pydantic-10601 | pydantic/pydantic | Performance Issue | Y | Y | Y | N | Y | 16 | 168966/3992 | 0.050 |  |
| aio-libs__aiohttp-9767 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 13 | 80603/2964 | 0.050 |  |
| tobymao__sqlglot-3901 | tobymao/sqlglot | Performance Issue | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| aio-libs__aiohttp-9762 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 10 | 41320/2540 | 0.050 |  |
| nautobot__nautobot-6837 | nautobot/nautobot | Performance Issue | Y | Y | Y | Y | Y | 35 | 787333/6576 | 0.050 |  |
| paperless-ngx__paperless-ngx-1227 | paperless-ngx/paperless-ngx | Performance Issue | Y | Y | Y | N | Y | 10 | 59233/3038 | 0.050 |  |
| Bears-R-Us__arkouda-1969 | Bears-R-Us/arkouda | Performance Issue | Y | Y | Y | Y | Y | 25 | 352280/4639 | 0.050 |  |
| zulip__zulip-14091 | zulip/zulip | Performance Issue | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| rapidsai__dask-cuda-98 | rapidsai/dask-cuda | Performance Issue | Y | Y | Y | Y | Y | 13 | 107160/3646 | 0.050 |  |
| spotify__luigi-3308 | spotify/luigi | Security Vulnerability | Y | Y | Y | N | Y | 8 | 41036/2203 | 0.050 |  |
| duncanscanga__VDRS-Solutions-73 | duncanscanga/VDRS-Solutions | Security Vulnerability | Y | Y | Y | N | Y | 9 | 55529/3616 | 0.050 |  |
| internetarchive__openlibrary-3196 | internetarchive/openlibrary | Security Vulnerability | Y | Y | Y | N | N | 36 | 716439/6074 | 0.050 |  |
| Innopoints__backend-124 | Innopoints/backend | Security Vulnerability | Y | Y | Y | N | N | 13 | 100235/3487 | 0.050 |  |
| Chainlit__chainlit-1441 | Chainlit/chainlit | Security Vulnerability | Y | Y | Y | N | Y | 19 | 240635/3582 | 0.050 |  |
| mathesar-foundation__mathesar-3117 | mathesar-foundation/mathesar | Security Vulnerability | Y | Y | Y | N | Y | 36 | 829138/5446 | 0.050 |  |
| jazzband__django-two-factor-auth-390 | jazzband/django-two-factor-auth | Security Vulnerability | Y | Y | Y | Y | N | 18 | 186374/4093 | 0.050 |  |
| Chainlit__chainlit-1575 | Chainlit/chainlit | Security Vulnerability | Y | Y | Y | N | N | 21 | 253515/4199 | 0.050 |  |
| streamlit__streamlit-9754 | streamlit/streamlit | Security Vulnerability | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (1055 MB > 1000) |
| django__django-13134 | django/django | Security Vulnerability | Y | Y | Y | Y | Y | 18 | 205436/4129 | 0.050 |  |
| sancus-tee__sancus-compiler-36 | sancus-tee/sancus-compiler | Security Vulnerability | Y | Y | N | N | N | 13 | 122373/2914 | 0.050 |  |
| jobatabs__textec-53 | jobatabs/textec | Security Vulnerability | Y | Y | N | N | N | 9 | 48976/2109 | 0.050 |  |
| tobymao__sqlglot-4415 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| huggingface__accelerate-3261 | huggingface/accelerate | Bug Report | Y | Y | N | N | N | 27 | 443023/4635 | 0.050 |  |
