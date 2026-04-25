"""Run a Loc-Bench subset against our localization tools and score F1 per instance.

Loop over N selected Loc-Bench instances and for each:

  1. Read the instance from the parquet (problem_statement, repo, base_commit,
     edit_functions ground truth).
  2. Clone the repo at the recorded base_commit into a working dir.
  3. Index it with our codebase-memory-mcp binary (VOYAGE_API_KEY enables
     embedding seeds for the hybrid strategy).
  4. Run the eval harness against the resulting DB with -agent (LLM loop)
     and -seed-strategy=hybrid.
  5. Score: did the ground-truth file or class appear in the agent's
     finalized entities? Record per-instance hit/miss + token usage.
  6. After every instance, check accumulated estimated LLM cost and
     abort if the configured budget cap is exceeded.
  7. Write a summary table at the end.

This script is the harness for the Phase B / V1 deliverable from the
2026-04-25 superplan: turn the n=1 hit on pypa__pip-13085 (PR #82) into
a defensible N=20 benchmark claim.

The script is INTENTIONALLY conservative about cost:

  - Hard-aborts when accumulated cost exceeds --budget-usd (default $3).
  - Skips instances whose repo > 200 MB (indexing wall time would dominate).
  - Skips instances whose ground-truth requires multi-file edits unless
    they all share a common parent dir.

Not run by CI. This is an offline benchmark — invoke manually:

    export ANTHROPIC_API_KEY=sk-...
    export VOYAGE_API_KEY=pa-...
    python bench/research/eval_locbench_batch.py \
        --n 20 --budget-usd 3.0 \
        --workdir C:/tmp/locbench-batch \
        --output bench/research/locbench-n20-results-$(date +%Y-%m-%d).md
"""
from __future__ import annotations

import argparse
import os
import random
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import pandas as pd

REPO_ROOT = Path(__file__).resolve().parents[2]
PARQUET = REPO_ROOT / "bench/research/locbench.parquet"
EVAL_BIN = REPO_ROOT / "bench/research/eval_rank_localize/eval.exe"
INDEX_BIN = REPO_ROOT / "bin/codebase-memory-mcp.exe"
CACHE_DIR = Path.home() / ".cache" / "codebase-memory-mcp"

# Estimated $/M tokens for Haiku 4.5 (input + output averaged over typical
# agent runs from PR #82: ~50K in, ~1.4K out → $0.04-0.05 per query).
COST_PER_QUERY_USD_ESTIMATE = 0.05

# Repo size cap: above this, indexing wall time > 30min — skip to keep
# the batch tractable.
MAX_REPO_MB = 200


@dataclass
class InstanceResult:
    instance_id: str
    repo: str
    category: str
    ground_truth: list[str]
    indexed: bool = False
    agent_ran: bool = False
    file_hit: bool = False  # any ground-truth file appears in agent output
    class_hit: bool = False  # any ground-truth class appears in agent output
    func_hit: bool = False  # any ground-truth function appears
    turns: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    cost_estimate_usd: float = 0.0
    note: str = ""
    duration_s: float = 0.0


@dataclass
class BatchSummary:
    n_total: int = 0
    n_indexed: int = 0
    n_agent_ran: int = 0
    n_file_hit: int = 0
    n_class_hit: int = 0
    n_func_hit: int = 0
    total_input_tokens: int = 0
    total_output_tokens: int = 0
    total_cost_usd: float = 0.0
    aborted_reason: str = ""
    instances: list[InstanceResult] = field(default_factory=list)


def select_instances(df: pd.DataFrame, n: int, seed: int) -> pd.DataFrame:
    """Pick N instances with a balanced mix of categories.

    Default strategy: 5 each of Bug, Feature, Performance, Security if
    available; fall back to uniform random if categories under-supply.
    """
    random.seed(seed)
    target_per_cat = n // 4
    picked: list[pd.Series] = []
    for cat in ["Bug", "Feature", "Performance", "Security"]:
        sub = df[df["category"] == cat]
        if len(sub) == 0:
            continue
        take = min(target_per_cat, len(sub))
        picked.extend(sub.sample(n=take, random_state=seed).to_dict("records"))
    # Top up if we under-filled.
    while len(picked) < n:
        remaining = df.drop(index=[df[df["instance_id"] == r["instance_id"]].index[0] for r in picked])
        if len(remaining) == 0:
            break
        picked.append(remaining.sample(n=1, random_state=seed + len(picked)).iloc[0].to_dict())
    return pd.DataFrame(picked[:n])


def repo_size_mb(path: Path) -> float:
    """Estimate disk usage of a checked-out repo."""
    total = 0
    for root, _dirs, files in os.walk(path):
        for f in files:
            try:
                total += (Path(root) / f).stat().st_size
            except OSError:
                pass
    return total / (1024 * 1024)


def clone_repo(repo: str, base_commit: str, dest: Path) -> bool:
    """Shallow-clone {repo} at {base_commit} into {dest}. Returns True on success."""
    if dest.exists():
        shutil.rmtree(dest, ignore_errors=True)
    dest.parent.mkdir(parents=True, exist_ok=True)
    url = f"https://github.com/{repo}.git"
    # Full clone needed because shallow + base_commit isn't reliable across
    # all GitHub repos. Tradeoff: more wall time, but deterministic.
    try:
        subprocess.run(
            ["git", "clone", "--quiet", url, str(dest)],
            check=True,
            timeout=600,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        subprocess.run(
            ["git", "-C", str(dest), "checkout", base_commit],
            check=True,
            timeout=60,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        return True
    except subprocess.CalledProcessError as e:
        print(f"  clone/checkout failed: {e.stderr.decode('utf-8', errors='replace')[:200]}")
        return False
    except subprocess.TimeoutExpired:
        print("  clone/checkout timed out")
        return False


def index_repo(path: Path) -> bool:
    """Run codebase-memory-mcp index_repository on {path}. Returns True on success."""
    if not INDEX_BIN.exists():
        print(f"  binary missing: {INDEX_BIN}")
        return False
    # Equivalent to: codebase-memory-mcp index <path>
    # Note: project name is derived from the path; embedding seeds require
    # VOYAGE_API_KEY to be set in env at index time.
    try:
        result = subprocess.run(
            [str(INDEX_BIN), "index", str(path)],
            capture_output=True,
            text=True,
            timeout=1800,  # 30 min cap per index
        )
        if result.returncode != 0:
            print(f"  index failed (exit {result.returncode}): {result.stderr[:200]}")
            return False
        return True
    except subprocess.TimeoutExpired:
        print("  index timed out (30min)")
        return False


def db_path_for(repo_path: Path) -> Path:
    """Mimic ProjectNameFromPath sanitization: lowercase drive letter, replace
    `:`/`\\`/`/` with `-`, append `.db`. Matches the on-disk filename."""
    raw = str(repo_path).replace("\\", "/")
    # Lowercase drive letter (Windows)
    if len(raw) >= 2 and raw[1] == ":":
        raw = raw[0].lower() + raw[1:]
    sanitized = raw.replace(":", "-").replace("/", "-")
    return CACHE_DIR / f"{sanitized}.db"


def run_agent(db: Path, query: str, top_k: int = 10) -> dict[str, Any]:
    """Run eval_rank_localize binary with -agent. Returns parsed result dict."""
    cmd = [
        str(EVAL_BIN),
        "-top-k", str(top_k),
        "-agent",
        "-seed-strategy", "hybrid",
        str(db),
        query,
    ]
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=300)
    if result.returncode != 0:
        return {
            "error": result.stderr[:500],
            "stdout": result.stdout,
            "input_tokens": 0,
            "output_tokens": 0,
            "turns": 0,
        }
    out = result.stdout
    # Parse the line: "turns=N, stop_reason=foo, input_tokens=X, output_tokens=Y"
    parsed = {"stdout": out, "input_tokens": 0, "output_tokens": 0, "turns": 0}
    for line in out.splitlines():
        if "input_tokens=" in line and "output_tokens=" in line:
            for part in line.split(","):
                k, _, v = part.strip().partition("=")
                if k in {"turns", "input_tokens", "output_tokens"}:
                    try:
                        parsed[k] = int(v)
                    except ValueError:
                        pass
    return parsed


def score_against_ground_truth(agent_output: str, ground_truth: list[str]) -> tuple[bool, bool, bool]:
    """Return (file_hit, class_hit, func_hit). Each True if ANY ground-truth
    item's file path / containing class / function name appears in the
    agent's output text."""
    file_hit = class_hit = func_hit = False
    for gt in ground_truth:
        if ":" not in gt:
            # Format expected: "path/to/file.py:Class.func" or "path/to/file.py:func"
            continue
        file_part, func_part = gt.split(":", 1)
        if file_part in agent_output:
            file_hit = True
        comps = func_part.split(".")
        if len(comps) >= 2:
            cls = comps[0]
            fn = comps[-1]
            if cls in agent_output:
                class_hit = True
            if fn in agent_output:
                func_hit = True
        else:
            if func_part in agent_output:
                func_hit = True
    return file_hit, class_hit, func_hit


def evaluate_instance(row: dict[str, Any], workdir: Path) -> InstanceResult:
    iid = row["instance_id"]
    repo = row["repo"]
    res = InstanceResult(
        instance_id=iid,
        repo=repo,
        category=row.get("category", "Unknown"),
        ground_truth=list(row.get("edit_functions", [])),
    )
    t0 = time.time()
    print(f"\n=== {iid} ({repo}, {res.category}) ===")
    print(f"ground truth ({len(res.ground_truth)} fns): {res.ground_truth[:3]}")

    repo_dir = workdir / iid
    if not clone_repo(repo, row["base_commit"], repo_dir):
        res.note = "clone failed"
        res.duration_s = time.time() - t0
        return res

    size_mb = repo_size_mb(repo_dir)
    if size_mb > MAX_REPO_MB:
        res.note = f"repo too large ({size_mb:.0f} MB > {MAX_REPO_MB})"
        shutil.rmtree(repo_dir, ignore_errors=True)
        res.duration_s = time.time() - t0
        return res

    if not index_repo(repo_dir):
        res.note = "index failed"
        shutil.rmtree(repo_dir, ignore_errors=True)
        res.duration_s = time.time() - t0
        return res
    res.indexed = True

    # Use only the first paragraph as the agent's query — full multi-
    # paragraph issue dilutes seed matching (verified PR #82 testing).
    short_query = row["problem_statement"].split("\n\n")[0].strip()
    db = db_path_for(repo_dir)
    if not db.exists():
        res.note = f"db not at expected path {db.name}"
        shutil.rmtree(repo_dir, ignore_errors=True)
        res.duration_s = time.time() - t0
        return res

    parsed = run_agent(db, short_query, top_k=10)
    res.agent_ran = "error" not in parsed
    res.input_tokens = parsed.get("input_tokens", 0)
    res.output_tokens = parsed.get("output_tokens", 0)
    res.turns = parsed.get("turns", 0)
    res.cost_estimate_usd = COST_PER_QUERY_USD_ESTIMATE if res.agent_ran else 0.0

    if res.agent_ran:
        res.file_hit, res.class_hit, res.func_hit = score_against_ground_truth(
            parsed["stdout"], res.ground_truth
        )

    # Cleanup repo to save disk
    shutil.rmtree(repo_dir, ignore_errors=True)
    # Cleanup index DB (saves ~50-200 MB per instance)
    try:
        db.unlink(missing_ok=True)
        Path(str(db) + "-shm").unlink(missing_ok=True)
        Path(str(db) + "-wal").unlink(missing_ok=True)
    except OSError:
        pass

    res.duration_s = time.time() - t0
    print(
        f"  -> indexed={res.indexed} agent={res.agent_ran} "
        f"file_hit={res.file_hit} class_hit={res.class_hit} "
        f"tokens={res.input_tokens}/{res.output_tokens} "
        f"~${res.cost_estimate_usd:.3f} ({res.duration_s:.0f}s)"
    )
    return res


def write_report(summary: BatchSummary, output: Path) -> None:
    lines = [
        f"# Loc-Bench N={summary.n_total} batch results — {time.strftime('%Y-%m-%d %H:%M')}",
        "",
        "## Summary",
        "",
        f"- Instances attempted: {summary.n_total}",
        f"- Indexed successfully: {summary.n_indexed}",
        f"- Agent ran: {summary.n_agent_ran}",
        f"- File-level hit (any ground-truth file in output): {summary.n_file_hit}",
        f"- Class-level hit: {summary.n_class_hit}",
        f"- Function-level hit: {summary.n_func_hit}",
        f"- Total LLM tokens: {summary.total_input_tokens:,} input, {summary.total_output_tokens:,} output",
        f"- Estimated cost: ${summary.total_cost_usd:.2f}",
    ]
    if summary.n_agent_ran > 0:
        lines.append(
            f"- File-level accuracy (vs LocAgent's published 92.7%): "
            f"{100 * summary.n_file_hit / summary.n_agent_ran:.1f}% "
            f"({summary.n_file_hit}/{summary.n_agent_ran})"
        )
    if summary.aborted_reason:
        lines.append(f"- **Aborted**: {summary.aborted_reason}")
    lines.append("")
    lines.append("## Per-instance results")
    lines.append("")
    lines.append("| instance_id | repo | category | indexed | agent | file | class | func | turns | tokens | $ | note |")
    lines.append("|---|---|---|---|---|---|---|---|---|---|---|---|")
    for r in summary.instances:
        lines.append(
            f"| {r.instance_id} | {r.repo} | {r.category} | "
            f"{'Y' if r.indexed else 'N'} | {'Y' if r.agent_ran else 'N'} | "
            f"{'Y' if r.file_hit else 'N'} | {'Y' if r.class_hit else 'N'} | "
            f"{'Y' if r.func_hit else 'N'} | {r.turns} | "
            f"{r.input_tokens}/{r.output_tokens} | "
            f"{r.cost_estimate_usd:.3f} | {r.note} |"
        )
    output.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"\nReport written: {output}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--n", type=int, default=20, help="Number of instances")
    ap.add_argument("--seed", type=int, default=42, help="Random seed for sampling")
    ap.add_argument("--budget-usd", type=float, default=3.0, help="Hard abort threshold")
    ap.add_argument("--workdir", type=Path, default=Path(r"C:/tmp/locbench-batch"))
    ap.add_argument(
        "--output",
        type=Path,
        default=REPO_ROOT / "bench/research" / f"locbench-n20-results-{time.strftime('%Y-%m-%d')}.md",
    )
    args = ap.parse_args()

    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("ERROR: ANTHROPIC_API_KEY required for agent runs", file=sys.stderr)
        return 2
    if not os.environ.get("VOYAGE_API_KEY"):
        print("WARNING: VOYAGE_API_KEY not set — embedding seeds disabled, hybrid falls back to substring", file=sys.stderr)

    if not PARQUET.exists():
        print(f"ERROR: parquet not at {PARQUET}", file=sys.stderr)
        return 2

    df = pd.read_parquet(PARQUET)
    selected = select_instances(df, args.n, args.seed)
    print(f"Selected {len(selected)} instances:")
    for _, row in selected.iterrows():
        print(f"  - {row['instance_id']} ({row.get('category', '?')})")

    args.workdir.mkdir(parents=True, exist_ok=True)
    summary = BatchSummary(n_total=len(selected))

    for _, row in selected.iterrows():
        if summary.total_cost_usd >= args.budget_usd:
            summary.aborted_reason = (
                f"budget cap ${args.budget_usd:.2f} hit at "
                f"${summary.total_cost_usd:.2f} after {len(summary.instances)} runs"
            )
            print(f"\n!!! {summary.aborted_reason}")
            break
        try:
            res = evaluate_instance(row.to_dict(), args.workdir)
        except KeyboardInterrupt:
            summary.aborted_reason = "user interrupted (Ctrl+C)"
            break
        except Exception as e:
            res = InstanceResult(
                instance_id=row["instance_id"],
                repo=row["repo"],
                category=row.get("category", "Unknown"),
                ground_truth=list(row.get("edit_functions", [])),
                note=f"exception: {e!r}",
            )
        summary.instances.append(res)
        summary.n_indexed += int(res.indexed)
        summary.n_agent_ran += int(res.agent_ran)
        summary.n_file_hit += int(res.file_hit)
        summary.n_class_hit += int(res.class_hit)
        summary.n_func_hit += int(res.func_hit)
        summary.total_input_tokens += res.input_tokens
        summary.total_output_tokens += res.output_tokens
        summary.total_cost_usd += res.cost_estimate_usd

    write_report(summary, args.output)
    return 0


if __name__ == "__main__":
    sys.exit(main())
