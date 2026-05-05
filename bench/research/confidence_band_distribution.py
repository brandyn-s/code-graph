"""Empirically validate the confidence_band thresholds in trace.go.

Background: T2 #9 added `confidence_band` to trace_call_path responses.
The thresholds were heuristics (>=0.8 high, >=0.5 medium, <0.5 low,
0-with-unresolved-positive => speculative). This script measures the
actual distribution of resolved/(resolved+unresolved) ratios across
every Function/Method node in every code-graph project, so the thresholds
can be tuned to natural breakpoints in the data.

Inputs: code-graph SQLite DBs at `~/.cache/codebase-memory-mcp/*.db`.
Output: stdout report with per-project distribution, aggregate
distribution, and proposed empirical thresholds.

Run: python bench/research/confidence_band_distribution.py
"""
from __future__ import annotations

import json
import pathlib
import sqlite3
import statistics
import sys

CACHE_DIR = pathlib.Path.home() / ".cache" / "codebase-memory-mcp"


def per_function_resolved_ratios(db_path: pathlib.Path) -> list[tuple[float, int, int]]:
    """Return [(ratio, resolved, unresolved), ...] for each Function/Method."""
    con = sqlite3.connect(str(db_path))
    con.execute("PRAGMA query_only = ON")
    out: list[tuple[float, int, int]] = []
    # Get every Function/Method node + its outbound CALLS count + its
    # unresolved_call_count property.
    rows = con.execute(
        """
        SELECT n.id, n.properties,
               (SELECT COUNT(*) FROM edges e
                WHERE e.source_id = n.id AND e.type = 'CALLS') AS resolved
        FROM nodes n
        WHERE n.label IN ('Function', 'Method')
        """
    ).fetchall()
    for _, props_json, resolved in rows:
        unresolved = 0
        if props_json:
            try:
                props = json.loads(props_json)
                v = props.get("unresolved_call_count")
                if isinstance(v, (int, float)):
                    unresolved = int(v)
            except Exception:
                pass
        total = resolved + unresolved
        if total == 0:
            # No call sites — treat as "high" by current rule (vacuously safe).
            # Excluded from ratio distribution because ratio is undefined,
            # but counted separately.
            out.append((float("nan"), resolved, unresolved))
            continue
        out.append((resolved / total, resolved, unresolved))
    con.close()
    return out


def percentiles(xs: list[float], qs: list[float]) -> list[float]:
    if not xs:
        return [float("nan")] * len(qs)
    s = sorted(xs)
    out = []
    n = len(s)
    for q in qs:
        i = max(0, min(n - 1, int(q * (n - 1))))
        out.append(s[i])
    return out


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    if not CACHE_DIR.exists():
        print(f"No cache dir at {CACHE_DIR}")
        return 1
    dbs = sorted(CACHE_DIR.glob("*.db"))
    dbs = [d for d in dbs if not d.name.endswith("-shm.db") and not d.name.endswith("-wal.db")]
    if not dbs:
        print(f"No DBs in {CACHE_DIR}")
        return 1

    print(f"=== confidence_band distribution probe ({len(dbs)} DBs) ===\n")

    all_ratios: list[float] = []
    no_calls_total = 0
    speculative_total = 0  # unresolved > 0 AND resolved == 0
    function_total = 0

    print(f"{'project':<60s} {'fns':>6} {'P10':>6} {'P50':>6} {'P90':>6} "
          f"{'no_calls':>9} {'speculative':>11}")
    print("-" * 110)

    for db in dbs:
        try:
            triples = per_function_resolved_ratios(db)
        except Exception as e:
            print(f"{db.name:<60s} ERROR: {e}")
            continue
        valid_ratios = [t[0] for t in triples if not (t[0] != t[0])]  # filter NaN
        no_calls = sum(1 for t in triples if t[0] != t[0])
        speculative = sum(1 for t in triples if t[1] == 0 and t[2] > 0)
        function_total += len(triples)
        no_calls_total += no_calls
        speculative_total += speculative
        all_ratios.extend(valid_ratios)
        if valid_ratios:
            p10, p50, p90 = percentiles(valid_ratios, [0.10, 0.50, 0.90])
            print(
                f"{db.stem[:60]:<60s} {len(triples):>6} "
                f"{p10:>6.2f} {p50:>6.2f} {p90:>6.2f} "
                f"{no_calls:>9} {speculative:>11}"
            )
        else:
            print(
                f"{db.stem[:60]:<60s} {len(triples):>6} "
                f"{'-':>6} {'-':>6} {'-':>6} "
                f"{no_calls:>9} {speculative:>11}"
            )

    print()
    print(f"=== Aggregate over all DBs ===")
    print(f"Total functions/methods:  {function_total}")
    print(f"No-calls (vacuous high):  {no_calls_total} "
          f"({100*no_calls_total/function_total:.1f}%)")
    print(f"Speculative (0-of-N):     {speculative_total} "
          f"({100*speculative_total/function_total:.1f}%)")
    print(f"Functions with both res+unres: {len(all_ratios)} "
          f"({100*len(all_ratios)/function_total:.1f}%)")

    if all_ratios:
        qs = [0.05, 0.10, 0.25, 0.50, 0.75, 0.90, 0.95]
        ps = percentiles(all_ratios, qs)
        print(f"\nRatio percentiles (resolved / (resolved+unresolved)):")
        for q, p in zip(qs, ps):
            print(f"  P{int(q*100):2d}: {p:.3f}")
        mean = statistics.mean(all_ratios)
        median = statistics.median(all_ratios)
        print(f"  mean: {mean:.3f}  median: {median:.3f}")

        # Histogram in 10 buckets
        print(f"\nHistogram (10 buckets):")
        buckets = [0] * 10
        for r in all_ratios:
            idx = min(9, int(r * 10))
            buckets[idx] += 1
        max_bucket = max(buckets) if buckets else 1
        for i, b in enumerate(buckets):
            lo = i / 10
            hi = (i + 1) / 10
            bar = "#" * int(40 * b / max_bucket)
            print(f"  [{lo:.1f}, {hi:.1f}): {b:>5} {bar}")

        # Suggest thresholds based on distribution
        print(f"\n=== Threshold proposal ===")
        print(f"Current (heuristic):  high>=0.8  medium>=0.5  low<0.5  speculative=0+unres")
        # Pick thresholds at natural distribution points
        p25, p50, p75 = percentiles(all_ratios, [0.25, 0.50, 0.75])
        print(f"Proposed (empirical): high>={p75:.2f}  medium>={p25:.2f}  "
              f"low<{p25:.2f}  speculative=0+unres (unchanged)")
        print(f"  Rationale: P75 is the 'top quartile' boundary — calling something")
        print(f"  'high confidence' when only {100-75}% of nodes hit that score is")
        print(f"  consistent with the meaning of 'high'. P25 separates the bottom")
        print(f"  quartile (low) from the middle.")

    return 0


if __name__ == "__main__":
    sys.exit(main())
