"""Memorization / contamination probe for the Loc-Bench localization numbers.

Question this answers
---------------------
Our headline `code_localize_agent` Loc-Bench numbers (file Acc@10 = 86.0%, see
CLAUDE.md) are produced by the FULL pipeline: the model gets the issue text AND
graph-guided tools over an indexed copy of the repo. A reasonable external
critique (the "SWE-Bench Illusion", arXiv:2506.12286; replicated arXiv:2512.10218)
is that part of a high coding-localization score can come from the model having
*memorized* the repo (its file layout, and sometimes the exact fix) during
pre-training — not from reasoning over our graph. If so, the number partly
measures recall, not localization.

This probe isolates that. It runs an ISSUE-TEXT-ONLY arm: the model is given
ONLY the problem statement and asked to name the files it thinks must change —
no repo, no file contents, no code-graph tools. Then it compares that no-context
accuracy to the full-pipeline baseline.

Interpretation
--------------
Let A = issue-text-only file-hit rate (this probe), B = full-pipeline baseline
(86.0% file Acc@10).

  * A small  (A << B)  -> the graph is doing real work the issue text can't; the
                         headline is NOT explained by memorization. GOOD — this
                         is the claim our docs currently cannot defend.
  * A large  (A ~= B)  -> the model can name the right files from the issue text
                         alone, with no repo. The headline is largely recoverable
                         without our graph, which is the memorization signature.
                         Treat the Loc-Bench number as contamination-exposed.

Two built-in controls sharpen the read (neither needs extra data):

  1. shuffle control (--shuffle-control): score each prediction against a RANDOM
     other instance's ground truth. (matched - shuffled) removes the base-rate of
     guessing common paths (src/..., tests/...). The *delta* is the real signal.
  2. repo-popularity stratification: Loc-Bench's heavy repos (django=35, yt-dlp=27,
     vllm=24, pandas=22 ...) are far more represented in any code-training set than
     its long-tail repos. If issue-text-only accuracy is much higher on popular
     repos than rare ones, that gap is itself a memorization signal.

Steelman (read before over-interpreting a high A)
-------------------------------------------------
Loc-Bench (LocAgent, arXiv:2503.09089) was deliberately built from RECENT issues
to resist contamination, and issue descriptions often *name* the files/symbols, so
some issue-text-only signal is legitimate, not memorization. The clean memorization
claim needs BOTH a high A AND a large popular-vs-rare (or pre-vs-post-cutoff) gap.
A uniformly modest A across strata is the contamination-clean outcome.

Status / scope
--------------
DRAFT probe. Not yet run at scale — running Arm A on the full n=560 needs
`ANTHROPIC_API_KEY`, the `anthropic` package, and the local `locbench.parquet`
(gitignored). The scoring/parsing logic is covered by `--self-test`, which runs
fully offline against synthetic data + a stub model. Ship-discipline: do not cite
a contamination verdict from this until a real run is recorded under
`bench/accuracy/baselines/`.

Usage
-----
    # offline correctness check (no deps, no key) — run this first
    python3 bench/research/memorization_probe.py --self-test

    # build + print one prompt against the real dataset, no API calls
    python3 bench/research/memorization_probe.py --dry-run --sample 1

    # real run (needs anthropic + ANTHROPIC_API_KEY + locbench.parquet)
    python3 bench/research/memorization_probe.py --sample 100 --shuffle-control \
        --out bench/accuracy/baselines/2026-06-09-memorization-probe-n100.json
"""
from __future__ import annotations

import argparse
import json
import os
import random
import re
import sys
from pathlib import Path
from typing import Any, Callable

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_PARQUET = REPO_ROOT / "bench/research/locbench.parquet"
REPOS_JSON = REPO_ROOT / "bench/research/locbench_repos.json"
DEFAULT_MODEL = os.environ.get("ANTHROPIC_MODEL", "claude-haiku-4-5-20251001")

# Full-pipeline baseline we are comparing against (CLAUDE.md, 2026-05-04, n=200).
FULL_PIPELINE_FILE_ACC10 = 0.860

PROMPT_TEMPLATE = """\
You are triaging a software bug report. You do NOT have access to the repository.
Based ONLY on the issue text below, list the repository-relative file paths that
most likely need to be edited to fix it. Rank most-likely first.

Repository: {repo}

Issue:
{problem}

Respond with ONLY a JSON array of up to {k} relative file path strings, e.g.
["src/pkg/foo.py", "src/pkg/bar.py"]. No prose, no commentary.
"""


# --------------------------------------------------------------------------- #
# Data loading                                                                #
# --------------------------------------------------------------------------- #
def load_instances(path: Path) -> list[dict[str, Any]]:
    """Load Loc-Bench instances. Needs problem_statement + edit_functions.

    Supports .parquet (via pandas, lazy import) or .json/.jsonl. The committed
    locbench_v1_instances.json does NOT carry problem_statement/edit_functions —
    that data is in the gitignored local parquet — so a clear error is raised if
    the required fields are missing.
    """
    if not path.exists():
        raise FileNotFoundError(
            f"{path} not found. The full Loc-Bench text/ground-truth lives in the "
            f"local (gitignored) locbench.parquet; pass --instances to point at it "
            f"or at a json/jsonl export carrying 'problem_statement' and "
            f"'edit_functions'."
        )
    if path.suffix == ".parquet":
        import pandas as pd  # lazy: only needed for real runs

        df = pd.read_parquet(path)
        rows = df.to_dict("records")
    elif path.suffix == ".jsonl":
        rows = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
    else:
        rows = json.loads(path.read_text(encoding="utf-8"))
    rows = [dict(r) for r in rows]
    _require_fields(rows, path)
    return rows


def _require_fields(rows: list[dict[str, Any]], path: Path) -> None:
    if not rows:
        raise ValueError(f"{path} loaded 0 instances")
    missing = {"problem_statement", "edit_functions"} - set(rows[0].keys())
    if missing:
        raise ValueError(
            f"{path} is missing required field(s) {sorted(missing)} (has "
            f"{sorted(rows[0].keys())}). This probe needs the issue text and "
            f"ground-truth edits; the committed instance index alone is not enough."
        )


def gt_files(edit_functions: Any) -> set[str]:
    """Extract the set of ground-truth FILE paths from edit_functions.

    Each entry is like 'src/pip/_internal/commands/install.py:InstallCommand.run';
    the file part is everything before the first ':'.
    """
    files: set[str] = set()
    for ef in edit_functions or []:
        s = str(ef)
        files.add(_norm_path(s.split(":", 1)[0]))
    return {f for f in files if f}


# --------------------------------------------------------------------------- #
# Scoring                                                                      #
# --------------------------------------------------------------------------- #
def _norm_path(p: str) -> str:
    p = p.strip().strip("`'\" ").replace("\\", "/")
    p = re.sub(r"^\./", "", p)
    return p.strip("/")


def path_match(pred: str, gt: str) -> bool:
    """Suffix-tolerant file match: handles the model omitting/adding a repo prefix.

    Matches on exact equality or a MULTI-COMPONENT path suffix. A bare basename
    (no '/') matches only by exact equality — we deliberately do NOT basename-match,
    which would over-count common names like utils.py and inflate the score.
    """
    pred, gt = _norm_path(pred), _norm_path(gt)
    if not pred or not gt:
        return False
    if pred == gt:
        return True
    if "/" in pred and gt.endswith("/" + pred):
        return True
    if "/" in gt and pred.endswith("/" + gt):
        return True
    return False


def file_hits(preds: list[str], truth: set[str], k: int) -> tuple[bool, bool, int]:
    """Return (hit_any, hit_all, n_hit) for the top-k predictions vs ground truth."""
    topk = preds[:k]
    matched = {g for g in truth if any(path_match(p, g) for p in topk)}
    n = len(matched)
    return (n > 0, n == len(truth) and len(truth) > 0, n)


# --------------------------------------------------------------------------- #
# Model call (lazy / pluggable)                                               #
# --------------------------------------------------------------------------- #
def make_model_caller(model: str) -> Callable[[str], str]:
    """Return a function prompt->raw_text using the Anthropic SDK (lazy import)."""
    try:
        import anthropic  # lazy: only needed for real runs
    except ModuleNotFoundError as e:  # pragma: no cover - env-dependent
        raise SystemExit(
            "The 'anthropic' package is not installed. `pip install anthropic` "
            "(it is not currently a bench dependency), or use --self-test/--dry-run."
        ) from e
    if not os.environ.get("ANTHROPIC_API_KEY"):
        raise SystemExit("ANTHROPIC_API_KEY is not set; cannot run the real probe.")
    client = anthropic.Anthropic()

    def call(prompt: str) -> str:
        msg = client.messages.create(
            model=model,
            max_tokens=512,
            temperature=0,
            messages=[{"role": "user", "content": prompt}],
        )
        return "".join(b.text for b in msg.content if getattr(b, "type", "") == "text")

    return call


def parse_paths(raw: str) -> list[str]:
    """Parse the model's JSON array of paths, tolerant of code fences / stray prose."""
    m = re.search(r"\[.*\]", raw, re.DOTALL)
    if not m:
        return []
    try:
        arr = json.loads(m.group(0))
    except json.JSONDecodeError:
        return []
    return [_norm_path(str(x)) for x in arr if str(x).strip()]


# --------------------------------------------------------------------------- #
# Probe                                                                        #
# --------------------------------------------------------------------------- #
def repo_popularity() -> dict[str, int]:
    if not REPOS_JSON.exists():
        return {}
    d = json.loads(REPOS_JSON.read_text(encoding="utf-8"))
    return {r["repo"]: r["instance_count"] for r in d.get("repos_by_instance_count", [])}


def run_probe(
    instances: list[dict[str, Any]],
    call: Callable[[str], str],
    k: int,
    shuffle_control: bool,
    seed: int = 0,
) -> dict[str, Any]:
    rng = random.Random(seed)
    pop = repo_popularity()
    pop_median = sorted(pop.values())[len(pop) // 2] if pop else 0

    per: list[dict[str, Any]] = []
    for inst in instances:
        truth = gt_files(inst.get("edit_functions"))
        if not truth:
            continue
        prompt = PROMPT_TEMPLATE.format(repo=inst.get("repo", "?"), problem=inst["problem_statement"], k=k)
        preds = parse_paths(call(prompt))
        any_hit, all_hit, n_hit = file_hits(preds, truth, k)
        per.append(
            {
                "instance_id": inst.get("instance_id"),
                "repo": inst.get("repo"),
                "truth": sorted(truth),
                "preds": preds[:k],
                "n_truth": len(truth),
                "n_pred": len(preds),
                "hit_any": any_hit,
                "hit_all": all_hit,
                "n_hit": n_hit,
                "popular": pop.get(inst.get("repo", ""), 0) >= max(pop_median, 1),
            }
        )

    # Shuffled-GT base-rate: score each instance's predictions against a RANDOM
    # other instance's ground truth. (matched - shuffled) strips the base-rate of
    # guessing common paths and is the clean memorization-free localization signal.
    shuffle_rate = None
    if shuffle_control and per:
        alt = [set(r["truth"]) for r in per]
        rng.shuffle(alt)
        shuffle_rate = round(
            sum(1 for r, t in zip(per, alt) if file_hits(r["preds"], t, k)[1]) / len(per), 4
        )

    return summarize(per, k, shuffle_rate)


def _rate(rows: list[dict[str, Any]], key: str) -> float:
    return round(sum(1 for r in rows if r[key]) / len(rows), 4) if rows else 0.0


def summarize(per: list[dict[str, Any]], k: int, shuffle_rate: float | None = None) -> dict[str, Any]:
    pop_rows = [r for r in per if r["popular"]]
    rare_rows = [r for r in per if not r["popular"]]
    a_all = _rate(per, "hit_all")
    out: dict[str, Any] = {
        "n": len(per),
        "k": k,
        "model": DEFAULT_MODEL,
        "issue_text_only_file_acc": {  # "Arm A"
            "hit_all@k": a_all,
            "hit_any@k": _rate(per, "hit_any"),
        },
        "stratified_hit_all@k": {
            "popular_repos": {"n": len(pop_rows), "hit_all@k": _rate(pop_rows, "hit_all")},
            "rare_repos": {"n": len(rare_rows), "hit_all@k": _rate(rare_rows, "hit_all")},
            "popular_minus_rare": round(_rate(pop_rows, "hit_all") - _rate(rare_rows, "hit_all"), 4),
        },
        "full_pipeline_baseline_file_acc@10": FULL_PIPELINE_FILE_ACC10,
        "gap_baseline_minus_issue_text_only": round(FULL_PIPELINE_FILE_ACC10 - a_all, 4),
    }
    if shuffle_rate is not None:
        out["shuffle_control_hit_all@k"] = shuffle_rate
        out["signal_minus_shuffle"] = round(a_all - shuffle_rate, 4)
    out["interpretation"] = _interpret(a_all, _rate(pop_rows, "hit_all") - _rate(rare_rows, "hit_all"))
    out["per_instance"] = per
    return out


def _interpret(a_all: float, pop_minus_rare: float) -> str:
    gap = FULL_PIPELINE_FILE_ACC10 - a_all
    if a_all >= 0.6 and pop_minus_rare >= 0.15:
        verdict = "HIGH contamination concern"
        why = (
            "the model names the right files from issue text alone at a high rate AND "
            "does markedly better on popular (memorizable) repos — the memorization signature."
        )
    elif gap >= 0.3:
        verdict = "LOW contamination concern"
        why = (
            "issue-text-only accuracy is far below the full-pipeline baseline, so the graph "
            "is doing real localization work the issue text alone cannot recover."
        )
    else:
        verdict = "INCONCLUSIVE"
        why = (
            "intermediate; inspect the popular-vs-rare and (if available) pre-vs-post-cutoff "
            "strata, and widen n before drawing a conclusion."
        )
    return (
        f"{verdict}: issue-text-only hit_all@k={a_all:.3f} vs full-pipeline "
        f"baseline={FULL_PIPELINE_FILE_ACC10:.3f} (gap {gap:+.3f}); "
        f"popular-minus-rare={pop_minus_rare:+.3f}. {why}"
    )


# --------------------------------------------------------------------------- #
# Self-test (offline, no deps)                                                #
# --------------------------------------------------------------------------- #
def self_test() -> int:
    # path matching
    assert path_match("src/a/b.py", "src/a/b.py")
    assert path_match("a/b.py", "src/a/b.py")  # model dropped the repo prefix
    assert path_match("repo/src/a/b.py", "src/a/b.py")
    assert not path_match("src/a/b.py", "src/a/c.py")
    assert not path_match("utils.py", "src/totally/other/utils.py")  # bare basename: NO match
    assert path_match("other/utils.py", "src/other/utils.py")  # multi-component suffix: match
    assert not path_match("foo/utils.py", "bar/utils.py")  # different dirs, no suffix rel

    # gt extraction
    assert gt_files(["src/x.py:Cls.m", "src/y.py:fn"]) == {"src/x.py", "src/y.py"}

    # parsing tolerant of fences/prose
    assert parse_paths('```json\n["a/b.py", "c/d.py"]\n```') == ["a/b.py", "c/d.py"]
    assert parse_paths("here you go: [\"x.py\"] thanks") == ["x.py"]
    assert parse_paths("no array here") == []

    # file_hits semantics
    any_h, all_h, n = file_hits(["src/x.py", "q.py"], {"src/x.py", "src/y.py"}, 10)
    assert any_h and not all_h and n == 1
    any_h, all_h, n = file_hits(["a/x.py", "b/y.py"], {"src/a/x.py", "src/b/y.py"}, 10)
    assert any_h and all_h and n == 2
    # top-k cutoff respected
    any_h, all_h, n = file_hits(["a.py", "b.py", "src/x.py"], {"src/x.py"}, 2)
    assert not any_h

    # end-to-end with a stub model: a perfect oracle vs a useless one
    insts = [
        {"instance_id": "i1", "repo": "django/django", "problem_statement": "p1", "edit_functions": ["a/x.py:f"]},
        {"instance_id": "i2", "repo": "tiny/rare", "problem_statement": "p2", "edit_functions": ["b/y.py:g"]},
    ]
    oracle = lambda prompt: json.dumps(["a/x.py"]) if "p1" in prompt else json.dumps(["b/y.py"])
    res = run_probe(insts, oracle, k=10, shuffle_control=False)
    assert res["issue_text_only_file_acc"]["hit_all@k"] == 1.0, res
    useless = lambda prompt: json.dumps(["zzz/nope.py"])
    res2 = run_probe(insts, useless, k=10, shuffle_control=False)
    assert res2["issue_text_only_file_acc"]["hit_all@k"] == 0.0, res2
    assert "LOW contamination concern" in res2["interpretation"], res2["interpretation"]

    # shuffle-control path produces a float base-rate and a signal delta
    res3 = run_probe(insts, oracle, k=10, shuffle_control=True)
    assert isinstance(res3["shuffle_control_hit_all@k"], float), res3
    assert "signal_minus_shuffle" in res3, res3

    print("self-test: OK (path-match, gt-extract, parse, file-hits, shuffle, interpretation)")
    return 0


# --------------------------------------------------------------------------- #
# CLI                                                                          #
# --------------------------------------------------------------------------- #
def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--instances", type=Path, default=DEFAULT_PARQUET, help="parquet/json/jsonl with problem_statement+edit_functions")
    ap.add_argument("--sample", type=int, default=0, help="random N instances (0 = all)")
    ap.add_argument("--k", type=int, default=10, help="top-k file predictions to score (Acc@k)")
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--seed", type=int, default=0)
    ap.add_argument("--shuffle-control", action="store_true", help="record a shuffled-GT base-rate control")
    ap.add_argument("--dry-run", action="store_true", help="build + print one prompt, no API calls")
    ap.add_argument("--self-test", action="store_true", help="offline correctness check, no deps/key")
    ap.add_argument("--out", type=Path, help="write the JSON report here")
    args = ap.parse_args(argv)

    if args.self_test:
        return self_test()

    instances = load_instances(args.instances)
    if args.sample and args.sample < len(instances):
        instances = random.Random(args.seed).sample(instances, args.sample)

    if args.dry_run:
        inst = instances[0]
        print(PROMPT_TEMPLATE.format(repo=inst.get("repo"), problem=inst["problem_statement"][:1200], k=args.k))
        print(f"\n[dry-run] ground-truth files: {sorted(gt_files(inst.get('edit_functions')))}")
        print(f"[dry-run] would run {len(instances)} instance(s) against {args.model}")
        return 0

    call = make_model_caller(args.model)
    report = run_probe(instances, call, k=args.k, shuffle_control=args.shuffle_control, seed=args.seed)
    print(json.dumps({k: v for k, v in report.items() if k != "per_instance"}, indent=2))
    if args.out:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(report, indent=2), encoding="utf-8")
        print(f"\nwrote {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
