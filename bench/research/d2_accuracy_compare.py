"""Plan 5 Phase A.8: serial vs parallel iter=2 accuracy comparison.

Reads two per-case JSONs (serial + parallel) from eval_locbench_batch
runs at the same n+seed and computes the per-mode accuracy delta:

  |delta| = |mode_count_serial - mode_count_parallel| / N

Falsifier from META_SYNTHESIS D2: |Acc@10 delta| <= 1pp on file/class/
func per-mode counts → parallel iter=2 accuracy parity confirmed →
flip LOCAGENT_PARALLEL default from unset to 1.

Usage:
    python bench/research/d2_accuracy_compare.py \\
        --serial bench/research/baselines/2026-05-06-loc-bench-n50-serial.json \\
        --parallel bench/research/baselines/2026-05-06-loc-bench-n50-parallel.json
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path


def load_per_case(path: Path) -> dict[str, dict]:
    """Load per-case JSON; key by instance_id for cross-mode alignment."""
    raw = json.loads(path.read_text(encoding="utf-8"))
    cases = raw.get("instances", [])
    indexed = {}
    for c in cases:
        if c.get("indexed") and c.get("agent_ran"):
            indexed[c["instance_id"]] = c
    return indexed


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--serial", required=True, help="path to serial per-case JSON")
    p.add_argument("--parallel", required=True, help="path to parallel per-case JSON")
    args = p.parse_args()

    serial = load_per_case(Path(args.serial))
    parallel = load_per_case(Path(args.parallel))

    # Align by instance_id — only common instances are scored.
    common = sorted(set(serial.keys()) & set(parallel.keys()))
    print(f"Serial only:   {len(serial)} indexed+agent_ran")
    print(f"Parallel only: {len(parallel)} indexed+agent_ran")
    print(f"Common:        {len(common)}")

    if not common:
        print("ERROR: no common indexed instances — comparison impossible")
        return 2

    # Compute per-mode counts on the common subset.
    s_file = sum(1 for cid in common if serial[cid].get("file_hit"))
    s_class = sum(1 for cid in common if serial[cid].get("class_hit"))
    s_func = sum(1 for cid in common if serial[cid].get("func_hit"))
    p_file = sum(1 for cid in common if parallel[cid].get("file_hit"))
    p_class = sum(1 for cid in common if parallel[cid].get("class_hit"))
    p_func = sum(1 for cid in common if parallel[cid].get("func_hit"))

    n = len(common)
    print()
    print("=== Per-mode counts (common subset) ===")
    print(f"{'mode':<10} {'serial':>10} {'parallel':>10} {'delta':>10} {'pp':>8}")
    for mode, s, pl in [("file", s_file, p_file), ("class", s_class, p_class), ("func", s_func, p_func)]:
        delta = pl - s
        pp = abs(delta) / n * 100
        print(f"{mode:<10} {s:>10} {pl:>10} {delta:>+10} {pp:>7.1f}pp")

    print()
    print("=== Falsifier verdict ===")
    deltas_pp = [
        abs(p_file - s_file) / n * 100,
        abs(p_class - s_class) / n * 100,
        abs(p_func - s_func) / n * 100,
    ]
    max_delta = max(deltas_pp)
    print(f"Max |delta| across modes: {max_delta:.2f}pp")
    if max_delta <= 1.0:
        print("PASS: |delta| <= 1pp threshold across all modes.")
        print("Recommendation: flip LOCAGENT_PARALLEL default from unset to 1.")
        return 0
    if max_delta <= 3.0:
        print(f"AMBIGUOUS: {max_delta:.1f}pp delta; close to threshold.")
        print("Recommendation: re-run with larger n before flipping default.")
        return 0
    print(f"FAIL: {max_delta:.1f}pp delta exceeds 1pp threshold.")
    print("Recommendation: keep LOCAGENT_PARALLEL opt-in; investigate divergence.")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
