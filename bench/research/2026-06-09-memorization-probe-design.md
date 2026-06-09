# Loc-Bench memorization / contamination probe — design (2026-06-09)

**Status: DRAFT harness shipped, not yet run at scale.** `memorization_probe.py`
is committed with an offline `--self-test` (green). A real verdict requires a
recorded run under `bench/accuracy/baselines/` — until then this is a method, not
a finding (ship-discipline rule 10: **BLOCKED ON MEASUREMENT**).

## Why

Our defended Loc-Bench number is `code_localize_agent` **file Acc@10 = 86.0%**
(n=200, 2026-05-04). That number is produced by the *full* pipeline: the model
sees the issue text **and** has graph-guided tools over an indexed copy of the
repo. The external critique we currently **cannot rebut** is the "SWE-Bench
Illusion" (arXiv:2506.12286; independently replicated arXiv:2512.10218): a high
coding-localization score can partly reflect the model having **memorized** the
repo's file layout (and sometimes the fix) during pre-training, not localization
over our graph. Our CLAUDE.md already flags scorer/sample/protocol caveats and the
GC'd-`base_commit` reproduction problem — but it makes no claim either way about
*contamination*. This probe is the missing measurement.

## Method (one isolating arm + two free controls)

**Arm A — issue-text-only.** Give the model ONLY the `problem_statement` (no repo,
no file contents, no code-graph tools) and ask for a ranked list of files to edit.
Score file hit-rate vs the `edit_functions` ground-truth file parts.

Compare Arm A to the full-pipeline baseline (0.860):

| Outcome | Read |
|---|---|
| Arm A ≪ baseline (gap ≥ 0.30) | **LOW** contamination concern — the graph does real work the issue text can't recover. *This is the claim our docs can't currently defend.* |
| Arm A ≈ baseline | **HIGH** concern — the files are nameable from issue text with no repo; the headline is largely recoverable without our graph (memorization signature). |

**Control 1 — popular-vs-rare repo stratification** (no extra data; uses
`locbench_repos.json`). Loc-Bench's heavy repos (django=35, yt-dlp=27, vllm=24,
pandas=22, dask=20…) are vastly more represented in any code-training corpus than
its long-tail repos. A large `popular_minus_rare` gap in Arm-A accuracy is itself
a memorization signal. The clean HIGH-contamination verdict requires **both** a
high Arm A **and** a large popular−rare gap.

**Control 2 — shuffled-GT base-rate** (`--shuffle-control`). Score each
prediction against a *random other* instance's ground truth. `(Arm A − shuffled)`
strips the base-rate of guessing common paths (`src/…`, `tests/…`) and is the
clean, base-rate-free localization signal.

## Steelman (read before over-reading a high Arm A)

Loc-Bench (LocAgent, arXiv:2503.09089) was **deliberately built from recent issues
to resist contamination**, and issue text frequently *names* the files/symbols, so
some Arm-A signal is legitimate, not memorization. Caveats that keep this honest:

- A high Arm A alone is **not** proof — pair it with the popular−rare gap and, if
  the parquet carries issue/commit dates, a pre-vs-post model-cutoff split.
- Repo-layout memorization (knowing django's tree) is distinct from fix
  memorization; Arm A catches the former, which is the more plausible contaminant
  for popular repos even when the specific issue is post-cutoff.
- This measures the *model's* prior, not our graph's correctness. A LOW result is
  the strong, citable outcome: "our 86% is not issue-text-recoverable."

## Run

```bash
python3 bench/research/memorization_probe.py --self-test          # offline, no deps
python3 bench/research/memorization_probe.py --dry-run --sample 1 # inspect a prompt
# real (needs anthropic + ANTHROPIC_API_KEY + local locbench.parquet):
python3 bench/research/memorization_probe.py --sample 200 --shuffle-control \
    --model "$ANTHROPIC_MODEL" \
    --out bench/accuracy/baselines/2026-06-09-memorization-probe-n200.json
```

Match `--model` to the model whose 86% you are defending (default
`claude-haiku-4-5-20251001`). `anthropic` is **not** currently a bench dependency
— installing it is the one prerequisite for the real run.

## Dependencies / limits

- Needs the gitignored local `locbench.parquet` (issue text + `edit_functions`);
  the committed `locbench_v1_instances.json` carries only ids/repo/commit/category.
- Arm A is no-repo by construction, so it is a **lower bound** on what the issue
  text affords (it cannot read code); that asymmetry favors the LOW (good) verdict
  and should be stated when citing.
- Cost ≈ one cheap completion per instance (~512 output tokens); n=200 is a few
  dollars at Haiku.

**Key terms:** SWE-Bench Illusion, benchmark contamination, data leakage,
memorization probe, Loc-Bench, issue-text-only localization, code_localize_agent.
