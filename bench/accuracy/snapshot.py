"""Current-state accuracy snapshot for code-graph — without re-baselining.

Reads existing baselines/*.json and the binary mtime, emits CURRENT.md with
per-fixture freshness bands. NO re-indexing, NO oracle re-run, NO compare run.
Just re-formats data already on disk so a grader can see at-a-glance which
metrics are trustworthy right now.

Use case: when grading code-graph, you need to know whether each cited
metric reflects the current binary or a baseline that predates the latest
PRs. The accuracy-gap-inventory and topic-page posture snapshots are
hand-authored and go stale within hours of accuracy-relevant PRs landing
(2026-05-10 incident: gap inventory cited HTTP_CALLS precision 17.6%
hours after route-extractor PRs shipped that drove it to ~0%
misresolution).

Trust bands compare baseline mtime to binary mtime:
    FRESH    baseline_mtime >= binary_mtime - 1d
    STALE    baseline_mtime in [binary_mtime - 7d, binary_mtime - 1d)
    OLD      baseline_mtime < binary_mtime - 7d
    UNKNOWN  binary missing

Run:
    python bench/accuracy/snapshot.py

Output:
    bench/accuracy/CURRENT.md
"""
from __future__ import annotations

import argparse
import datetime
import json
import re
import sys
from collections import defaultdict
from dataclasses import dataclass
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
BASELINES_DIR = REPO_ROOT / "bench" / "accuracy" / "baselines"
DEFAULT_OUTPUT = REPO_ROOT / "bench" / "accuracy" / "CURRENT.md"
BINARY_CANDIDATES = [
    REPO_ROOT / "bin" / "code-graph.exe",
    REPO_ROOT / "bin" / "code-graph",
    REPO_ROOT / "code-graph.exe",
    REPO_ROOT / "code-graph",
]

# baselines/YYYY-MM-DD-<fixture-with-dashes>-report.json
REPORT_NAME_RE = re.compile(
    r"^(\d{4}-\d{2}-\d{2})-(.+)-report\.json$"
)

EDGE_TYPES_TO_REPORT = ["CALLS", "IMPORTS", "HTTP_CALLS", "IMPLEMENTS"]
PREFERRED_METRIC = "scope_aligned"  # per bench/accuracy/README.md
FALLBACK_METRIC = "exact"  # for very old reports without scope_aligned


@dataclass
class FixtureReport:
    fixture: str
    date: str  # YYYY-MM-DD from filename
    path: Path
    mtime: float  # epoch seconds
    sha: str | None
    data: dict


def find_binary() -> tuple[Path | None, float | None]:
    """Return (binary_path, mtime) or (None, None) if no binary present."""
    for candidate in BINARY_CANDIDATES:
        if candidate.exists():
            return candidate, candidate.stat().st_mtime
    return None, None


def load_reports() -> dict[str, FixtureReport]:
    """Load most-recent report per fixture from baselines/*.json.

    Returns a dict {fixture_name: FixtureReport}. If a fixture has multiple
    dated reports, the one with the latest date in the filename wins (tie-
    breaker: file mtime).
    """
    by_fixture: dict[str, FixtureReport] = {}
    if not BASELINES_DIR.exists():
        return by_fixture
    for json_path in sorted(BASELINES_DIR.glob("*-report.json")):
        m = REPORT_NAME_RE.match(json_path.name)
        if not m:
            continue
        date_str, fixture = m.group(1), m.group(2)
        try:
            data = json.loads(json_path.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, OSError) as exc:
            print(
                f"  ! skipping {json_path.name}: {exc}", file=sys.stderr
            )
            continue
        mtime = json_path.stat().st_mtime
        existing = by_fixture.get(fixture)
        # Prefer later date, then later mtime as tiebreaker
        if existing is None or (date_str, mtime) > (existing.date, existing.mtime):
            by_fixture[fixture] = FixtureReport(
                fixture=fixture,
                date=date_str,
                path=json_path,
                mtime=mtime,
                sha=data.get("short_sha") or data.get("sha"),
                data=data,
            )
    return by_fixture


def extract_metric(report: FixtureReport, edge_type: str) -> dict | None:
    """Return the preferred-metric block for an edge type, or None.

    Looks up `results.<EDGE>.<scope_aligned|exact>`. Returns the block
    containing tp/fp/fn/precision/recall/f1.
    """
    results = report.data.get("results", {})
    edge_block = results.get(edge_type)
    if not isinstance(edge_block, dict):
        return None
    for key in (PREFERRED_METRIC, FALLBACK_METRIC):
        block = edge_block.get(key)
        if isinstance(block, dict) and "f1" in block:
            return {**block, "_metric_kind": key}
    return None


def freshness_band(
    baseline_mtime: float, binary_mtime: float | None
) -> str:
    """Compute FRESH / STALE / OLD / UNKNOWN."""
    if binary_mtime is None:
        return "UNKNOWN"
    delta = binary_mtime - baseline_mtime  # positive => baseline OLDER than binary
    day = 86400.0
    if delta <= day:
        return "FRESH"
    if delta <= 7 * day:
        return "STALE"
    return "OLD"


def fmt_mtime(epoch: float) -> str:
    return datetime.datetime.fromtimestamp(epoch).strftime("%Y-%m-%d %H:%M")


def fmt_n(value: float | int | None, places: int = 3) -> str:
    if value is None:
        return "—"
    if isinstance(value, int):
        return str(value)
    return f"{value:.{places}f}"


def classify_fixture(name: str) -> str:
    """Tag fixture as production / adversarial / unknown.

    Heuristic: filename containing 'adversarial' is adversarial; others
    default to production. This matches the existing baseline-naming
    convention.
    """
    if "adversarial" in name:
        return "adversarial"
    return "production"


def render_markdown(
    reports: dict[str, FixtureReport],
    binary_path: Path | None,
    binary_mtime: float | None,
) -> str:
    """Build the CURRENT.md content."""
    now = datetime.datetime.now().strftime("%Y-%m-%d %H:%M")
    lines: list[str] = []

    lines.append(f"# code-graph accuracy snapshot — generated {now}")
    lines.append("")
    lines.append(
        "Generated by `bench/accuracy/snapshot.py` from existing "
        "`baselines/*.json`. **No re-baselining performed.** Each row "
        "reports the most recent baseline JSON for that fixture."
    )
    lines.append("")
    if binary_path is not None and binary_mtime is not None:
        lines.append(
            f"- **Binary**: `{binary_path.name}` built **{fmt_mtime(binary_mtime)}**"
        )
    else:
        lines.append(
            "- **Binary**: not found at expected paths "
            "(`bin/code-graph.exe` or repo root); freshness band "
            "will read UNKNOWN."
        )
    lines.append("- **Preferred metric**: `scope_aligned` (per README); falls back to `exact` if absent.")
    lines.append(
        "- **Freshness bands**: `FRESH` (baseline ≤1d older than binary), "
        "`STALE` (1–7d), `OLD` (>7d), `UNKNOWN` (no binary). "
        "OLD/UNKNOWN rows are flagged and should be re-baselined before citation."
    )
    lines.append("")

    if not reports:
        lines.append("**No baseline JSONs found in `bench/accuracy/baselines/`.**")
        return "\n".join(lines) + "\n"

    # Group by classification for the table headers
    grouped: dict[str, list[FixtureReport]] = defaultdict(list)
    for fixture, report in reports.items():
        grouped[classify_fixture(fixture)].append(report)

    for kind in ("production", "adversarial"):
        bucket = sorted(grouped.get(kind, []), key=lambda r: r.fixture)
        if not bucket:
            continue
        title = (
            "Production fixtures" if kind == "production"
            else "Adversarial fixtures (stress-test floor — NOT comparable to production)"
        )
        lines.append(f"## {title}")
        lines.append("")
        # One table per edge type that has at least one fixture reporting it
        for edge_type in EDGE_TYPES_TO_REPORT:
            rows: list[tuple[str, dict, FixtureReport]] = []
            for report in bucket:
                metric = extract_metric(report, edge_type)
                if metric is not None:
                    rows.append((report.fixture, metric, report))
            if not rows:
                continue
            lines.append(f"### {edge_type}")
            lines.append("")
            lines.append(
                "| Fixture | F1 | Precision | Recall | TP | FP | FN | "
                "Metric | Baseline date | Baseline mtime | Freshness |"
            )
            lines.append(
                "|---|---:|---:|---:|---:|---:|---:|---|---|---|---|"
            )
            for fixture, metric, report in rows:
                band = freshness_band(report.mtime, binary_mtime)
                marker = "" if band == "FRESH" else f"  ⚠"
                lines.append(
                    f"| {fixture} | {fmt_n(metric.get('f1'))} | "
                    f"{fmt_n(metric.get('precision'))} | "
                    f"{fmt_n(metric.get('recall'))} | "
                    f"{fmt_n(metric.get('tp'))} | {fmt_n(metric.get('fp'))} | "
                    f"{fmt_n(metric.get('fn'))} | "
                    f"`{metric.get('_metric_kind', '?')}` | "
                    f"{report.date} | {fmt_mtime(report.mtime)} | "
                    f"{band}{marker} |"
                )
            lines.append("")

    # Per-project breakdowns for multi-project fixtures
    multi_project_rows: list[str] = []
    for fixture in sorted(reports):
        report = reports[fixture]
        for edge_type in EDGE_TYPES_TO_REPORT:
            edge_block = report.data.get("results", {}).get(edge_type)
            if not isinstance(edge_block, dict):
                continue
            per_project = edge_block.get("per_project")
            if not isinstance(per_project, list) or not per_project:
                continue
            for pp in per_project:
                if not isinstance(pp, dict):
                    continue
                multi_project_rows.append(
                    f"| {fixture} | {edge_type} | {pp.get('project', '?')} | "
                    f"{fmt_n(pp.get('f1'))} | {fmt_n(pp.get('precision'))} | "
                    f"{fmt_n(pp.get('recall'))} | {fmt_n(pp.get('oracle_count'))} | "
                    f"{fmt_n(pp.get('measured_count'))} |"
                )
    if multi_project_rows:
        lines.append("## Per-project breakdown (multi-subset fixtures)")
        lines.append("")
        lines.append(
            "Multi-subset fixtures (Rust crates, Go packages) report per-project "
            "metrics separately. Aggregate F1 hides variance — long-pole subsets "
            "drive the score."
        )
        lines.append("")
        lines.append(
            "| Fixture | Edge | Subset | F1 | Precision | Recall | Oracle # | Measured # |"
        )
        lines.append(
            "|---|---|---|---:|---:|---:|---:|---:|"
        )
        lines.extend(multi_project_rows)
        lines.append("")

    # Citation guidance
    lines.append("## Citation guidance")
    lines.append("")
    lines.append(
        "1. Cite metrics from `FRESH` rows directly with `[FRESH]` band."
    )
    lines.append(
        "2. `STALE` rows: cite with the `[STALE]` band AND a note that the "
        "metric predates the current binary. Re-baseline before using in a "
        "ship decision."
    )
    lines.append(
        "3. `OLD` / `UNKNOWN` rows: do NOT cite as current state. Re-run the "
        "harness for the affected fixture first."
    )
    lines.append(
        "4. Production and adversarial fixtures get separate grades. "
        "Adversarial fixtures are stress-test floors, not production grades."
    )
    lines.append(
        "5. Multi-subset fixtures (Rust crates): the long-pole subset usually "
        "drives the aggregate. Per-project breakdown above surfaces variance."
    )
    lines.append("")

    lines.append("## Methodology")
    lines.append("")
    lines.append(
        "This snapshot is **read-only** — it does not re-index, re-run oracles, "
        "or re-run compare. It re-formats the latest `baselines/*.json` per "
        "fixture into a freshness-stamped table. To refresh stale rows, run "
        "the relevant oracle + `compare.py` for that fixture; see "
        "`bench/accuracy/README.md`."
    )
    lines.append("")
    lines.append(
        f"Source files scanned: {len(reports)} fixture(s) under "
        f"`bench/accuracy/baselines/`."
    )
    lines.append("")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description=(
            "Emit CURRENT.md from existing accuracy baselines. "
            "Does NOT re-baseline."
        ),
    )
    parser.add_argument(
        "--output",
        type=Path,
        default=DEFAULT_OUTPUT,
        help=f"Output path (default: {DEFAULT_OUTPUT.relative_to(REPO_ROOT)})",
    )
    parser.add_argument(
        "--print", dest="to_stdout", action="store_true",
        help="Print to stdout instead of writing to --output",
    )
    args = parser.parse_args(argv)

    binary_path, binary_mtime = find_binary()
    reports = load_reports()
    content = render_markdown(reports, binary_path, binary_mtime)

    if args.to_stdout:
        sys.stdout.write(content)
        return 0

    args.output.write_text(content, encoding="utf-8")
    rel = args.output.relative_to(REPO_ROOT) if args.output.is_relative_to(REPO_ROOT) else args.output
    print(f"Wrote {rel} ({len(reports)} fixture(s))")
    return 0


if __name__ == "__main__":
    sys.exit(main())
