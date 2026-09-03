"""Empirically validate the confidence_band thresholds in trace.go.

Background: T2 #9 added `confidence_band` to trace_call_path responses.
The thresholds were heuristics (>=0.8 high, >=0.5 medium, <0.5 low,
0-with-unresolved-positive => speculative). This script measures the
actual distribution of resolved/(resolved+unresolved) ratios across
every Function/Method node in every code-graph project, so the thresholds
can be tuned to natural breakpoints in the data.

Inputs: code-graph SQLite DBs at `~/.cache/code-graph/*.db`.
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

CACHE_DIR = pathlib.Path.home() / ".cache" / "code-graph"


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
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")

    # --ci-smoke is the CI mode: probe the codebase imports + threshold
    # logic without requiring real DBs at ~/.cache/code-graph/.
    # Returns 0 if the script's machinery runs cleanly. The drift-detection
    # itself is meaningful only against real DBs (run locally before
    # accepting drift signals).
    if "--ci-smoke" in sys.argv:
        try:
            # Exercise the threshold-calculation path against a synthetic
            # ratio distribution so import-time + math errors surface.
            test_ratios = [0.99, 0.97, 0.85, 0.50, 0.05, 0.02]
            ps = percentiles(test_ratios, [0.10, 0.50, 0.90])
            assert len(ps) == 3
            print(f"OK: probe machinery alive (synthetic P10/P50/P90 = {ps})")
            return 0
        except Exception as e:
            print(f"FAIL: probe machinery broken: {e}", file=sys.stderr)
            return 1

    # --check-drift is the real drift-detection mode (Phase B3). Compares
    # the current high-band cluster percentage against the baseline saved
    # at confidence_band_baseline.json; exits 1 if drift exceeds the
    # threshold.
    #
    # Run after a known-intentional change (INDIRECT_CALLS extractor
    # update, new resolver strategy) with `--update-drift-baseline` to
    # rebaseline.
    if "--check-drift" in sys.argv or "--update-drift-baseline" in sys.argv:
        return _check_drift_main(
            update_baseline="--update-drift-baseline" in sys.argv
        )

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


def _check_drift_main(update_baseline: bool = False) -> int:
    """Phase B3: confidence_band drift detection vs saved baseline.

    Computes the current high-band percentage (ratio >= 0.95) across all
    indexed projects, compares against the baseline at
    confidence_band_baseline.json, and exits 1 if the deviation exceeds
    the configured threshold (default 5pp).

    Returns 0 on success (drift within tolerance OR baseline updated).
    Returns 1 on (a) drift exceeds threshold, or (b) no DBs to probe.
    """
    if not CACHE_DIR.exists():
        print(f"No cache dir at {CACHE_DIR} — drift check requires indexed DBs")
        return 1

    dbs = sorted(CACHE_DIR.glob("*.db"))
    dbs = [d for d in dbs if not d.name.endswith("-shm.db") and not d.name.endswith("-wal.db")]
    if not dbs:
        print(f"No DBs in {CACHE_DIR} — drift check requires indexed DBs")
        return 1

    all_ratios: list[float] = []
    for db in dbs:
        try:
            triples = per_function_resolved_ratios(db)
        except Exception as e:
            print(f"  {db.name}: ERROR {e}")
            continue
        valid = [t[0] for t in triples if not (t[0] != t[0])]
        all_ratios.extend(valid)

    if not all_ratios:
        print("No ratio data found — drift check requires populated DBs")
        return 1

    high_band_count = sum(1 for r in all_ratios if r >= 0.95)
    high_band_pct = 100.0 * high_band_count / len(all_ratios)

    baseline_path = pathlib.Path(__file__).parent / "confidence_band_baseline.json"
    if update_baseline:
        baseline_path.write_text(
            json.dumps(
                {
                    "_comment": (
                        "Baseline distribution for confidence_band drift detection. "
                        "Rebaseline by running confidence_band_distribution.py "
                        "--update-drift-baseline after intentional extractor changes."
                    ),
                    "baseline_date": "auto",
                    "n_functions_with_both_resolved_and_unresolved": len(all_ratios),
                    "n_projects": len(dbs),
                    "high_band_pct": round(high_band_pct, 2),
                    "low_band_pct": round(
                        100.0 * sum(1 for r in all_ratios if r < 0.10) / len(all_ratios), 2
                    ),
                    "drift_threshold_pp": 5.0,
                },
                indent=2,
            ),
            encoding="utf-8",
        )
        print(f"Baseline updated: high_band_pct={high_band_pct:.1f}%")
        return 0

    if not baseline_path.exists():
        print(f"No baseline at {baseline_path} — run with --update-drift-baseline first")
        return 1

    baseline = json.loads(baseline_path.read_text(encoding="utf-8"))
    baseline_high = float(baseline.get("high_band_pct", 72.0))
    threshold_pp = float(baseline.get("drift_threshold_pp", 5.0))
    drift_pp = high_band_pct - baseline_high

    print(f"=== confidence_band drift check ===")
    print(f"  baseline high_band_pct: {baseline_high:.1f}%")
    print(f"  current  high_band_pct: {high_band_pct:.1f}%")
    print(f"  drift:                  {drift_pp:+.2f}pp")
    print(f"  threshold:              ±{threshold_pp:.1f}pp")
    print(f"  n_functions:            {len(all_ratios)} across {len(dbs)} DBs")

    if abs(drift_pp) > threshold_pp:
        print(f"\nFAIL: drift {drift_pp:+.2f}pp exceeds ±{threshold_pp:.1f}pp threshold.")
        print("If the drift is from a known intentional change (e.g., new INDIRECT_CALLS")
        print("extractor edges), rebaseline with:")
        print(f"  python {pathlib.Path(__file__).name} --update-drift-baseline")
        print("If the drift is unexpected, investigate the recent changes to")
        print("internal/cbm/extract_calls.c and the resolver pipeline.")
        return 1

    print(f"\nOK: within ±{threshold_pp:.1f}pp tolerance")
    return 0


if __name__ == "__main__":
    sys.exit(main())
