#!/usr/bin/env python3
"""Score the SweRank pre-filter pilot: paired deltas + bootstrap CIs.

Inputs: per-case JSONs from eval_locbench_batch (--per-case-json shape, arms
A and B) and armC_retrieval.py (arm C shape). Pairs instances by instance_id
over the intersection that every requested arm attempted (indexed+agent_ran
for batch arms; indexed for arm C), reports per-metric accuracy per arm, and
paired bootstrap (10K resamples, seed fixed) 95% CIs on the deltas vs arm A
— the pattern from code-search bench/research/paired_bootstrap_per_subproject.py.

Per eval-shipping discipline: the CI is computed on per-instance paired data;
"CI excludes zero" is the verdict line for each delta.
"""
from __future__ import annotations

import argparse
import json
import random
from pathlib import Path

METRICS = ("file_hit", "class_hit", "func_hit")


def load_batch_cases(path: Path) -> dict[str, dict]:
    data = json.loads(path.read_text(encoding="utf-8"))
    out = {}
    for c in data.get("cases", []):
        if c.get("indexed") and c.get("agent_ran", True):
            out[c["instance_id"]] = c
    return out


def paired_bootstrap(deltas: list[int], n_boot: int = 10000, seed: int = 42) -> tuple[float, float, float]:
    rng = random.Random(seed)
    n = len(deltas)
    mean = sum(deltas) / n
    samples = []
    for _ in range(n_boot):
        s = [deltas[rng.randrange(n)] for _ in range(n)]
        samples.append(sum(s) / n)
    samples.sort()
    lo = samples[int(0.025 * n_boot)]
    hi = samples[int(0.975 * n_boot)]
    return mean, lo, hi


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--arm-a", required=True, type=Path)
    ap.add_argument("--arm-b", type=Path, default=None)
    ap.add_argument("--arm-c", type=Path, default=None)
    ap.add_argument("--n-boot", type=int, default=10000)
    args = ap.parse_args()

    arms: dict[str, dict[str, dict]] = {"A": load_batch_cases(args.arm_a)}
    if args.arm_b:
        arms["B"] = load_batch_cases(args.arm_b)
    if args.arm_c:
        arms["C"] = load_batch_cases(args.arm_c)

    common = set(arms["A"])
    for name, cases in arms.items():
        common &= set(cases)
    ids = sorted(common)
    print(f"paired n = {len(ids)} (intersection of attempted instances across arms {'/'.join(arms)})")
    if not ids:
        print("nothing to compare")
        return 1

    for metric in METRICS:
        print(f"\n== {metric} ==")
        for name, cases in arms.items():
            acc = sum(bool(cases[i].get(metric)) for i in ids) / len(ids)
            print(f"  arm {name}: {acc:.3f}")
        for name in [n for n in arms if n != "A"]:
            deltas = [
                int(bool(arms[name][i].get(metric))) - int(bool(arms["A"][i].get(metric)))
                for i in ids
            ]
            mean, lo, hi = paired_bootstrap(deltas, n_boot=args.n_boot)
            excl = "EXCLUDES zero" if (lo > 0 or hi < 0) else "includes zero"
            print(f"  Δ({name}-A): {mean:+.3f}  95% CI [{lo:+.3f}, {hi:+.3f}]  ({excl}, {args.n_boot} resamples)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
