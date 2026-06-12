#!/usr/bin/env python3
"""Arm C of the SweRank pre-filter pilot: retrieval-only localization.

Per pinned Loc-Bench instance: clone repo@base_commit (reusing the batch
harness's clone_repo / size cap), index with code-search (Voyage embeddings),
run one hybrid search with the production reranker defaults, emit the top-15
unique files plus an entity blob, and score with the SAME substring
convention as eval_locbench_batch.score_against_ground_truth — so arm C's
hits are directly comparable to arms A/B.

Cost design (the plan's own): instances are grouped by repo — one clone and
one code-search storage per DISTINCT repo, `git checkout` per instance
commit, incremental (merkle-delta) indexing after the first instance. A
chunk-count Voyage meter (chunks_added x EST_TOKENS_PER_CHUNK x $/M) hard-
aborts when the ceiling is hit, recording remaining instances as
budget-skipped rather than silently dropping them.

Outputs a per-case JSON (checkpointed after every instance) consumed by the
arm-B parquet builder and the pilot compare step.

Run from the pilot venv (pandas); code-search work happens in subprocesses
using code-search's own venv (--cs-python).
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from eval_locbench_batch import (  # noqa: E402
    MAX_REPO_MB,
    clone_repo,
    repo_size_mb,
    score_against_ground_truth,
)

import pandas as pd  # noqa: E402

# Conservative Voyage estimate: avg tokens per code chunk x $/M tokens.
# voyage-4-large list price ~$0.12/M; we meter at $0.20/M + 400 tok/chunk
# so the ceiling errs toward overestimating spend (aborting early), never
# under. The finding doc reports metered-estimate, not actuals.
EST_TOKENS_PER_CHUNK = 400
EST_USD_PER_MTOK = 0.20


def index_with_code_search(cs_python: str, cs_root: str, repo_dir: Path,
                           storage: Path, env: dict, force_full: bool,
                           timeout: int = 2400) -> tuple[bool, str, int]:
    """Returns (ok, err, chunks_added)."""
    cmd = [cs_python, str(Path(__file__).resolve().parent / "cs_index_once.py"),
           "--code-search-root", cs_root, "--repo", str(repo_dir),
           "--storage-dir", str(storage)]
    if force_full:
        cmd.append("--force-full")
    try:
        r = subprocess.run(cmd, capture_output=True, timeout=timeout, env=env)
    except subprocess.TimeoutExpired:
        return False, "index timed out", 0
    if r.returncode != 0:
        return False, r.stderr.decode("utf-8", errors="replace")[-300:], 0
    try:
        payload = json.loads(r.stdout.decode("utf-8", errors="replace"))
        return bool(payload.get("success", True)), payload.get("error") or "", int(payload.get("chunks_added", 0))
    except (json.JSONDecodeError, ValueError):
        return True, "", 0


def search_once(cs_python: str, cs_root: str, storage: Path, query: str,
                env: dict, k: int = 50, timeout: int = 300) -> list[dict]:
    cmd = [cs_python, str(Path(__file__).resolve().parent / "cs_search_once.py"),
           "--code-search-root", cs_root, "--storage-dir", str(storage),
           "--query", query, "--k", str(k)]
    r = subprocess.run(cmd, capture_output=True, timeout=timeout, env=env)
    if r.returncode != 0:
        raise RuntimeError(f"search failed: {r.stderr.decode('utf-8', errors='replace')[-300:]}")
    return json.loads(r.stdout.decode("utf-8", errors="replace"))


def checkout(repo_dir: Path, commit: str) -> bool:
    r = subprocess.run(["git", "-C", str(repo_dir), "checkout", "--quiet", commit],
                       capture_output=True, timeout=120)
    return r.returncode == 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--pin", required=True, type=Path)
    ap.add_argument("--parquet", required=True, type=Path)
    ap.add_argument("--workdir", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument("--cs-root", default=str(Path.home() / "Documents/GitHub/code-search"))
    ap.add_argument("--cs-python", default=str(Path.home() / "Documents/GitHub/code-search/.venv/bin/python"))
    ap.add_argument("--top-files", type=int, default=15)
    ap.add_argument("--voyage-ceiling-usd", type=float, default=12.0)
    args = ap.parse_args()

    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(line_buffering=True)

    pin = json.loads(args.pin.read_text(encoding="utf-8"))
    ids = set(pin.get("pinned_instance_ids", pin) if isinstance(pin, dict) else pin)
    df = pd.read_parquet(args.parquet)
    selected = df[df["instance_id"].isin(ids)]
    print(f"Arm C: {len(selected)}/{len(ids)} pinned instances present in parquet")

    by_repo: dict[str, list[dict]] = defaultdict(list)
    for _, row in selected.iterrows():
        by_repo[row["repo"]].append(row.to_dict())
    print(f"{len(by_repo)} distinct repos (one clone + one index storage each)")

    # Subprocess env: keys must be present; the empty-string ANTHROPIC_BASE_URL
    # trap breaks the python anthropic SDK (reranker), so drop it.
    env = {k: v for k, v in os.environ.items() if k != "ANTHROPIC_BASE_URL"}

    args.workdir.mkdir(parents=True, exist_ok=True)
    cases: list[dict] = []
    est_voyage_usd = 0.0
    aborted = ""

    def checkpoint() -> None:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps({
            "est_voyage_usd": round(est_voyage_usd, 2),
            "aborted_reason": aborted,
            "cases": cases,
        }, indent=2), encoding="utf-8")

    for repo, instances in by_repo.items():
        if aborted:
            break
        slug = repo.replace("/", "__")
        repo_dir = args.workdir / slug
        storage = args.workdir / f"{slug}-cs-storage"
        cloned = False
        first_in_repo = True
        try:
            for row in instances:
                if est_voyage_usd >= args.voyage_ceiling_usd:
                    aborted = (f"voyage ceiling ${args.voyage_ceiling_usd:.2f} hit at "
                               f"~${est_voyage_usd:.2f} after {len(cases)} instances")
                    print(f"\n!!! {aborted}")
                    break

                iid = row["instance_id"]
                gt = list(row.get("edit_functions", []))
                case = {
                    "instance_id": iid, "repo": repo,
                    "category": row.get("category", "Unknown"),
                    "ground_truth": gt, "indexed": False,
                    "file_hit": False, "class_hit": False, "func_hit": False,
                    "top_files": [], "chunks_added": 0, "note": "",
                }
                t0 = time.time()
                print(f"\n=== armC {iid} ({repo}, {case['category']}) ===")
                try:
                    if not cloned:
                        if not clone_repo(repo, row["base_commit"], repo_dir):
                            case["note"] = "clone failed"
                            continue
                        size_mb = repo_size_mb(repo_dir)
                        if size_mb > MAX_REPO_MB:
                            case["note"] = f"repo too large ({size_mb:.0f} MB)"
                            # Skip the whole repo group: record + bail to next repo.
                            cases.append(case)
                            checkpoint()
                            break
                        cloned = True
                    elif not checkout(repo_dir, row["base_commit"]):
                        case["note"] = "checkout failed"
                        continue

                    ok, err, chunks = index_with_code_search(
                        args.cs_python, args.cs_root, repo_dir, storage, env,
                        force_full=first_in_repo,
                    )
                    first_in_repo = False
                    case["chunks_added"] = chunks
                    est_voyage_usd += chunks * EST_TOKENS_PER_CHUNK * EST_USD_PER_MTOK / 1e6
                    if not ok:
                        case["note"] = f"index failed: {err}"
                        continue
                    case["indexed"] = True

                    # Same query convention as arm A: first paragraph only.
                    short_query = row["problem_statement"].split("\n\n")[0].strip()
                    results = search_once(args.cs_python, args.cs_root, storage, short_query, env)

                    seen: list[str] = []
                    for r in results:
                        rp = r.get("relative_path") or ""
                        if rp and rp not in seen:
                            seen.append(rp)
                        if len(seen) >= args.top_files:
                            break
                    case["top_files"] = seen
                    # Persist per-rank result names so class/func can be
                    # re-scored at any depth offline. The 2026-06-11 pilot
                    # scored the full k=50 blob while arm A is judged on its
                    # top-10 entities — a depth mismatch that confounded the
                    # func delta and forced a BLOCKED verdict; with results
                    # persisted, matched-depth scoring needs no re-run.
                    case["results"] = [
                        {
                            "rank": i + 1,
                            "relative_path": r.get("relative_path") or "",
                            "parent_name": r.get("parent_name") or "",
                            "name": r.get("name") or "",
                        }
                        for i, r in enumerate(results)
                    ]

                    # Entity blob mirrors arm A's json_mode scoring blob
                    # (qualified-name-ish + file path per result).
                    blob = "\n".join(
                        f"{(r.get('parent_name') or '')}.{(r.get('name') or '')} {r.get('relative_path') or ''}"
                        for r in results
                    )
                    f_hit, c_hit, fn_hit = score_against_ground_truth(blob, gt)
                    case["file_hit"], case["class_hit"], case["func_hit"] = f_hit, c_hit, fn_hit
                except Exception as exc:  # noqa: BLE001 — record and continue the batch
                    case["note"] = f"error: {exc!r}"[:300]
                finally:
                    case["duration_s"] = round(time.time() - t0, 1)
                    cases.append(case)
                    checkpoint()
                    print(
                        f"  -> indexed={case['indexed']} file_hit={case['file_hit']} "
                        f"class_hit={case['class_hit']} func_hit={case['func_hit']} "
                        f"files={len(case['top_files'])} chunks={case['chunks_added']} "
                        f"~voyage=${est_voyage_usd:.2f} ({case['duration_s']}s) {case['note']}"
                    )
        finally:
            shutil.rmtree(repo_dir, ignore_errors=True)
            shutil.rmtree(storage, ignore_errors=True)

    n = len(cases)
    if n:
        print(
            f"\nArm C done: n={n} indexed={sum(c['indexed'] for c in cases)} "
            f"file={sum(c['file_hit'] for c in cases)}/{n} "
            f"class={sum(c['class_hit'] for c in cases)}/{n} "
            f"func={sum(c['func_hit'] for c in cases)}/{n} "
            f"~voyage=${est_voyage_usd:.2f} {('ABORTED: ' + aborted) if aborted else ''}"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
