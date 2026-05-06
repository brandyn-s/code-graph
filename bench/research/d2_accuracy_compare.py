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
import sys
from pathlib import Path

# Get-well plan Phase 1: shared schema. Both eval (writer) and this
# script (reader) construct/parse via schema.py — drift is a parse-time
# error, not a silent fallback.
sys.path.insert(0, str(Path(__file__).resolve().parent))
import schema  # noqa: E402


def load_per_case(path: Path) -> dict[str, "schema.PerCaseRecord"]:
    """Load per-case JSON via the shared schema, keyed by instance_id.

    Get-well plan Phase 1 (2026-05-06): replaces the previous
    `raw.get("cases") or raw.get("instances")` fallback with a direct
    schema parse. If the on-disk JSON drifts from the schema (e.g.,
    eval renames a field), this raises a KeyError at parse time
    instead of silently producing an empty dict — which was the
    behavior pre-T2 that hid the bug for 6 hours after I shipped it.
    """
    raw = json.loads(path.read_text(encoding="utf-8"))
    summary = schema.BatchSummaryRecord.from_dict(raw)
    indexed: dict[str, schema.PerCaseRecord] = {}
    for case in summary.cases:
        if case.indexed and case.agent_ran:
            indexed[case.instance_id] = case
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
        # Get-well plan Phase 1.5: no-common-cases is now a hard error
        # (raise, not "return 2 + print"). Pre-T2 the script silently
        # printed "comparison impossible" and exited 2; users could
        # miss it in the output. Raising forces the operator to fix
        # the input rather than scroll past the message.
        raise RuntimeError(
            f"no common indexed instances between {args.serial} and "
            f"{args.parallel}; comparison impossible. "
            f"Serial has {len(serial)} indexed+agent_ran cases; "
            f"parallel has {len(parallel)}. "
            f"Either the seeds differ or one of the runs hit zero indexes."
        )

    # Compute per-mode counts on the common subset. Schema-typed
    # PerCaseRecord exposes file_hit / class_hit / func_hit as
    # attributes; pyright catches mistyped field names at edit time
    # (the bug class this whole get-well plan addresses).
    s_file = sum(1 for cid in common if serial[cid].file_hit)
    s_class = sum(1 for cid in common if serial[cid].class_hit)
    s_func = sum(1 for cid in common if serial[cid].func_hit)
    p_file = sum(1 for cid in common if parallel[cid].file_hit)
    p_class = sum(1 for cid in common if parallel[cid].class_hit)
    p_func = sum(1 for cid in common if parallel[cid].func_hit)

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

    # Roundtable T3 (2026-05-06): apply a sample-size adequacy gate
    # parallel to the audit-harness gate. A 0pp delta at n=6 is consistent
    # with parity but not statistically conclusive — at n<10 a single
    # mis-classified case shifts the headline by >=10pp. The flip-default
    # recommendation requires both small delta AND adequate n.
    MIN_COMMON_FOR_DEFAULT_FLIP = 10
    if n < MIN_COMMON_FOR_DEFAULT_FLIP:
        print(f"INSUFFICIENT_SAMPLE: only n={n} common indexed instances.")
        print(f"Threshold for default-flip recommendation: n>={MIN_COMMON_FOR_DEFAULT_FLIP}.")
        if max_delta <= 1.0:
            print(
                f"Observation: max |delta|={max_delta:.2f}pp at n={n} is "
                "consistent with accuracy parity but not statistically "
                "conclusive. Do NOT flip default on this evidence alone; "
                "expand the matched subset before acting."
            )
            return 0
        print(f"And max |delta|={max_delta:.2f}pp exceeds 1pp threshold; "
              "divergence at small-n is suggestive of real signal.")
        return 1
    if max_delta <= 1.0:
        print(f"PASS: |delta| <= 1pp threshold across all modes (n={n} >= {MIN_COMMON_FOR_DEFAULT_FLIP}).")
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
