# arxiv-bench primitives

Shared agent-runner primitives used by `bench/research/agent-effectiveness/`:

- `agent_runner.py`: tool-calling loop against a code-graph backend.
- `scorer.py`: continuous 0-1 LLM judge.
- `tool_allowlist.json`: tools the agent may call during a battery.

The original arxiv-bench question set and its results were measured against
private corpora and are not included in the public repository.
