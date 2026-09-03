"""Post-battery synthetic fixture regression gate.

Indexes `bench/accuracy/synthetic/post-battery/` with the local
code-graph binary, then asserts every capability the
12-item PSM test battery (April-May 2026) regression-protected.

Each assertion has a documented PR provenance and what regressing
it would mean — so a future contributor failing this gate sees
which capability they broke.

Usage:
  python bench/accuracy/check_post_battery.py

Exit codes:
  0 — all checks pass
  1 — at least one capability assertion failed
  2 — infrastructure failure (binary missing, fixture missing, DB error)
"""
from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
FIXTURE_ROOT = REPO_ROOT / "bench" / "accuracy" / "synthetic" / "post-battery"
GROUND_TRUTH = FIXTURE_ROOT / "ground_truth.json"

_BIN_DIR = REPO_ROOT / "bin"
_BINARY_CANDIDATES = [
    _BIN_DIR / "code-graph.exe",
    _BIN_DIR / "code-graph",
]
BINARY = next((p for p in _BINARY_CANDIDATES if p.exists()), _BINARY_CANDIDATES[0])


def project_for_path(p: Path) -> str:
    """Mirror Go binary's pipeline.ProjectNameFromPath exactly.

    Duplicated from check_negative_fixtures.py to keep this script
    self-contained — the bench dir does not have a shared library
    on PYTHONPATH at gate-execution time.
    """
    s = str(p)
    if len(s) >= 2 and s[1] == ":":
        s = s[0].lower() + s[1:]
    s = s.replace("\\", "-").replace("/", "-").replace(":", "-")
    while "--" in s:
        s = s.replace("--", "-")
    s = s.lstrip("-")
    return s or "root"


def db_path_for(project: str) -> Path:
    return Path.home() / ".cache" / "code-graph" / f"{project}.db"


def index_fixture() -> None:
    if not BINARY.exists():
        sys.stderr.write(f"binary not found: {BINARY}; run `make build` first\n")
        raise SystemExit(2)
    if not FIXTURE_ROOT.exists():
        sys.stderr.write(f"fixture not found: {FIXTURE_ROOT}\n")
        raise SystemExit(2)
    args = json.dumps({
        "repo_path": str(FIXTURE_ROOT.resolve()),
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
            f"index_repository failed (rc={proc.returncode}): "
            f"{proc.stderr.decode('utf-8', errors='replace')[:500]}\n"
        )
        raise SystemExit(2)


def count_edges_by_type(project: str, edge_type: str) -> int:
    db = db_path_for(project)
    if not db.exists():
        sys.stderr.write(f"DB not found: {db}\n")
        raise SystemExit(2)
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    try:
        (n,) = con.execute(
            "SELECT COUNT(*) FROM edges WHERE project = ? AND type = ?",
            (project, edge_type),
        ).fetchone()
        return int(n)
    finally:
        con.close()


def count_rationale_by_kind(project: str, kind: str) -> int:
    db = db_path_for(project)
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    try:
        (n,) = con.execute(
            """
            SELECT COUNT(*) FROM nodes
            WHERE project = ? AND label = 'Rationale'
              AND json_extract(properties, '$.kind') = ?
            """,
            (project, kind),
        ).fetchone()
        return int(n)
    finally:
        con.close()


def main() -> int:
    with GROUND_TRUTH.open("r", encoding="utf-8") as f:
        gt = json.load(f)

    index_fixture()
    project = project_for_path(FIXTURE_ROOT.resolve())

    checks = [
        (
            "HTTP_CALLS",
            count_edges_by_type(project, "HTTP_CALLS"),
            int(gt["expected_http_calls_min"]),
            "PR #251 SVG filter / reqwest extractor: regressing this means HTTP_CALLS went silent",
        ),
        (
            "IMPLEMENTS",
            count_edges_by_type(project, "IMPLEMENTS"),
            int(gt["expected_implements_min"]),
            "Rust impl Trait extraction: regressing this means impl-block coverage broke",
        ),
        (
            "HANDLES",
            count_edges_by_type(project, "HANDLES"),
            int(gt["expected_handles_min"]),
            "PR #250 axum builder routes: regressing this means route extraction broke",
        ),
        (
            "Rationale[SAFETY]",
            count_rationale_by_kind(project, "SAFETY"),
            int(gt["expected_safety_rationale_min"]),
            "Rationale extractor: regressing this means SAFETY/WHY/HACK comments stopped becoming nodes",
        ),
    ]

    failed = 0
    for name, observed, floor, hint in checks:
        ok = observed >= floor
        status = "PASS" if ok else "FAIL"
        print(f"  [{status}] {name}: observed={observed} floor={floor}")
        if not ok:
            print(f"         hint: {hint}")
            failed += 1

    if failed:
        print(f"\n{failed} of {len(checks)} capability assertions failed.")
        return 1
    print(f"\nAll {len(checks)} capability assertions passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
