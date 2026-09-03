"""handler-resolution adversarial fixture gate (Phase D1).

Indexes `bench/accuracy/synthetic/handler-resolution/` and asserts
that when two modules share a function name (`list_users`), the
HTTP_CALLS edge is routed to the one in the SAME module as the route
declaration — not the unrelated decoy and not the route-declaring
function itself.

Run via:
  python bench/accuracy/check_handler_resolution.py

Exit codes:
  0 — handler resolved to the real handler; no edges to forbidden targets
  1 — assertion failed
  2 — infrastructure failure
"""
from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
FIXTURE_ROOT = REPO_ROOT / "bench" / "accuracy" / "synthetic" / "handler-resolution"
GROUND_TRUTH = FIXTURE_ROOT / "ground_truth.json"

_BIN_DIR = REPO_ROOT / "bin"
_BINARY_CANDIDATES = [
    _BIN_DIR / "code-graph.exe",
    _BIN_DIR / "code-graph",
]
BINARY = next((p for p in _BINARY_CANDIDATES if p.exists()), _BINARY_CANDIDATES[0])


def project_for_path(p: Path) -> str:
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


def fetch_http_calls(project: str) -> list[dict]:
    db = db_path_for(project)
    if not db.exists():
        sys.stderr.write(f"DB not found: {db}\n")
        raise SystemExit(2)
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    try:
        rows = con.execute(
            """
            SELECT src.qualified_name AS from_qn,
                   tgt.qualified_name AS to_qn,
                   json_extract(e.properties, '$.url_path') AS url_path
            FROM edges e
            JOIN nodes src ON e.source_id = src.id
            JOIN nodes tgt ON e.target_id = tgt.id
            WHERE e.project = ? AND e.type = 'HTTP_CALLS'
            """,
            (project,),
        ).fetchall()
    finally:
        con.close()
    return [dict(r) for r in rows]


def main() -> int:
    with GROUND_TRUTH.open("r", encoding="utf-8") as f:
        gt = json.load(f)

    index_fixture()
    project = project_for_path(FIXTURE_ROOT.resolve())
    edges = fetch_http_calls(project)

    print(f"  HTTP_CALLS observed: {len(edges)}")

    failed = 0
    for expected in gt["expected_http_call_edges"]:
        from_suffix = expected["from_qn_suffix"]
        to_suffix = expected["to_qn_suffix"]
        url_path = expected["url_path"]
        match = None
        for e in edges:
            if (
                e["from_qn"].endswith(from_suffix)
                and e["to_qn"].endswith(to_suffix)
                and e["url_path"] == url_path
            ):
                match = e
                break
        if match:
            print(f"  [PASS] {url_path}: {from_suffix} -> {to_suffix}")
        else:
            print(f"  [FAIL] expected edge missing: {url_path}: {from_suffix} -> {to_suffix}")
            print(
                f"         observed edges: {edges}"
            )
            failed += 1

    for forbidden_suffix in gt.get("forbidden_to_qn_suffixes", []):
        bad = [e for e in edges if e["to_qn"].endswith(forbidden_suffix)]
        if bad:
            print(f"  [FAIL] forbidden target hit: {forbidden_suffix}")
            print(f"         offending edges: {bad}")
            failed += 1
        else:
            print(f"  [PASS] no edges to forbidden target {forbidden_suffix}")

    if failed:
        print(f"\n{failed} assertion(s) failed.")
        return 1
    print(f"\nAll handler-resolution assertions passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
