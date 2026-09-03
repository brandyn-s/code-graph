"""Run a single Loc-Bench issue end-to-end against our code_localize tool.

Issue: pypa__pip-13085 (Lazy import allows wheel to execute code on install)
Ground truth: src/pip/_internal/commands/install.py:InstallCommand.run

This is a one-instance proof of concept that the Loc-Bench harness CAN be wired
up. NOT a benchmark — n=1 doesn't generalize.
"""
from __future__ import annotations

import pandas as pd
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(r"C:/Users/user/Documents/GitHub/code-graph")
PARQUET = REPO_ROOT / "bench/research/locbench.parquet"
EVAL_BIN = REPO_ROOT / "bench/research/eval_rank_localize/eval.exe"
PIP_DB = Path(r"C:/Users/user/.cache/code-graph/c-Users-user-tmp-pip-locbench.db")


def main() -> int:
    df = pd.read_parquet(PARQUET)
    row = df[df["instance_id"] == "pypa__pip-13085"].iloc[0]
    problem = row["problem_statement"]
    ground_truth = list(row["edit_functions"])

    print(f"=== Loc-Bench instance: {row['instance_id']} ===")
    print(f"Repo: {row['repo']}")
    print(f"Category: {row['category']}")
    print(f"Ground truth ({len(ground_truth)} function(s)):")
    for fn in ground_truth:
        print(f"  - {fn}")
    print(f"\nProblem statement (first 400 chars):")
    print(problem[:400])
    print()

    # Use only the description's first paragraph as the localization query —
    # full multi-paragraph issue may dilute seed matching.
    short_query = problem.split("\n\n")[0].strip()
    print(f"=== Query passed to code_localize (first paragraph): ===")
    print(short_query[:300])
    print()

    # Invoke our eval binary.
    cmd = [str(EVAL_BIN), "-top-k", "20", "-depth", "3", str(PIP_DB), short_query]
    print(f"Running: {' '.join(cmd)}\n")
    # Capture as bytes + UTF-8 decode (text=True uses cp1252 on Windows
    # and crashes on non-cp1252 bytes — PR #97 fix).
    result = subprocess.run(cmd, capture_output=True, timeout=120)
    output = result.stdout.decode("utf-8", errors="replace")
    print(output)
    if result.returncode != 0:
        err = result.stderr.decode("utf-8", errors="replace")
        print("STDERR:", err, file=sys.stderr)
        return 1

    # Score: did any ground-truth function appear in top-K?
    print("=== Scoring ===")
    for gt in ground_truth:
        # gt format: "src/pip/_internal/commands/install.py:InstallCommand.run"
        file_part, func_part = gt.split(":", 1)
        # Look for either the file or function name in the eval output.
        file_match = file_part in output
        # function name segment for matching (last component)
        func_simple = func_part.split(".")[-1]  # "run"
        func_class = func_part.split(".")[0] if "." in func_part else func_part
        func_match = func_class in output  # "InstallCommand"
        print(f"  Ground truth: {gt}")
        print(f"    File path '{file_part}' in output: {file_match}")
        print(f"    Class name '{func_class}' in output: {func_match}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
