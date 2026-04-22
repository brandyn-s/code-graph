"""
Compare two baseline JSON files from harness.py.

Reports per-repo per-question changes (grade transitions, latency deltas,
result_hash changes) and per-repo summary stats. Designed to be the gate
check after each feature PR — if a PR is supposed to turn Q13 from N/A to
PASS, this diff surfaces that evidence.

Usage:
    python bench/compare.py bench/baseline_2026-04-22.json bench/after_pr1.json
    python bench/compare.py --json bench/before.json bench/after.json > diff.json
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


def load(path: str) -> dict:
    return json.loads(Path(path).read_text(encoding="utf-8"))


def compare_question(q_before: dict, q_after: dict) -> dict | None:
    """Return a change record if anything meaningful changed, else None."""
    changes = {}
    if q_before.get("grade") != q_after.get("grade"):
        changes["grade"] = {"before": q_before.get("grade"), "after": q_after.get("grade")}
    before_ms = q_before.get("latency_ms") or 0
    after_ms = q_after.get("latency_ms") or 0
    if before_ms and after_ms:
        pct = (after_ms - before_ms) / before_ms * 100
        if abs(pct) >= 25:
            changes["latency"] = {"before_ms": before_ms, "after_ms": after_ms, "pct": round(pct, 1)}
    if q_before.get("result_hash") != q_after.get("result_hash") and q_before.get("result_hash"):
        changes["result_hash"] = {
            "before": q_before.get("result_hash"),
            "after": q_after.get("result_hash"),
            "before_preview": q_before.get("preview", "")[:80],
            "after_preview": q_after.get("preview", "")[:80],
        }
    return changes or None


def main() -> int:
    ap = argparse.ArgumentParser(description="Diff two baseline files")
    ap.add_argument("before")
    ap.add_argument("after")
    ap.add_argument("--json", action="store_true", help="emit JSON instead of human-readable")
    args = ap.parse_args()

    before = load(args.before)
    after = load(args.after)

    diff: dict = {
        "before_captured_at": before.get("captured_at"),
        "after_captured_at": after.get("captured_at"),
        "before_binary_sha": before.get("binary_sha"),
        "after_binary_sha": after.get("binary_sha"),
        "repos": {},
    }

    for fid in sorted(set(before["repos"]) | set(after["repos"])):
        b_repo = before["repos"].get(fid, {})
        a_repo = after["repos"].get(fid, {})
        repo_diff = {"questions": {}}

        # Index stats
        b_idx = b_repo.get("index") or {}
        a_idx = a_repo.get("index") or {}
        if b_idx and a_idx:
            for key in ("nodes", "edges"):
                if b_idx.get(key) != a_idx.get(key):
                    repo_diff.setdefault("index", {})[key] = {
                        "before": b_idx.get(key),
                        "after": a_idx.get(key),
                        "delta": (a_idx.get(key, 0) or 0) - (b_idx.get(key, 0) or 0),
                    }

        # Per-question
        all_qs = set(b_repo.get("questions", {})) | set(a_repo.get("questions", {}))
        for qid in sorted(all_qs):
            qb = b_repo.get("questions", {}).get(qid, {})
            qa = a_repo.get("questions", {}).get(qid, {})
            change = compare_question(qb, qa)
            if change:
                repo_diff["questions"][qid] = change

        if repo_diff.get("index") or repo_diff["questions"]:
            diff["repos"][fid] = repo_diff

    if args.json:
        print(json.dumps(diff, indent=2))
        return 0

    # Human-readable
    print(f"Before: {diff['before_captured_at']}  (binary {diff['before_binary_sha']})")
    print(f"After:  {diff['after_captured_at']}  (binary {diff['after_binary_sha']})")
    print()

    if not diff["repos"]:
        print("no meaningful changes detected")
        return 0

    for fid, repo_diff in diff["repos"].items():
        print(f"=== {fid} ===")
        if repo_diff.get("index"):
            print(f"  INDEX:")
            for key, v in repo_diff["index"].items():
                sign = "+" if v["delta"] >= 0 else ""
                print(f"    {key}: {v['before']} -> {v['after']} ({sign}{v['delta']})")
        for qid, change in repo_diff["questions"].items():
            bits = []
            if "grade" in change:
                bits.append(f"grade {change['grade']['before']} -> {change['grade']['after']}")
            if "latency" in change:
                lat = change["latency"]
                sign = "+" if lat["pct"] > 0 else ""
                bits.append(f"latency {lat['before_ms']}ms -> {lat['after_ms']}ms ({sign}{lat['pct']}%)")
            if "result_hash" in change:
                bits.append(f"result changed ({change['result_hash']['before']} -> {change['result_hash']['after']})")
            print(f"  {qid}: {'; '.join(bits)}")
        print()

    return 0


if __name__ == "__main__":
    sys.exit(main())
