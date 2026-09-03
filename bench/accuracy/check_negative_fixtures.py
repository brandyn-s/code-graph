"""Negative-benchmark gate for synthetic fixtures.

Runs hand-authored "negative" fixtures (kind: negative in
ground_truth.json) and compares emitted CALLS-family edges against
the fixture's `forbidden_emitted_calls` list. Any forbidden hit is
a phantom emission — code-graph bound a chain method on an external
trait to an internal target via bare-name resolution.

Independent of `oracle_rust_syn.py` — the fixture file IS the
oracle. This is the auxiliary release gate prescribed by the
2026-05-02 roundtable Recommendation #1
(~/Documents/knowledge-base/research/dispatch-runs/2026-05-02-codegraph-roundtable/results/META_SYNTHESIS.md).

Usage:
  # Run all negative fixtures, report counts.
  python bench/accuracy/check_negative_fixtures.py

  # Run a single fixture by directory name.
  python bench/accuracy/check_negative_fixtures.py rust-diesel-negative

  # Regression-gate mode: fail if phantom_count exceeds the per-fixture
  # baseline pinned in `negative_baselines.json` (created on first run).
  python bench/accuracy/check_negative_fixtures.py --regression-gate

Exit codes:
  0 — all checks pass
  1 — at least one phantom edge emitted (or, in --regression-gate mode,
      phantom count exceeds baseline)
  2 — infrastructure failure (binary missing, fixture missing, DB error)
"""
from __future__ import annotations

import argparse
import json
import re
import sqlite3
import subprocess
import sys
from pathlib import Path
from typing import Iterator

REPO_ROOT = Path(__file__).resolve().parents[2]
SYNTHETIC_ROOT = REPO_ROOT / "bench" / "accuracy" / "synthetic"
# Binary name differs by platform — Windows build produces .exe, Linux/macOS plain.
# Match Makefile: BINARY_EXT=.exe on Windows_NT, empty otherwise.
_BIN_DIR = REPO_ROOT / "bin"
_BINARY_CANDIDATES = [
    _BIN_DIR / "code-graph.exe",
    _BIN_DIR / "code-graph",
]
BINARY = next((p for p in _BINARY_CANDIDATES if p.exists()), _BINARY_CANDIDATES[0])
BASELINES_FILE = REPO_ROOT / "bench" / "accuracy" / "negative_baselines.json"

# CALLS-family edge types we evaluate forbidden patterns against.
CALLS_FAMILY = {"CALLS", "CALLS_EXTERNAL", "CALLS_PSEUDO", "INDIRECT_CALLS"}


def project_for_path(p: Path) -> str:
    """Mirror Go binary's pipeline.ProjectNameFromPath exactly.

    Duplicated from check_go_minimal_resolver_rule.py to avoid an
    intra-bench import dependency that would force PYTHONPATH gymnastics.
    """
    s = str(p)
    if len(s) >= 2 and s[1] == ":":
        s = s[0].lower() + s[1:]
    s = s.replace("\\", "-").replace("/", "-").replace(":", "-")
    while "--" in s:
        s = s.replace("--", "-")
    s = s.lstrip("-")
    return s or "root"


def rewrite_qn_to_current_project(qn: str, current_project: str) -> str:
    """Strip the leading project-prefix from `qn` and replace with
    `current_project`.

    Ground-truth files store QNs with the project prefix that existed
    on the developer's machine at GT-authoring time (e.g.
    `c-Users-user-Documents-GitHub-code-graph-bench-...`).
    On a different machine (CI runner, another developer) the project
    prefix differs because `project_for_path` derives it from the
    absolute filesystem path. Without rewriting, every positive-
    control and forbidden-pattern lookup misses with 100% recall loss.

    Rewrite preserves the path-suffix tail (everything after the
    first dot, which is the project's internal-relative QN like
    `src.main.entry`) and prepends the runtime project name.

    Examples:
        old_qn  = "c-Users-Brandyn-...-rust-actix-data-negative.src.main.entry"
        current = "home-runner-work-...-rust-actix-data-negative"
        result  = "home-runner-work-...-rust-actix-data-negative.src.main.entry"

        old_qn  = "no_dots_at_all"  (degenerate, shouldn't happen)
        result  = "no_dots_at_all"  (unchanged; defensive)
    """
    if "." not in qn:
        return qn
    _, tail = qn.split(".", 1)
    return current_project + "." + tail


def db_path_for(project: str) -> Path:
    return Path.home() / ".cache" / "code-graph" / f"{project}.db"


def index_fixture(fixture_root: Path) -> None:
    args = json.dumps({
        "repo_path": str(fixture_root.resolve()),
        "mode": "full",
        "force": True,
    })
    proc = subprocess.run(
        [str(BINARY), "cli", "index_repository", args],
        capture_output=True,
        timeout=180,
    )
    if proc.returncode != 0:
        sys.stderr.write(
            f"index_repository failed for {fixture_root.name} (rc={proc.returncode}): "
            f"{proc.stderr.decode('utf-8', errors='replace')[:500]}\n"
        )
        raise SystemExit(2)


def fetch_calls_from(project: str, from_qn: str) -> list[dict]:
    """All CALLS-family edges originating at from_qn in the project DB."""
    db = db_path_for(project)
    if not db.exists():
        sys.stderr.write(f"DB not found: {db}\n")
        raise SystemExit(2)
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    try:
        rows = con.execute(
            f"""
            SELECT src.qualified_name AS from_qn,
                   tgt.qualified_name AS to_qn,
                   e.type AS edge_type,
                   json_extract(e.properties, '$.resolver_rule') AS resolver_rule
            FROM edges e
            JOIN nodes src ON e.source_id = src.id
            JOIN nodes tgt ON e.target_id = tgt.id
            WHERE e.project = ?
              AND src.qualified_name = ?
              AND e.type IN ({','.join(repr(t) for t in CALLS_FAMILY)})
            """,
            (project, from_qn),
        ).fetchall()
    finally:
        con.close()
    return [dict(r) for r in rows]


def fetch_edges_matching(project: str, from_qn: str, to_qn: str) -> list[dict]:
    """Specific (from_qn, to_qn) edges, used for positive-control checks."""
    db = db_path_for(project)
    if not db.exists():
        sys.stderr.write(f"DB not found: {db}\n")
        raise SystemExit(2)
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    try:
        rows = con.execute(
            f"""
            SELECT src.qualified_name AS from_qn,
                   tgt.qualified_name AS to_qn,
                   e.type AS edge_type
            FROM edges e
            JOIN nodes src ON e.source_id = src.id
            JOIN nodes tgt ON e.target_id = tgt.id
            WHERE e.project = ?
              AND src.qualified_name = ?
              AND tgt.qualified_name = ?
              AND e.type IN ({','.join(repr(t) for t in CALLS_FAMILY)})
            """,
            (project, from_qn, to_qn),
        ).fetchall()
    finally:
        con.close()
    return [dict(r) for r in rows]


def iter_negative_fixtures(name_filter: str | None) -> Iterator[Path]:
    if not SYNTHETIC_ROOT.exists():
        return
    for child in sorted(SYNTHETIC_ROOT.iterdir()):
        if not child.is_dir():
            continue
        if name_filter and child.name != name_filter:
            continue
        gt = child / "ground_truth.json"
        if not gt.exists():
            continue
        try:
            with gt.open("r", encoding="utf-8") as f:
                data = json.load(f)
        except json.JSONDecodeError:
            continue
        if data.get("kind") != "negative":
            continue
        yield child


def evaluate_fixture(fixture_root: Path) -> tuple[int, list[dict], dict]:
    """Index fixture, fetch emitted edges, evaluate forbidden + positive lists.

    Returns (phantom_count, phantom_edges, positive_recall).
      positive_recall = {expected_edge_key: bool_emitted}
    """
    gt_path = fixture_root / "ground_truth.json"
    with gt_path.open("r", encoding="utf-8") as f:
        gt = json.load(f)

    print(f"\n[neg-gate] === {fixture_root.name} ===")
    print(f"[neg-gate] indexing {fixture_root}")
    index_fixture(fixture_root)

    project = project_for_path(fixture_root)
    print(f"[neg-gate] project: {project}")

    forbidden = gt.get("forbidden_emitted_calls") or []
    expected_internal = gt.get("expected_internal_calls") or []

    phantom_edges: list[dict] = []
    by_caller: dict[str, list[dict]] = {}

    # Group forbidden specs by caller QN and fetch each caller's edges once.
    # Rewrite stored from_qn to runtime project prefix — fixture GTs were
    # authored on a developer machine and contain that machine's project
    # prefix verbatim. Without rewriting, lookups miss 100% on any other
    # machine (CI runners, other developers).
    callers = sorted({rewrite_qn_to_current_project(entry["from_qn"], project) for entry in forbidden})
    for caller in callers:
        by_caller[caller] = fetch_calls_from(project, caller)

    for entry in forbidden:
        caller = rewrite_qn_to_current_project(entry["from_qn"], project)
        pattern = entry["to_qn_pattern"]
        reason = entry.get("reason", "")
        compiled = re.compile(pattern)
        for row in by_caller.get(caller, []):
            to_qn = row["to_qn"] or ""
            if compiled.fullmatch(to_qn):
                phantom_edges.append({
                    "from_qn": caller,
                    "to_qn": to_qn,
                    "edge_type": row["edge_type"],
                    "resolver_rule": row.get("resolver_rule"),
                    "matched_pattern": pattern,
                    "reason": reason,
                })

    # Positive controls: confirm fixture indexed at all.
    # Rewrite stored from_qn / to_qn to runtime project prefix —
    # see rewrite_qn_to_current_project for rationale.
    positive_recall: dict[str, bool] = {}
    for exp in expected_internal:
        from_qn = rewrite_qn_to_current_project(exp["from_qn"], project)
        to_qn = rewrite_qn_to_current_project(exp["to_qn"], project)
        # Key still uses the original GT-stored QNs so the report is
        # readable in the original machine's vocabulary (matches what
        # the developer sees in the fixture file).
        key = f"{exp['from_qn']} -> {exp['to_qn']}"
        emitted = bool(fetch_edges_matching(project, from_qn, to_qn))
        positive_recall[key] = emitted

    return len(phantom_edges), phantom_edges, positive_recall


def load_baselines() -> dict:
    if not BASELINES_FILE.exists():
        return {}
    with BASELINES_FILE.open("r", encoding="utf-8") as f:
        return json.load(f)


def main() -> int:
    parser = argparse.ArgumentParser(
        description=(__doc__ or "Negative-benchmark gate for synthetic fixtures.").split("\n\n")[0]
    )
    parser.add_argument(
        "fixture",
        nargs="?",
        default=None,
        help="Specific fixture directory name (e.g., rust-diesel-negative). Omit to run all.",
    )
    parser.add_argument(
        "--regression-gate",
        action="store_true",
        help="Compare phantom counts against negative_baselines.json; fail on increase.",
    )
    parser.add_argument(
        "--write-baseline",
        action="store_true",
        help="Write current phantom counts to negative_baselines.json (use ONCE per fixture, then rely on --regression-gate).",
    )
    args = parser.parse_args()

    if not BINARY.exists():
        sys.stderr.write(
            f"Binary not built: {BINARY}\n"
            "Run `make build` (Windows) or "
            "`CGO_ENABLED=1 go build -o bin/code-graph ./cmd/code-graph/` (Linux/macOS).\n"
        )
        return 2

    fixtures = list(iter_negative_fixtures(args.fixture))
    if not fixtures:
        target = args.fixture or "<any>"
        sys.stderr.write(f"No negative fixtures found (filter={target}).\n")
        return 2

    baselines = load_baselines() if args.regression_gate else {}
    # When writing baselines, start from existing file content and update
    # only the entries for fixtures that actually ran. A name-filtered run
    # must NOT drop other fixtures' baselines.
    new_baselines: dict = load_baselines() if args.write_baseline else {}
    overall_failed = False

    for fixture in fixtures:
        phantom_count, phantoms, positive = evaluate_fixture(fixture)
        new_baselines[fixture.name] = {"phantom_count": phantom_count}

        # Report phantoms.
        if phantoms:
            print(f"[neg-gate] PHANTOM EDGES ({len(phantoms)}):")
            for p in phantoms:
                print(
                    f"  ! {p['from_qn'][-70:]} -> {p['to_qn'][-70:]} "
                    f"({p['edge_type']}, rule={p['resolver_rule']!r}, pattern={p['matched_pattern']})"
                )
        else:
            print("[neg-gate] no phantom edges")

        # Report positive-control recall.
        missing = [k for k, v in positive.items() if not v]
        if missing:
            print(
                f"[neg-gate] POSITIVE-CONTROL MISS ({len(missing)}/{len(positive)}): "
                "fixture may not be indexing correctly"
            )
            for k in missing:
                print(f"  ? {k[-100:]}")
            overall_failed = True
        else:
            print(f"[neg-gate] positive controls: {len(positive)}/{len(positive)} emitted")

        # Regression-gate logic.
        if args.regression_gate:
            baseline = baselines.get(fixture.name, {}).get("phantom_count")
            if baseline is None:
                print(
                    f"[neg-gate] FAIL: no baseline for {fixture.name}; "
                    "run with --write-baseline first"
                )
                overall_failed = True
            elif phantom_count > baseline:
                print(
                    f"[neg-gate] REGRESSION: {fixture.name} "
                    f"phantom_count={phantom_count} > baseline={baseline}"
                )
                overall_failed = True
            else:
                print(
                    f"[neg-gate] regression-gate ok: "
                    f"phantom_count={phantom_count} <= baseline={baseline}"
                )
        else:
            # Default mode: any phantom is a fail.
            if phantom_count > 0:
                overall_failed = True

    if args.write_baseline:
        with BASELINES_FILE.open("w", encoding="utf-8") as f:
            json.dump(new_baselines, f, indent=2, sort_keys=True)
            f.write("\n")
        print(f"\n[neg-gate] wrote baselines to {BASELINES_FILE}")

    print()
    if overall_failed:
        print("[neg-gate] FAIL")
        return 1
    print("[neg-gate] PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
