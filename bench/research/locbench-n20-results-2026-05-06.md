# Loc-Bench N=20 batch results — 2026-05-06 09:13

## Summary

- Instances attempted: 20
- Indexed successfully: 6
- Agent ran: 6
- File-level hit (any ground-truth file in output): 6
- Class-level hit: 4
- Function-level hit: 5
- Total LLM tokens: 1,147,749 input, 24,561 output
- Estimated cost: $0.30
- File-level accuracy (vs LocAgent's published 92.7%): 100.0% (6/6)

## Per-instance results

| instance_id | repo | category | indexed | agent | file | class | func | turns | tokens | $ | note |
|---|---|---|---|---|---|---|---|---|---|---|---|
| huggingface__accelerate-3279 | huggingface/accelerate | Bug Report | Y | Y | Y | N | Y | 15 | 126174/3836 | 0.050 |  |
| ray-project__ray-48793 | ray-project/ray | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (937 MB > 200) |
| kornia__kornia-3084 | kornia/kornia | Bug Report | Y | Y | Y | Y | Y | 20 | 215603/4642 | 0.050 |  |
| scikit-learn__scikit-learn-14012 | scikit-learn/scikit-learn | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (201 MB > 200) |
| aio-libs__aiohttp-7829 | aio-libs/aiohttp | Performance Issue | Y | Y | Y | Y | Y | 14 | 152146/3577 | 0.050 |  |
| yt-dlp__yt-dlp-11542 | yt-dlp/yt-dlp | Bug Report | Y | Y | Y | Y | Y | 15 | 195434/3925 | 0.050 |  |
| langchain-ai__langgraph-2724 | langchain-ai/langgraph | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (581 MB > 200) |
| vllm-project__vllm-10076 | vllm-project/vllm | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (212 MB > 200) |
| ray-project__ray-48907 | ray-project/ray | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (936 MB > 200) |
| ray-project__ray-48782 | ray-project/ray | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (936 MB > 200) |
| tobymao__sqlglot-4524 | tobymao/sqlglot | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| tobymao__sqlglot-4434 | tobymao/sqlglot | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| vllm-project__vllm-7874 | vllm-project/vllm | Performance Issue | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (209 MB > 200) |
| alexa-pi__AlexaPi-188 | alexa-pi/AlexaPi | Performance Issue | Y | Y | Y | Y | Y | 16 | 200501/4227 | 0.050 |  |
| pandas-dev__pandas-19074 | pandas-dev/pandas | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (428 MB > 200) |
| ranaroussi__yfinance-2122 | ranaroussi/yfinance | Bug Report | Y | Y | Y | N | N | 22 | 257891/4354 | 0.050 |  |
| vllm-project__vllm-10398 | vllm-project/vllm | Bug Report | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (214 MB > 200) |
| ray-project__ray-48957 | ray-project/ray | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (937 MB > 200) |
| tobymao__sqlglot-3901 | tobymao/sqlglot | Performance Issue | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
| prowler-cloud__prowler-5933 | prowler-cloud/prowler | Performance Issue | N | N | N | N | N | 0 | 0/0 | 0.000 | clone failed |
