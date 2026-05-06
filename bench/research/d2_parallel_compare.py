"""Plan 4 D2 falsifier: serial vs parallel iter=2 wall-time comparison.

Runs eval_parallel.exe with LOCAGENT_PARALLEL=0 then =1 against the
locally-indexed code-graph project, on a small set of representative
queries. Reports wall-time delta and entity-overlap (sanity check for
"same protocol, different scheduling").

Falsifier (META_SYNTHESIS D2): "≥30% P95 wall-time reduction with ≤1pp
Acc@10 regression → top-3". This script captures wall-time. Accuracy
delta requires Loc-Bench ground truth + scoring; this is the sub-test.

Usage:
    export ANTHROPIC_API_KEY=sk-...
    python bench/research/d2_parallel_compare.py
"""
from __future__ import annotations

import json
import os
import statistics
import subprocess
import sys
import time
from pathlib import Path

# Hand-picked queries against the indexed code-graph project.
# Each is something the agent CAN realistically resolve, so we measure
# work-time not error-time. These map to known files in code-graph.
QUERIES = [
    "the Cypher parser fails to recognize ENDS WITH after a property reference",
    "MCP tool invocations need a metadata block with freshness and provenance",
    "the indexer runs slow on large repos; want parallel pipeline phases",
]

DB = Path.home() / ".cache" / "codebase-memory-mcp" / "c-Users-user-Documents-GitHub-code-graph.db"
# Resolve relative to the repo root (parent-of-parent of this script).
REPO_ROOT = Path(__file__).resolve().parents[2]
EVAL = REPO_ROOT / "eval_parallel.exe"


def to_windows(p: Path) -> str:
    s = str(p).replace("\\", "/")
    if len(s) >= 3 and s[0] == "/" and s[2] == "/" and s[1].isalpha():
        return s[1].upper() + ":" + s[2:]
    return s


def run_one(query: str, parallel: bool) -> tuple[float, dict]:
    env = os.environ.copy()
    env["LOCAGENT_PARALLEL"] = "1" if parallel else "0"
    env["LOCAGENT_ITERATIONS"] = "2"
    cmd = [
        str(EVAL),
        "-top-k", "10",
        "-agent",
        "-seed-strategy", "hybrid",
        "-json",
        to_windows(DB),
        query,
    ]
    t0 = time.time()
    proc = subprocess.run(cmd, capture_output=True, env=env, timeout=300)
    dur = time.time() - t0
    if proc.returncode != 0:
        return dur, {"error": proc.stderr.decode("utf-8", errors="replace")[:300]}
    out = proc.stdout.decode("utf-8", errors="replace")
    try:
        envelope = json.loads(out)
    except json.JSONDecodeError:
        return dur, {"error": "json decode failed", "stdout_head": out[:300]}
    return dur, envelope


def entity_paths(env: dict) -> set[str]:
    agent = env.get("code_localize_agent") or {}
    return {
        e.get("file_path", "")
        for e in agent.get("entities", [])
        if isinstance(e, dict)
    }


def main() -> int:
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")
    if not EVAL.exists():
        print(f"ERROR: {EVAL} not found; build with go build first", file=sys.stderr)
        return 2
    if not DB.exists():
        print(f"ERROR: indexed DB not found at {DB}", file=sys.stderr)
        return 2
    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("ERROR: ANTHROPIC_API_KEY required", file=sys.stderr)
        return 2

    serial_times: list[float] = []
    parallel_times: list[float] = []
    overlap_pcts: list[float] = []

    for i, q in enumerate(QUERIES, 1):
        print(f"\n=== Query {i}/{len(QUERIES)}: {q[:60]}... ===")

        # Serial first.
        print(f"  serial...   ", end="", flush=True)
        ds, env_s = run_one(q, parallel=False)
        if "error" in env_s:
            print(f"ERROR: {env_s['error'][:100]}")
            continue
        print(f"{ds:.1f}s")

        # Then parallel.
        print(f"  parallel... ", end="", flush=True)
        dp, env_p = run_one(q, parallel=True)
        if "error" in env_p:
            print(f"ERROR: {env_p['error'][:100]}")
            continue
        print(f"{dp:.1f}s")

        sp = entity_paths(env_s)
        pp = entity_paths(env_p)
        overlap = len(sp & pp) / max(1, len(sp | pp)) * 100.0
        speedup = ds / dp if dp > 0 else 0.0

        print(f"  speedup: {speedup:.2f}x; entity-set Jaccard: {overlap:.0f}%")

        serial_times.append(ds)
        parallel_times.append(dp)
        overlap_pcts.append(overlap)

    if not serial_times:
        print("\nNo successful runs; cannot compute summary", file=sys.stderr)
        return 1

    print("\n=== D2 Falsifier Comparison ===")
    print(f"N queries:        {len(serial_times)}")
    print(f"serial mean:      {statistics.mean(serial_times):.2f}s")
    print(f"parallel mean:    {statistics.mean(parallel_times):.2f}s")
    if len(serial_times) >= 2:
        print(f"serial median:    {statistics.median(serial_times):.2f}s")
        print(f"parallel median:  {statistics.median(parallel_times):.2f}s")
    speedup = statistics.mean(serial_times) / statistics.mean(parallel_times)
    reduction_pct = (1 - 1 / speedup) * 100
    print(f"mean speedup:     {speedup:.2f}x ({reduction_pct:.1f}% wall reduction)")
    print(f"entity overlap:   {statistics.mean(overlap_pcts):.0f}% (Jaccard)")

    print("\n=== Falsifier verdict ===")
    if reduction_pct >= 30 and statistics.mean(overlap_pcts) >= 50:
        print(f"PASSED: {reduction_pct:.1f}% reduction >= 30% threshold; entity overlap "
              f"{statistics.mean(overlap_pcts):.0f}% suggests same protocol behavior.")
        print("D2 verdict: parallel iter=2 deserves top-3 priority for production rollout.")
        return 0
    if reduction_pct < 20:
        print(f"FAILED: only {reduction_pct:.1f}% reduction (<20% threshold).")
        print("D2 verdict: parallel iter=2 is secondary, not top-3.")
        return 0
    print(f"AMBIGUOUS: {reduction_pct:.1f}% reduction in [20%, 30%]. Run larger n.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
