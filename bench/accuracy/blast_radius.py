"""Per-call-site blast-radius analysis from accuracy harness baseline.

Step 1 of the 2026-05-02 plateau-2 plan (see knowledge-base/research/
2026-05-02-code-graph-plateau2-problem.md). Surfaces single-call-site
explosions that aggregate F1 + per-project F1 hide.

Per-site blast_radius = #FPs sharing site + #FNs sharing site

Reads a baseline JSON report. Two modes:

  PARTIAL  — uses sample_fp_scoped / sample_fn_scoped (10 + 6 per project).
             Catches the headline finding (top-1 site by FN concentration)
             but cannot compute distribution stats.

  FULL     — uses fp_scoped_full / fn_scoped_full if present (added by
             this PR). Computes p50/p95/max distribution, top-1% share
             of total errors, top-20 sites table.

Usage:
    python bench/accuracy/blast_radius.py [BASELINE_JSON]

Default baseline: most recent code-graph-go report in baselines/.
"""
from __future__ import annotations

import json
import statistics
import sys
from collections import Counter
from pathlib import Path

if sys.stdout.encoding and sys.stdout.encoding.lower() != "utf-8":
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")  # type: ignore[union-attr]

ROOT = Path(__file__).parent
BASELINES = ROOT / "baselines"


def find_default_report() -> Path:
    candidates = sorted(BASELINES.glob("*-code-graph-go-report.json"))
    if not candidates:
        sys.exit("No code-graph-go baseline report found")
    return candidates[-1]


def caller_qn(edge: list) -> str:
    """Edge tuples are (from_qn, to_qn, type). The call site is the caller QN."""
    return edge[0] if edge else ""


def analyze_arm(fps: list, fns: list, partial: bool) -> dict:
    """Compute blast-radius distribution for one arm (project or aggregate)."""
    sites: Counter[str] = Counter()
    for e in fps:
        sites[caller_qn(e)] += 1
    for e in fns:
        sites[caller_qn(e)] += 1
    if not sites:
        return {"n_sites": 0, "partial": partial}
    counts = sorted(sites.values(), reverse=True)
    total_errors = sum(counts)
    top_1pct_n = max(1, len(counts) // 100)
    top_1pct_share = sum(counts[:top_1pct_n]) / total_errors if total_errors else 0
    return {
        "partial": partial,
        "n_sites": len(sites),
        "total_errors": total_errors,
        "p50": int(statistics.median(counts)) if counts else 0,
        "p95": counts[max(0, int(len(counts) * 0.05) - 1)] if counts else 0,
        "max": counts[0] if counts else 0,
        "top_1pct_share": round(top_1pct_share, 4),
        "top_sites": sites.most_common(20),
    }


def short_qn(qn: str, max_chars: int = 60) -> str:
    """Display-shortened QN: drop the absolute-path prefix."""
    if not qn:
        return ""
    # Strip "c-Users-...-code-graph-internal-" -> "internal-"
    parts = qn.split(".")
    if len(parts) > 1 and "internal-" in parts[0]:
        idx = parts[0].rfind("internal-")
        parts[0] = parts[0][idx:]
    s = ".".join(parts)
    return s if len(s) <= max_chars else "..." + s[-(max_chars - 3):]


def main() -> None:
    report_path = Path(sys.argv[1]) if len(sys.argv) > 1 else find_default_report()
    print(f"=== Blast-radius analysis from {report_path.name} ===\n")
    d = json.loads(report_path.read_text(encoding="utf-8"))
    calls = d["results"].get("CALLS", {})

    # Decide PARTIAL vs FULL mode based on what's present
    has_full = "fp_scoped_full" in calls and "fn_scoped_full" in calls
    mode = "FULL" if has_full else "PARTIAL"

    if has_full:
        all_fps = calls["fp_scoped_full"]
        all_fns = calls["fn_scoped_full"]
    else:
        all_fps = calls.get("sample_fp_scoped", [])
        all_fns = calls.get("sample_fn_scoped", [])

    print(f"Mode: {mode}")
    print(f"  FPs available: {len(all_fps)} (scope-aligned reported: {calls['scope_aligned']['fp']})")
    print(f"  FNs available: {len(all_fns)} (scope-aligned reported: {calls['scope_aligned']['fn']})")
    if not has_full:
        print(f"  NOTE: PARTIAL data only. Distribution stats and top-20 may not be representative.")
        print(f"  For FULL analysis, re-run compare.py with the fp_scoped_full / fn_scoped_full")
        print(f"  fields (added in this same PR).")
    print()

    # Aggregate analysis
    agg = analyze_arm(all_fps, all_fns, partial=not has_full)
    print("=== Aggregate ===")
    print(f"  Distinct call sites with errors: {agg['n_sites']}")
    print(f"  Total error edges: {agg['total_errors']}")
    print(f"  p50/p95/max blast: {agg['p50']} / {agg['p95']} / {agg['max']}")
    print(f"  Top 1% sites share of total errors: {agg['top_1pct_share']:.1%}")
    print()

    print("=== Top sites by blast_radius ===")
    print(f"  {'blast':>5}  site")
    for site, n in agg["top_sites"][:20]:
        print(f"  {n:>5}  {short_qn(site)}")
    print()

    # Per-project (if per-project full data is present)
    pp = calls.get("per_project", [])
    if pp and has_full:
        print("=== Per-project (FULL mode) ===\n")
        for proj in pp:
            proj_name = proj["project"].split("-internal-")[-1] if "-internal-" in proj["project"] else proj["project"]
            # Filter the full sets to this project's edges by caller-QN prefix match
            proj_prefix = proj["project"]
            proj_fps = [e for e in all_fps if e[0].startswith(proj_prefix)]
            proj_fns = [e for e in all_fns if e[0].startswith(proj_prefix)]
            arm = analyze_arm(proj_fps, proj_fns, partial=False)
            print(f"  internal/{proj_name}: P={proj['precision']} F1={proj['f1']}")
            print(f"    sites={arm['n_sites']}  total_err={arm['total_errors']}  "
                  f"p50/p95/max={arm['p50']}/{arm['p95']}/{arm['max']}  "
                  f"top1%share={arm['top_1pct_share']:.1%}")
            print(f"    top-3 sites:")
            for site, n in arm["top_sites"][:3]:
                print(f"      {n:>3}  {short_qn(site)}")
            print()
    elif not has_full:
        print("(per-project breakdown skipped in PARTIAL mode — sample slices are too small)")

    # Headline check from samples
    print("=== Headline check ===")
    fn_sites = Counter(caller_qn(e) for e in all_fns)
    if fn_sites:
        top_fn_site, top_fn_n = fn_sites.most_common(1)[0]
        share = top_fn_n / len(all_fns)
        print(f"  Top-1 site by FN count: {short_qn(top_fn_site)}")
        print(f"  FN count at top site: {top_fn_n} of {len(all_fns)} sample FNs ({share:.0%})")
        if share > 0.5:
            print(f"  >> Confirms cohort hypothesis: a single call site dominates FN distribution.")
            print(f"  >> The plateau is partly a single-site explosion, not uniform error.")

    print()


if __name__ == "__main__":
    main()
