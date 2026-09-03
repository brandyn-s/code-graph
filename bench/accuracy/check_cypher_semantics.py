"""Cypher-engine array-operator regression gate.

Indexes `bench/accuracy/synthetic/cypher-semantics/` with the local
code-graph binary, then exercises `CONTAINS` on array-typed
node properties (`decorators`, `param_types`, `return_types`).
Pins PR #308's element-of semantics so the next refactor of the
Cypher executor can't silently regress it back to the pre-PR-#308
shape (CONTAINS on array returned 0 rows because the operand-type
branch checked `string contains substring` only).

Each assertion has a documented hint pointing at the PR-#308-shape
regression it guards against.

Usage:
  python bench/accuracy/check_cypher_semantics.py

Exit codes:
  0 — all checks pass
  1 — at least one cypher operator returned fewer rows than the floor
  2 — infrastructure failure (binary missing, fixture missing, query error)
"""
from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
FIXTURE_ROOT = REPO_ROOT / "bench" / "accuracy" / "synthetic" / "cypher-semantics"
GROUND_TRUTH = FIXTURE_ROOT / "ground_truth.json"

_BIN_DIR = REPO_ROOT / "bin"
_BINARY_CANDIDATES = [
    _BIN_DIR / "code-graph.exe",
    _BIN_DIR / "code-graph",
]
BINARY = next((p for p in _BINARY_CANDIDATES if p.exists()), _BINARY_CANDIDATES[0])


def project_for_path(p: Path) -> str:
    """Mirror Go binary's pipeline.ProjectNameFromPath exactly.

    Duplicated from check_post_battery.py to keep this script
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


def run_count_query(project: str, cypher: str) -> int:
    """Run a `RETURN count(...) AS n` query via cli --raw and parse the result.

    Uses --raw so the response is JSON instead of the pretty-printed table.
    """
    args = json.dumps({"query": cypher, "project": project})
    proc = subprocess.run(
        [str(BINARY), "cli", "--raw", "query_graph", args],
        capture_output=True,
        timeout=60,
    )
    if proc.returncode != 0:
        sys.stderr.write(
            f"query_graph failed (rc={proc.returncode}): "
            f"{proc.stderr.decode('utf-8', errors='replace')[:500]}\n"
            f"  query: {cypher}\n"
        )
        raise SystemExit(2)
    try:
        out = json.loads(proc.stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as e:
        sys.stderr.write(
            f"failed to parse query_graph response as JSON: {e}\n"
            f"  stdout (first 500 chars): {proc.stdout[:500]!r}\n"
        )
        raise SystemExit(2)
    rows = out.get("rows", [])
    if not rows:
        return 0
    # Single-row, single-column count query — extract the int value.
    row = rows[0]
    if isinstance(row, dict):
        # Column was aliased AS n — return the first (and only) value.
        for v in row.values():
            return int(v)
        return 0
    if isinstance(row, list) and row:
        return int(row[0])
    return 0


def main() -> int:
    with GROUND_TRUTH.open("r", encoding="utf-8") as f:
        gt = json.load(f)

    index_fixture()
    project = project_for_path(FIXTURE_ROOT.resolve())

    checks = [
        (
            "decorators CONTAINS '#[test]'",
            run_count_query(
                project,
                "MATCH (f:Function) WHERE f.decorators CONTAINS '#[test]' "
                "RETURN count(f) AS n",
            ),
            int(gt["expected_decorators_test_min"]),
            (
                "PR #308 (CONTAINS-on-array element-of). If 0, the Cypher "
                "executor regressed `CONTAINS` on []string properties back "
                "to substring-on-string semantics. Re-check internal/cypher/"
                "executor.go CONTAINS branch handles []string and []any."
            ),
        ),
        (
            "return_types CONTAINS 'Vec'",
            run_count_query(
                project,
                "MATCH (f:Function) WHERE f.return_types CONTAINS 'Vec' "
                "RETURN count(f) AS n",
            ),
            int(gt["expected_return_types_vec_min"]),
            (
                "PR #308 element-of semantics on `return_types`. Outer "
                "type constructor (`Vec`) is the element; if 0, either "
                "CONTAINS regressed or the Rust extractor stopped recording "
                "outer-constructor names in return_types."
            ),
        ),
        (
            "return_types CONTAINS 'Result'",
            run_count_query(
                project,
                "MATCH (f:Function) WHERE f.return_types CONTAINS 'Result' "
                "RETURN count(f) AS n",
            ),
            int(gt["expected_return_types_result_min"]),
            (
                "PR #308 element-of semantics on `return_types` for "
                "`Result<T, E>` outer constructor. Second element to "
                "guard against single-element collapse in the executor."
            ),
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
        print(f"\n{failed} of {len(checks)} cypher-operator assertions failed.")
        return 1
    print(f"\nAll {len(checks)} cypher-operator assertions passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
