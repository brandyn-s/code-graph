# code-graph edge-level accuracy harness

Measures precision/recall/F1 of code-graph's structural edge extraction against
independent oracles, on SHA-pinned fixtures. Freezes baselines so future
improvements are measurable.

**Goal**: answer "is code-graph's CALLS extraction above 85% recall / 60%
precision?" with a number, not a guess. Sui et al. (ICSE 2020) reported
median 0.884 recall / 20-30% precision for Java static analyzers; we adjust
the precision target up because Python/Go code has less reflection
complexity than Java.

## Why this is LLM-only but not circular

code-graph already consumes LSP internally (`internal/cbm/cbm.go`), so LSP
can't serve as an independent oracle. Instead:

| Edge type | Oracle | Trust |
|-----------|--------|-------|
| CALLS     | PyCG (static analysis; peer-reviewed micro-benchmark) | High |
| IMPORTS   | Python `ast` stdlib (deterministic) | High |
| HTTP_CALLS | Opus + Sonnet ensemble with 2/3 majority tiebreaker | Medium |

None require human verification. Ground truth is frozen to JSON and
re-used for every future regression run.

## Directory layout

```
bench/accuracy/
  __init__.py
  README.md              this file
  fixtures.json          SHA-pinned fixtures (initial: mcp-servers @ 81fa7d5)
  common.py              Edge dataclass, SHA verification, subprocess wrapper
  oracle_pycg.py         CALLS ground truth via PyCG
  oracle_ast_imports.py  IMPORTS ground truth via Python ast
  oracle_llm_ensemble.py HTTP_CALLS ground truth via Opus+Sonnet
  compare.py             TP/FP/FN/P/R/F1 reporter
  run_baseline.py        orchestrator: verifies SHA, runs all oracles, runs code-graph, produces report
  cache/                 per-(fixture, sha, tool) oracle output cache
  baselines/             frozen baseline reports — committed to git
```

## Reproduction

```bash
# One-time:
pip install pycg==0.0.7 anthropic

# Verify binary is current:
CGO_ENABLED=1 go build -o bin/codebase-memory-mcp.exe ./cmd/codebase-memory-mcp/

# Run baseline against a fixture:
python bench/accuracy/run_baseline.py --fixture mcp-servers

# Output:
#   bench/accuracy/baselines/YYYY-MM-DD-mcp-servers-report.md   (human)
#   bench/accuracy/baselines/YYYY-MM-DD-mcp-servers-report.json (machine)
```

## Environment

- `AWS_BEARER_TOKEN_BEDROCK` — required for LLM ensemble oracle; refresh via `aws sso login` if 401.
- `ANTHROPIC_MODEL_OPUS` — defaults to `claude-opus-4-7` (override for testing).
- `ANTHROPIC_MODEL_SONNET` — defaults to `claude-sonnet-4-6`.
- Code-graph binary at `bin/codebase-memory-mcp.exe` in this repo.
- Fixture repos at paths in `fixtures.json`; must be at pinned SHA (harness exits 2 on drift).

## Regression workflow

1. Run baseline, commit `baselines/YYYY-MM-DD-<fixture>-report.{md,json}` to git.
2. Make an improvement (edit Go, rebuild binary).
3. Re-run baseline. The compare tool reports `was X, now Y, delta Z` against the most recent baseline JSON.
4. If numbers improved: commit the new baseline as the new reference. If they regressed: revert or fix.

## Known limitations

- **PyCG is flow-insensitive**; it will have its own false negatives on dynamic dispatch. We measure comparative drift, not absolute truth.
- **LLM ensemble non-determinism**: runs are cached per `(file_sha, model)` so results are reproducible across re-runs, but cache invalidation on file change means the first run of a new fixture SHA is stochastic.
- **Python-only first fixture**: Go and Rust fixtures will be added once the Python pipeline is validated.
