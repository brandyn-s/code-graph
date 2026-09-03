"""Synthetic-fixture gate for caller_node_kind on go-minimal.

Validates that the resolver populates `caller_node_kind` on every emitted
CALLS-family edge in `bench/accuracy/synthetic/go-minimal/`, and that
the kinds match the hand-enumerable shape of the fixture.

Step 3 of the 2026-05-02 plateau-2 plan. Acts as the prove-the-instrument
gate per `rules/verify-effectiveness.md` — if the indexed fixture
produces an edge with NULL or unexpected caller_node_kind, this script
exits 1 and the resolver change is not safe to ship to a real-fixture
baseline.

What this gate checks
---------------------

1. **Every CALLS-family edge emitted has a non-empty `caller_node_kind`.**
   NULL means the resolver bypassed the property-population path and the
   instrument is leaky.

2. **`pkg_block_caller_FP_rate = 0` on this clean fixture.** The fixture
   is hand-authored: no `init()`, no package-level var initializer with
   call RHS, no top-level call statements. Every edge MUST classify as
   `function-body`. Any edge with kind in {file-block, package-init-block,
   type-decl, var-init} on this fixture indicates classification logic
   that misfires on simple Go.

3. **Every emitted CALLS-family edge classifies as `function-body`.**
   This is the strict form of (2): not just "no ghost kinds present"
   but "every kind == function-body" since the fixture has no methods,
   no tests, no init, no closures.

The fixture's exact QN format is determined by code-graph's
`pipeline.ProjectNameFromPath` sanitizer — fragile to assert against,
so this gate doesn't try; it asserts kind PROPERTIES on whatever
edges the resolver emits. Edge-set equivalence is checked separately
by the existing oracle harness (compare.py).

Exit codes
----------
0 — all CALLS-family edges have kind populated and classify as function-body
1 — at least one violation; details printed
2 — index_repository / DB access failed (binary error, fixture missing, etc.)

Usage
-----
  python bench/accuracy/check_go_minimal_caller_kind.py
"""
from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
FIXTURE_ROOT = REPO_ROOT / "bench" / "accuracy" / "synthetic" / "go-minimal"
GROUND_TRUTH = FIXTURE_ROOT / "ground_truth.json"
BINARY = REPO_ROOT / "bin" / "code-graph.exe"

# Kinds that indicate a "ghost caller" — package-level scope rather
# than a real function/method body. Any of these on the synthetic
# fixture is a classification bug.
PKG_BLOCK_KINDS = {"file-block", "package-init-block", "type-decl", "var-init"}

# CALLS-family edge types we consider as "the resolver emitted a call."
CALLS_FAMILY = {"CALLS", "CALLS_EXTERNAL", "CALLS_PSEUDO", "INDIRECT_CALLS"}


def project_for_path(p: Path) -> str:
    """Mirror Go binary's pipeline.ProjectNameFromPath exactly."""
    s = str(p)
    if len(s) >= 2 and s[1] == ":":
        s = s[0].lower() + s[1:]
    s = s.replace("\\", "-").replace("/", "-").replace(":", "-")
    while "--" in s:
        s = s.replace("--", "-")
    s = s.lstrip("-")
    return s or "root"


def index_fixture() -> None:
    args = json.dumps({"repo_path": str(FIXTURE_ROOT.resolve()), "mode": "full"})
    proc = subprocess.run(
        [str(BINARY), "cli", "index_repository", args],
        capture_output=True,
        timeout=120,
    )
    if proc.returncode != 0:
        sys.stderr.write(
            f"index_repository failed (rc={proc.returncode}): "
            f"{proc.stderr.decode('utf-8', errors='replace')[:500]}\n"
        )
        raise SystemExit(2)


def db_path_for(project: str) -> Path:
    home = Path.home()
    return home / ".cache" / "code-graph" / f"{project}.db"


def fetch_calls_with_kind(project: str) -> list[dict]:
    """Read every CALLS-family edge in the project DB with caller kind."""
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
                   json_extract(e.properties, '$.caller_node_kind') AS caller_node_kind
            FROM edges e
            JOIN nodes src ON e.source_id = src.id
            JOIN nodes tgt ON e.target_id = tgt.id
            WHERE e.project = ?
              AND e.type IN ({','.join(repr(t) for t in CALLS_FAMILY)})
            """,
            (project,),
        ).fetchall()
    finally:
        con.close()
    return [dict(r) for r in rows]


def main() -> int:
    if not BINARY.exists():
        sys.stderr.write(
            f"Binary not built: {BINARY}\n"
            "Run: CGO_ENABLED=1 go build -o bin/code-graph.exe ./cmd/code-graph/\n"
        )
        return 2

    if not GROUND_TRUTH.exists():
        sys.stderr.write(f"Ground truth missing: {GROUND_TRUTH}\n")
        return 2

    print(f"[gate] indexing {FIXTURE_ROOT}")
    index_fixture()

    project = project_for_path(FIXTURE_ROOT)
    print(f"[gate] project name: {project}")

    rows = fetch_calls_with_kind(project)
    print(f"[gate] {len(rows)} CALLS-family edges emitted")

    if not rows:
        print("[gate] FAIL: no CALLS-family edges emitted on a fixture that should produce 5")
        return 1

    failures: list[str] = []

    # 1. Every emitted CALLS-family edge must have a non-empty caller_node_kind.
    missing_kind = [r for r in rows if not r.get("caller_node_kind")]
    for r in missing_kind:
        failures.append(
            f"NULL caller_node_kind: ({r['edge_type']}) "
            f"{r['from_qn'][-60:]} -> {r['to_qn'][-60:]}"
        )

    # 2. pkg_block_caller_FP_rate must be 0 on this clean fixture.
    pkg_block = [r for r in rows if r.get("caller_node_kind") in PKG_BLOCK_KINDS]
    for r in pkg_block:
        failures.append(
            f"unexpected pkg-block kind on clean fixture: ({r['edge_type']}) "
            f"{r['from_qn'][-60:]} -> {r['to_qn'][-60:]} "
            f"kind={r['caller_node_kind']}"
        )

    # 3. Every kind on this fixture should be function-body. The fixture
    #    has no methods, no tests, no init, no var initializers.
    expected_kind = "function-body"
    wrong_kind = [
        r for r in rows
        if r.get("caller_node_kind")
        and r["caller_node_kind"] not in {expected_kind, None, ""}
        and r["caller_node_kind"] not in PKG_BLOCK_KINDS  # already counted above
    ]
    for r in wrong_kind:
        failures.append(
            f"unexpected kind on free-function fixture: ({r['edge_type']}) "
            f"{r['from_qn'][-60:]} -> {r['to_qn'][-60:]} "
            f"kind={r['caller_node_kind']} (expected {expected_kind})"
        )

    if failures:
        print(f"\n[gate] FAIL: {len(failures)} issue(s)")
        for f in failures:
            print(f"  - {f}")
        return 1

    kinds_present = sorted({r["caller_node_kind"] for r in rows if r.get("caller_node_kind")})
    print(
        f"\n[gate] PASS — {len(rows)} CALLS-family edges, every edge has caller_node_kind, "
        f"pkg_block_caller_FP_rate=0.0, kinds_present={kinds_present}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
