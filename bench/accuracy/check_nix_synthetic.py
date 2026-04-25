"""Synthetic-fixture regression gate for the Nix pub/sub extractor.

Indexes `bench/accuracy/synthetic/nix-pubsub-minimal/` and asserts the
extracted edges exactly match `ground_truth.json`. Exit code 1 on any FN
or FP — used as a CI gate on PRs touching nix_services.go or zenoh.go.

This is the smallest possible fixture that exercises:
  - declarative baf.pub_topic (alphad)
  - declarative baf.sub_topics list (alphad)
  - imperative `pubmsg <literal>` (betad, no mkOption)
  - cross-file additional_sub_topics (betad → alphad gets "extra-cross-file")

The gate runs in CI without the live MCP server — it shells out to
`bin/codebase-memory-mcp.exe cli` directly, then queries the resulting
SQLite DB.
"""

from __future__ import annotations

import argparse
import json
import sqlite3
import subprocess
import sys
from pathlib import Path


def index_fixture(binary: Path, fixture_root: Path) -> None:
    """Run the indexer on the fixture. Re-runs are idempotent because each
    invocation deletes-then-inserts."""
    args = json.dumps({"repo_path": str(fixture_root.resolve()), "mode": "full"})
    result = subprocess.run(
        [str(binary), "cli", "index_repository", args],
        capture_output=True, text=True, timeout=120,
    )
    if result.returncode != 0:
        sys.stderr.write(f"index_repository failed: {result.stderr}\n")
        raise SystemExit(2)


def project_for_path(p: Path) -> str:
    """Mirror code-graph's project name sanitization. On Windows the path
    starts `C:\\Users\\...`; code-graph maps that to `c-Users-...`."""
    s = str(p)
    # Lowercase drive letter so `C:` → `c:` → `c-`.
    if len(s) >= 2 and s[1] == ":":
        s = s[0].lower() + s[1:]
    s = s.replace("\\", "-").replace("/", "-").replace(":", "-")
    # Collapse any double dashes from the drive letter substitution.
    while "--" in s:
        s = s.replace("--", "-")
    return s


def query_actual(db_path: Path, project: str) -> tuple[set[tuple[str, str, str]], set[str]]:
    """Returns (set of (service, topic, edge_type) tuples, set of service names).
    Excludes AMBIGUOUS-tier edges from the strict check."""
    con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    try:
        rows = con.execute(
            """
            SELECT src.name AS service, tgt.name AS topic, e.type AS edge_type
            FROM edges e
            JOIN nodes src ON e.source_id = src.id
            JOIN nodes tgt ON e.target_id = tgt.id
            WHERE e.project = ?
              AND src.label = 'Service'
              AND tgt.label = 'Topic'
              AND e.type IN ('PUBLISHES_TO', 'SUBSCRIBES_TO')
              AND json_extract(e.properties, '$.confidence_tier') != 'AMBIGUOUS'
            """,
            (project,),
        ).fetchall()
        services = con.execute(
            "SELECT name FROM nodes WHERE project = ? AND label = 'Service'",
            (project,),
        ).fetchall()
    finally:
        con.close()
    edges = {(r["service"], r["topic"], r["edge_type"]) for r in rows}
    svc_names = {r["name"] for r in services}
    return edges, svc_names


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--binary", type=Path, required=True,
                    help="path to codebase-memory-mcp.exe")
    ap.add_argument("--fixture", type=Path, required=True,
                    help="path to bench/accuracy/synthetic/nix-pubsub-minimal/")
    ap.add_argument("--db-dir", type=Path,
                    default=Path.home() / ".cache" / "codebase-memory-mcp")
    args = ap.parse_args(argv[1:])

    fixture_root = args.fixture.resolve()
    project = project_for_path(fixture_root)
    db_path = args.db_dir / f"{project}.db"

    # Force fresh index (delete existing DB so the new pass runs).
    for ext in ("", "-shm", "-wal"):
        p = Path(f"{db_path}{ext}")
        if p.exists():
            p.unlink()

    print(f"Indexing fixture: {fixture_root}")
    index_fixture(args.binary, fixture_root)

    gt = json.loads((args.fixture / "ground_truth.json").read_text())

    expected_edges = set()
    for e in gt["expected_publishes_to"]:
        expected_edges.add((e["service"], e["topic"], "PUBLISHES_TO"))
    for e in gt["expected_subscribes_to"]:
        expected_edges.add((e["service"], e["topic"], "SUBSCRIBES_TO"))
    expected_services = set(gt["expected_services"])

    actual_edges, actual_services = query_actual(db_path, project)

    fn = expected_edges - actual_edges
    fp = actual_edges - expected_edges
    svc_missing = expected_services - actual_services
    svc_extra = actual_services - expected_services

    print(f"Project: {project}")
    print(f"Services:  expected={len(expected_services)}  got={len(actual_services)}")
    if svc_missing:
        print(f"  ! missing services: {sorted(svc_missing)}")
    if svc_extra:
        print(f"  ~ extra services:   {sorted(svc_extra)}")

    print(f"Edges:     expected={len(expected_edges)}  got={len(actual_edges)}")
    if fn:
        print("  ! missing edges (FN):")
        for e in sorted(fn):
            print(f"      {e[0]} -[{e[2]}]-> {e[1]}")
    if fp:
        print("  ! extra edges (FP):")
        for e in sorted(fp):
            print(f"      {e[0]} -[{e[2]}]-> {e[1]}")

    if fn or fp or svc_missing:
        print("REGRESSION: extraction does not match ground truth")
        return 1

    print("OK: synthetic fixture extraction matches ground truth")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
