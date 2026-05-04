# Loc-Bench N=2 batch results — 2026-04-25 08:17

## Summary

- Instances attempted: 2
- Indexed successfully: 1
- Agent ran: 1
- File-level hit (any ground-truth file in output): 1
- Class-level hit: 0
- Function-level hit: 0
- Total LLM tokens: 28,232 input, 1,197 output
- Estimated cost: $0.05
- File-level accuracy (vs LocAgent's published 92.7%): 100.0% (1/1)

## Per-instance results

| instance_id | repo | category | indexed | agent | file | class | func | turns | tokens | $ | note |
|---|---|---|---|---|---|---|---|---|---|---|---|
| jax-ml__jax-19601 | jax-ml/jax | Feature Request | Y | Y | Y | N | N | 5 | 28232/1197 | 0.050 |  |
| huggingface__transformers-34279 | huggingface/transformers | Feature Request | N | N | N | N | N | 0 | 0/0 | 0.000 | repo too large (552 MB > 200) |
