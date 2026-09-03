# Benchmark Harness Plan

Measured before/after for 7 feature PRs lifting patterns from graphify into code-graph and code-search.

## Why this exists

The revised [graphify-improvements plan](../docs/plans/graphify-improvements.md) requires every feature PR to ship with measured pre/post metrics, not just unit tests. This directory holds the harness that produces those measurements.

Three artifacts:

1. **0a — Repo benchmark suite** (`bench/harness.py` + `bench/fixtures.json` + `bench/questions.json`): shell out to `codebase-memory-mcp cli`, run index + query suite against 4 frozen redacted repos at pinned SHAs, output `bench/baseline_YYYY-MM-DD.json`.
2. **0b — Transcript replay corpus** (`bench/transcripts.py`): filter ~/.claude/projects/ for sessions using code-search/code-graph, extract tool call sequences, output `bench/transcripts_YYYY-MM-DD.jsonl`.
3. **0c — PR ground-truth set** (`bench/pr_groundtruth.jsonl`): 20 merged PRs with verified blast-radius. Eval set for PR 5 (graph diff) and PR 1 (confidence calibration).

## Frozen fixture repos (on-disk at pinned SHAs)

| Repo | SHA | Language mix | Why chosen |
|------|-----|--------------|------------|
| mcp-servers | 81fa7d5 | Python + YAML + HCL | Primary production workload, 2,725 nodes baseline |
| rmf-corsair | f545976 | Rust + Nix | Compliance repo; tests Rust + Nix parser parity |
| mcp-infra | 8173017 | Python + Terraform/HCL | Infrastructure-heavy; tests HCL + config linking |
| code-graph | c9b1195 | Go (self-hosting) | Meta-test: tool indexes itself |

SHAs pinned in `bench/fixtures.json` (a local, uncommitted file). Re-pin by updating that file when baseline is re-captured.

## 20-question standard set

Derived from existing [BENCHMARK.md](../BENCHMARK.md) 12-question methodology + 8 targeted at incoming feature PRs:

| # | Category | Question | Tool | Feature coverage |
|---|----------|----------|------|------------------|
| Q1 | Indexing | Graph stats | `get_graph_schema` | baseline |
| Q2 | Discovery | Find functions | `search_graph(label="Function")` | baseline |
| Q3 | Discovery | Find classes | `search_graph(label="Class")` | baseline |
| Q4 | Pattern | Find by name | `search_graph(name_pattern=...)` | baseline |
| Q5 | Code | Get snippet | `get_code_snippet` | baseline |
| Q6 | Search | Text search | `search_code` | baseline |
| Q7 | Trace | Outbound call | `trace_call_path(outbound)` | baseline |
| Q8 | Trace | Inbound call | `trace_call_path(inbound)` | baseline |
| Q9 | Cypher | CALLS query | `query_graph` | baseline |
| Q10 | Enrich | Params/returns | `query_graph` | baseline |
| Q11 | OOP | Inheritance | `query_graph` | baseline |
| Q12 | Files | List dirs | `list_directory` | baseline |
| **Q13** | **Confidence** | **Only confident callers** | `query_graph WHERE r.confidence='EXTRACTED'` | **PR 1** |
| **Q14** | **Community** | **Largest community size** | `get_architecture` (cohesion) | **PR 2** |
| **Q15** | **Orientation** | **Report exists + well-formed** | file check on `ARCHITECTURE_REPORT.md` | **PR 3** |
| **Q16** | **Rationale** | **Find SAFETY annotations** | `find_rationale(kind="SAFETY")` | **PR 4** |
| **Q17** | **Diff** | **PR blast radius** | `diff_graph(base, head)` | **PR 5** |
| **Q18** | **Similarity** | **Similar function pairs** | `find_similar_functions(name)` | **PR 7** |
| **Q19** | **Explain+rationale** | **Why does X do Y** | `explain_symbol(name)` includes rationale | **PR 4** |
| **Q20** | **Diff in review** | **get_review_context uses diff** | `get_review_context` includes graph diff | **PR 5** |

Q1-Q12 establish the floor. Q13-Q20 are NEW capabilities — pre-baseline answer is "not supported"; post-change answer is captured.

## Metrics captured per question

- Wall-clock latency (ms)
- Result count
- Correctness vs ground truth (PASS / PARTIAL / FAIL / N/A) — ground truth in `bench/expected/<repo>/Q<N>.json`
- Result hash (detects silent behavior changes)

## Metrics captured per repo

- Index wall-clock
- Index size on disk (`du -sb ~/.cache/codebase-memory-mcp/<project>.db`)
- Node count by label
- Edge count by type
- Confidence distribution (once PR 1 lands)
- Community size distribution + cohesion (once PR 2 lands)

## Output schema (`baseline_YYYY-MM-DD.json`)

```json
{
  "date": "2026-04-22",
  "binary_sha": "c9b1195",
  "binary_version": "v0.3.0",
  "repos": {
    "mcp-servers": {
      "sha": "81fa7d5",
      "index_ms": 12450,
      "index_size_bytes": 8421376,
      "nodes_by_label": {"Function": 1316, "Method": 90, "Class": 34, ...},
      "edges_by_type": {"CALLS": 2443, "DEFINES": 1315, ...},
      "questions": {
        "Q1": {"latency_ms": 12, "result_hash": "abc...", "correctness": "PASS", "result_preview": "..."},
        "Q13": {"latency_ms": null, "correctness": "N/A", "note": "Feature PR 1 not merged"},
        ...
      }
    },
    "rmf-corsair": {...},
    "mcp-infra": {...},
    "code-graph": {...}
  }
}
```

## Usage

```bash
# First run — captures main-branch baseline
python bench/harness.py --output bench/baseline_$(date +%F).json

# After each feature PR merge — re-run and diff
python bench/harness.py --output bench/after_pr1_$(date +%F).json
python bench/compare.py bench/baseline_*.json bench/after_pr1_*.json
```

## Reproducibility

- Fixture SHAs pinned in `fixtures.json`. A baseline run is invalid if any fixture has local modifications (`git diff --quiet`).
- Binary SHA captured in output — required for any comparison to be meaningful.
- All 4 fixtures must pass `git status` clean check before the harness runs.

## Stop-ship enforcement

Each feature PR's `PR.md` declares metric deltas it must achieve. The harness `compare.py` script prints PASS/FAIL per declared metric. CI can gate on this (future work).
