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
# One-time: ensure `uv` is installed (https://github.com/astral-sh/uv).
# PyCG needs Python 3.11 + 3 local patches; `_env.py` provisions that venv
# idempotently on first run. No manual setup required.

pip install anthropic   # only LLM-ensemble oracle needs this on the system python

# Verify binary is current:
CGO_ENABLED=1 go build -o bin/codebase-memory-mcp.exe ./cmd/codebase-memory-mcp/

# Run each oracle + compare:
python bench/accuracy/oracle_pycg.py mcp-servers       # CALLS  (auto-bootstraps bench venv + patches)
python bench/accuracy/oracle_ast_imports.py mcp-servers # IMPORTS (stdlib only)
python bench/accuracy/oracle_llm_ensemble.py mcp-servers # HTTP_CALLS (needs ANTHROPIC_API_KEY)
python bench/accuracy/compare.py mcp-servers

# Output:
#   bench/accuracy/baselines/YYYY-MM-DD-mcp-servers-report.md   (human)
#   bench/accuracy/baselines/YYYY-MM-DD-mcp-servers-report.json (machine)
```

### Pre-provisioning the bench venv (CI, first-timers)

```bash
python bench/accuracy/_env.py
```

Creates `~/.cache/code-graph-bench/py311/` (uv-managed), installs
`setuptools<81` + `pycg==0.0.7`, and applies three patches to
`pycg/machinery/imports.py` that make PyCG work on Python 3.11+.
Idempotent — subsequent calls return the cached interpreter path.

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

## Metrics reported

Per edge type, three metrics:

- **Exact**: strict `(from_qn, to_qn, type)` equality. Baseline number — may be depressed by scope mismatches.
- **Suffix-3**: permissive match on the last 3 QN segments. Useful for
  spotting QN-drift artifacts between oracle and code-graph.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's
  analyzed-caller set. This is the apples-to-apples accuracy reading —
  it excludes code-graph edges from callers the oracle never reached
  (e.g., test files PyCG's entry-point analysis doesn't touch).

The gap between "Exact" and "Scope-aligned" is the *scope-mismatch
artifact* — not an accuracy bug. For mcp-servers CALLS, raw exact
precision is 14.5% but scope-aligned is 93.5% because code-graph indexes
the full repo while PyCG only walks 5 service entry points.

## Known limitations

- **PyCG is flow-insensitive**; it will have its own false negatives on dynamic dispatch. We measure comparative drift, not absolute truth.
- **LLM ensemble non-determinism**: runs are cached per `(file_sha, model)` so results are reproducible across re-runs, but cache invalidation on file change means the first run of a new fixture SHA is stochastic.
- **Python-only first fixture**: Go and Rust fixtures will be added once the Python pipeline is validated.

## Hand-oracle methodology: multi-annotator + Cohen's Kappa

Single-annotator hand-oracles (like `nix_pubsub_hand_oracle.json` as first
shipped) carry self-bias risk — the annotator's mental model of the pattern
may be the same as the extractor's, so systemic bugs go undetected. When
expanding hand-oracle coverage to a new language (Rust CALLS, Python CALLS,
Go stdlib edges, etc.), require **≥2 independent annotators** per fixture
and compute **Cohen's Kappa** via `check_hand_oracle_kappa.py`.

**Standard**: κ ≥ 0.81 = "almost perfect agreement" (threshold from
[Code Debloating ground-truth, arXiv 2604.17717](https://arxiv.org/abs/2604.17717)
and [CLEVER, NeurIPS 2025](https://arxiv.org/abs/2505.13938)). Below 0.81
means either the fixture is ambiguous (split on a case neither annotator
was prepared for) or the annotator protocol needs refinement.

### Multi-annotator schema

```json
{
  "_annotators": ["brandyn-2026-04-24", "reviewer-tbd"],
  "services": [
    {
      "service": "canstatd",
      "source_file": "nix/modules/canstatd.nix",
      "annotations": {
        "brandyn-2026-04-24": {
          "pub_topics": ["canstatd"],
          "sub_topics": []
        },
        "reviewer-tbd": {
          "pub_topics": ["canstatd"],
          "sub_topics": []
        }
      }
    }
  ]
}
```

The legacy single-annotator path (top-level `pub_topics`/`sub_topics`)
remains supported — `check_hand_oracle_kappa.py` reports "single-annotator,
kappa not applicable" and exits 0, preserving existing CI behavior.

### Usage

```bash
python bench/accuracy/check_hand_oracle_kappa.py \
  bench/accuracy/nix_pubsub_hand_oracle.json

# Override default threshold (0.81) if needed:
python bench/accuracy/check_hand_oracle_kappa.py \
  bench/accuracy/rust_calls_hand_oracle.json --threshold 0.75
```

Exit codes: `0` = pass or single-annotator, `1` = multi-annotator below
threshold, `2` = schema/file error.
