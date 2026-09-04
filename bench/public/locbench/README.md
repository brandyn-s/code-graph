# Reproducible public LocBench run

This directory makes the LocBench numbers quoted in
[`docs/measured-evidence.md`](../../../docs/measured-evidence.md) something
anyone can re-run, and states precisely which of them cannot be.

## What is reproducible today: the n=200 agent baseline

The `code_localize_agent` file/class/function Acc@10 figures (86.0 / 84.5 /
73.5 defended on 2026-05-04, reproduced within CI on 2026-06-12) run on a
pinned set of 200 Loc-Bench instances.

| Input | Pin |
|---|---|
| Dataset | Hugging Face `czlll/Loc-Bench_V1`, split `test` (downloaded to `bench/research/locbench.parquet`; its SHA-256 is recorded in the run output) |
| Instances | `bench/accuracy/baselines/data/2026-06-12-matched-depth-n200/locbench-n200-pin.json` (200 `pinned_instance_ids` with repo and `base_commit`; SHA-256 `886156bb…0556453`, see the `.sha256` file beside it) |
| Engine | The code-graph commit you build; record `git rev-parse HEAD` and the binary SHA-256 alongside the result |
| Agent model | Anthropic model from `ANTHROPIC_MODEL` (default built in); `LOCAGENT_ITERATIONS=2` |
| Embedding seeds | Optional `VOYAGE_API_KEY` |

Cost and time from the 2026-06-12 run: about 4.3 hours wall with four
workers and $50.51 in token-metered Anthropic spend (mean $0.25 per instance,
heaviest $0.89). Budget with `--budget-usd`; the harness stops when the
estimate is exceeded.

```bash
export ANTHROPIC_API_KEY=...        # required
export VOYAGE_API_KEY=...           # optional, enables embedding seeds
bench/public/locbench/run.sh --budget-usd 60 --out /tmp/locbench-n200
```

`run.sh` verifies the pin's digest, writes the instance-id list the harness
consumes (`pin-ids.json` in the output directory), builds `code-graph`
and the `eval_rank_localize` harness, downloads the parquet, and runs
`bench/research/eval_locbench_batch.py --instances pin-ids.json`.
Expected outputs in `--out`: `report.md` with the three Acc@10 rows and a
paired-bootstrap comparison against the recorded baseline, `cases.json` with
one row per instance, and `provenance.json` with dataset, pin, engine, and
binary digests. A clean reproduction is one whose 95% bootstrap intervals for
all three metrics include zero difference against the recorded baseline.

Smoke the plumbing first without spending:

```bash
bench/public/locbench/run.sh --smoke     # builds, downloads, validates the pin; no agent calls
```

## What is not reproducible from this repository: the n=80 graph-only replay

`docs/measured-evidence.md` also quotes a graph-only conceptual localization
replay on a "frozen balanced public LocBench n=80" cohort (Acc@1 0.175 →
0.200, MRR@10 0.219 → 0.260). The baseline document
`bench/accuracy/baselines/2026-08-13-graph-concept-localize-seed-quality.md`
records the SHA-256 of the case list (`de09dcbb…`) and oracle (`1f0cdf82…`)
and the two binaries, but the repository does not contain:

- the 80-case list and labels themselves;
- the preserved query and store inputs the replay reused byte-for-byte;
- the oracle file and the scorer invocation that kept "the first ten distinct
  files".

Until those artifacts are added under this directory (case list JSON, oracle
JSON, and a `run_n80.sh` that rebuilds the stores from the pinned commits), the
n=80 figures should be read as an internal paired replay, not a public
benchmark. The README's differentiation claims do not rely on them.
