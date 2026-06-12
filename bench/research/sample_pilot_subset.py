#!/usr/bin/env python3
"""Stratified pilot sampler for the SweRank pre-filter pilot (2026-06-11 plan, Item 3).

Samples N instances stratified by category from the Loc-Bench instances JSON,
restricted to a reachability pin when given (--pin), otherwise live-probing
each sampled candidate via the GitHub commits API (reusing
bench/accuracy/locbench_reachability.github_reachable) and replacing
unreachable picks with same-category alternates. Never silently drops an
unreachable instance — replacement preserves the category targets, per the
2026-05-12 unreachable-tail finding (the GC'd tail is category-skewed).

Output pin JSON is consumed by eval_locbench_batch.py --instances.
"""
from __future__ import annotations

import argparse
import json
import os
import random
import sys
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "accuracy"))
from locbench_reachability import github_reachable  # noqa: E402

CATEGORIES = ["Bug Report", "Feature Request", "Performance Issue", "Security Vulnerability"]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--instances", required=True, type=Path)
    ap.add_argument("--n", type=int, default=50)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--pin", type=Path, default=None,
                    help="reachability pin JSON; restricts the pool, skips live probing")
    ap.add_argument("--out", required=True, type=Path)
    args = ap.parse_args()

    instances = json.loads(args.instances.read_text(encoding="utf-8"))
    pool = defaultdict(list)
    pinned_ids = None
    if args.pin:
        pin = json.loads(args.pin.read_text(encoding="utf-8"))
        pinned_ids = set(pin["pinned_instance_ids"])
    for inst in instances:
        if pinned_ids is not None and inst["instance_id"] not in pinned_ids:
            continue
        pool[inst["category"]].append(inst)

    rng = random.Random(args.seed)
    for cat in pool:
        rng.shuffle(pool[cat])

    # Category targets: n//4 each, remainder to the largest pools.
    base, rem = divmod(args.n, len(CATEGORIES))
    targets = {cat: base for cat in CATEGORIES}
    for cat in sorted(CATEGORIES, key=lambda c: -len(pool[c]))[:rem]:
        targets[cat] += 1

    token = os.environ.get("GITHUB_TOKEN", "")
    picked: list[dict] = []
    probed = 0
    for cat in CATEGORIES:
        candidates = list(pool[cat])
        take = min(targets[cat], len(candidates))
        chosen: list[dict] = []
        while candidates and len(chosen) < take:
            inst = candidates.pop()
            if pinned_ids is None:
                probed += 1
                if not github_reachable(inst["repo"], inst["base_commit"], token=token):
                    print(f"  unreachable, replacing: {inst['instance_id']}")
                    continue
            chosen.append(inst)
        if len(chosen) < targets[cat]:
            print(f"WARNING: {cat} under target ({len(chosen)}/{targets[cat]}) — pool exhausted")
        picked.extend(chosen)

    out = {
        "purpose": "swerank-prefilter-pilot 2026-06-11",
        "sample_seed": args.seed,
        "n": len(picked),
        "live_probed": probed,
        "categories": {cat: sum(1 for p in picked if p["category"] == cat) for cat in CATEGORIES},
        "pinned_instance_ids": sorted(p["instance_id"] for p in picked),
    }
    args.out.write_text(json.dumps(out, indent=2), encoding="utf-8")
    print(f"wrote {args.out}: n={out['n']} categories={out['categories']} probed={probed}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
