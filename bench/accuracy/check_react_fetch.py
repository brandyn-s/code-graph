"""react-fetch fixture gate (Phase C2).

Indexes `bench/accuracy/synthetic/react-fetch/` and asserts that
each of the three TS/JSX fetch URL shapes (literal, template-literal
with prefix, template-literal with id slot) produces an HTTP_CALLS
edge to the matching axum handler in the sibling server crate.

Run via:
  python bench/accuracy/check_react_fetch.py

Exit codes:
  0 — all expected edges present
  1 — at least one expected edge missing
  2 — infrastructure failure (binary missing, fixture missing, DB error)
"""
from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
FIXTURE_ROOT = REPO_ROOT / "bench" / "accuracy" / "synthetic" / "react-fetch"
GROUND_TRUTH = FIXTURE_ROOT / "ground_truth.json"

_BIN_DIR = REPO_ROOT / "bin"
_BINARY_CANDIDATES = [
    _BIN_DIR / "codebase-memory-mcp.exe",
    _BIN_DIR / "codebase-memory-mcp",
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
    return Path.home() / ".cache" / "codebase-memory-mcp" / f"{project}.db"


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

    floor = int(gt["expected_http_calls_min"])
    print(f"  HTTP_CALLS observed: {len(edges)}; floor: {floor}")

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
            print(f"  [FAIL] {url_path}: {from_suffix} -> {to_suffix}")
            print(
                f"         expected edge missing. observed edges from this caller: "
                f"{[e for e in edges if e['from_qn'].endswith(from_suffix)]}"
            )
            failed += 1

    if len(edges) < floor:
        print(
            f"  [FAIL] HTTP_CALLS count {len(edges)} below floor {floor}"
        )
        failed += 1

    if failed:
        print(f"\n{failed} assertion(s) failed.")
        return 1
    print(f"\nAll assertions passed ({len(edges)} HTTP_CALLS, floor {floor}).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
