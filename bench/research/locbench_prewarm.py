"""Pre-warm Loc-Bench instance caches (clone + index) without running the eval.

The eval (`eval_locbench_compare.py`) is index-aware - if the SQLite DB
for an instance already exists at the canonical cache path, it skips
clone+index and goes straight to the LLM-driven localizer. But the FIRST
run on a new instance set has to clone+index every instance from scratch,
which is the I/O-bound bulk of total wall-time (clone+index ~= 95% per
instance on cold cache; LLM ~= 5%).

This script separates the I/O-bound pre-warm from the compute+LLM-bound
eval so they can run in different sessions:

  - Pre-warm overnight: `python bench/research/locbench_prewarm.py --n 200 --workers 8 > /tmp/prewarm.log 2>&1`
  - Eval next session: `python bench/research/eval_locbench_compare.py --n 200 --workers 4 ...`

The eval picks up the cached clones + SQLite DBs and runs in cache-hit
mode, dropping per-instance wall-time from ~22 min to LLM-dominant
(~1-2 min per instance at iter=2).

Reuses `clone_repo`, `index_repo`, `db_path_for`, `repo_size_mb`,
`select_instances`, and the parquet location from eval_locbench_compare.py
so behavior matches the eval's clone+index path exactly (same git
strategy, same index binary invocation, same cache directory keying).

Usage:
    python bench/research/locbench_prewarm.py --n 200 --workers 8
    python bench/research/locbench_prewarm.py --instances <id1>,<id2>,...
    python bench/research/locbench_prewarm.py --categories "Bug Report,Feature Request" --n 100

Per-instance status reports:
    cache-hit  : DB exists, no work needed
    cloned     : repo cloned but DB not present (caller skipped index or it failed)
    indexed    : new clone + new DB
    too-large  : repo size > --max-mb, skipped
    failed     : clone or index errored (note in status)

Plan: knowledge-base PR #489 plans/2026-05-10-reduce-recurring-plan-cycle-friction.md - Phase C.
"""
from __future__ import annotations

import argparse
import sys
import time
from concurrent.futures import ProcessPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import pandas as pd

# Reuse eval helpers so clone+index behavior matches the eval exactly.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from eval_locbench_compare import (  # noqa: E402
    PARQUET,
    DEFAULT_INDEX_BIN,
    clone_repo,
    db_path_for,
    index_repo,
    repo_size_mb,
    select_instances,
)


@dataclass
class PrewarmResult:
    instance_id: str
    repo: str
    base_commit: str
    cache_hit: bool = False
    cloned: bool = False
    indexed: bool = False
    too_large: bool = False
    repo_size_mb: float = 0.0
    note: str = ""
    duration_s: float = 0.0


def prewarm_instance(
    row: dict[str, Any],
    workdir: Path,
    index_bin: Path,
    max_mb: float,
) -> PrewarmResult:
    t0 = time.time()
    iid = row["instance_id"]
    repo = row["repo"]
    base_commit = row["base_commit"]
    res = PrewarmResult(instance_id=iid, repo=repo, base_commit=base_commit)

    repo_dir = workdir / iid
    db = db_path_for(repo_dir)

    # Fast path: DB cached
    if db.exists():
        res.cache_hit = True
        res.indexed = True
        res.duration_s = time.time() - t0
        res.note = "cache-hit"
        return res

    # Clone (skip if dir already exists from a prior interrupted run)
    if not repo_dir.exists():
        if not clone_repo(repo, base_commit, repo_dir):
            res.note = "clone failed"
            res.duration_s = time.time() - t0
            return res
    res.cloned = True

    # Size check
    res.repo_size_mb = repo_size_mb(repo_dir)
    if max_mb > 0 and res.repo_size_mb > max_mb:
        res.too_large = True
        res.note = f"too large ({res.repo_size_mb:.0f} MB > {max_mb:.0f})"
        res.duration_s = time.time() - t0
        return res

    # Index
    if not index_repo(repo_dir, index_bin):
        res.note = "index failed"
        res.duration_s = time.time() - t0
        return res
    res.indexed = True
    res.duration_s = time.time() - t0
    res.note = "indexed"
    return res


def _prewarm_one_worker(
    row: dict[str, Any],
    workdir_str: str,
    index_bin_str: str,
    max_mb: float,
) -> PrewarmResult:
    """Worker entrypoint for ProcessPoolExecutor. Pickle-friendly args."""
    workdir = Path(workdir_str)
    workdir.mkdir(parents=True, exist_ok=True)
    return prewarm_instance(row, workdir, Path(index_bin_str), max_mb)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--instances", help="Comma-separated instance IDs (overrides --n)")
    ap.add_argument("--n", type=int, default=200)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--categories", help="Comma-separated category names to filter")
    ap.add_argument("--max-mb", type=float, default=1000, help="Repo size cap (0 = none)")
    ap.add_argument("--workdir", type=Path, default=Path(r"C:/tmp/locbench-batch"))
    ap.add_argument("--index-bin", type=Path, default=DEFAULT_INDEX_BIN)
    ap.add_argument(
        "--workers",
        type=int,
        default=4,
        help="Parallel worker count (1 = sequential). Each instance gets its own subdir; concurrent clones don't collide.",
    )
    args = ap.parse_args()

    if not args.index_bin.exists():
        print(f"FATAL: index binary missing: {args.index_bin}", file=sys.stderr)
        return 2

    df = pd.read_parquet(PARQUET)
    explicit = [s.strip() for s in args.instances.split(",")] if args.instances else None
    cats = [c.strip() for c in args.categories.split(",")] if args.categories else None
    selected = select_instances(df, args.n, args.seed, explicit, cats)
    print(f"Loc-Bench instances: {len(df)}; selected {len(selected)} for pre-warm")

    args.workdir.mkdir(parents=True, exist_ok=True)
    rows = [row.to_dict() for _, row in selected.iterrows()]
    order = {row["instance_id"]: i for i, row in enumerate(rows)}
    results: list[PrewarmResult] = []

    t_start = time.time()

    if args.workers <= 1:
        for row in rows:
            try:
                res = prewarm_instance(row, args.workdir, args.index_bin, args.max_mb)
            except KeyboardInterrupt:
                print("\n!!! interrupted", file=sys.stderr)
                break
            except Exception as e:  # noqa: BLE001
                res = PrewarmResult(
                    instance_id=row["instance_id"],
                    repo=row.get("repo", ""),
                    base_commit=row.get("base_commit", ""),
                    note=f"exception: {e!r}",
                )
            results.append(res)
            _print_status(res)
    else:
        print(f"Running with {args.workers} parallel workers...")
        worker_args = [
            (row, str(args.workdir), str(args.index_bin), args.max_mb)
            for row in rows
        ]
        with ProcessPoolExecutor(max_workers=args.workers) as ex:
            futures = {ex.submit(_prewarm_one_worker, *a): a[0]["instance_id"] for a in worker_args}
            for fut in as_completed(futures):
                iid = futures[fut]
                try:
                    res = fut.result()
                except Exception as e:  # noqa: BLE001
                    res = PrewarmResult(
                        instance_id=iid, repo="", base_commit="",
                        note=f"worker exception: {e!r}",
                    )
                results.append(res)
                _print_status(res)
        results.sort(key=lambda r: order.get(r.instance_id, 9999))

    elapsed = time.time() - t_start

    # Summary
    cache_hits = sum(1 for r in results if r.cache_hit)
    indexed = sum(1 for r in results if r.indexed and not r.cache_hit)
    cloned_only = sum(1 for r in results if r.cloned and not r.indexed)
    too_large = sum(1 for r in results if r.too_large)
    failed = sum(1 for r in results if not r.indexed and not r.too_large)

    print(f"\n=== Pre-warm summary ===")
    print(f"  Total instances:  {len(results)}")
    print(f"  Cache hits:       {cache_hits}")
    print(f"  Newly indexed:    {indexed}")
    print(f"  Cloned but no DB: {cloned_only}")
    print(f"  Too large:        {too_large}")
    print(f"  Failed:           {failed}")
    print(f"  Elapsed:          {elapsed:.0f}s ({elapsed/60:.1f}m)")

    return 0 if failed == 0 else 1


def _print_status(r: PrewarmResult) -> None:
    status = (
        "cache-hit" if r.cache_hit
        else "indexed" if r.indexed
        else "too-large" if r.too_large
        else "FAIL"
    )
    size_str = f"{r.repo_size_mb:.0f}MB " if r.repo_size_mb else ""
    print(f"  [{status:10s}] {r.instance_id:50s} {size_str}({r.duration_s:.0f}s) {r.note}")


if __name__ == "__main__":
    sys.exit(main())
