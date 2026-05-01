"""Synthetic-fixture gate for candidate_set_size on go-minimal.

Validates that the resolver populates `candidate_set_size` on every emitted
CALLS-family edge in `bench/accuracy/synthetic/go-minimal/`, and that the
aggregate `method_set_ambiguity_index` matches the fixture's expected
value.

Step 5 of the 2026-05-02 plateau-2 plan. Acts as the prove-the-instrument
gate per `rules/verify-effectiveness.md` — if the indexed fixture
produces an edge with NULL `candidate_set_size`, this script exits 1 and
the resolver change is not safe to ship to a real-fixture baseline.

What this gate checks
---------------------

1. **Every CALLS-family edge emitted has a non-NULL `candidate_set_size`.**
   NULL means the resolver bypassed the property-population path and
   the instrument is leaky.

2. **`candidate_set_size` is in the expected range** (min/max). On the
   unaugmented fixture every callee is uniquely named project-wide and
   every receiver is unambiguous, so every edge MUST carry size=1.

3. **`method_set_ambiguity_index == expected`** (0.0 on the unaugmented
   fixture). Imports and runs `compute_ambiguity_metrics` on the
   fixture's edges to confirm the index matches the ground-truth value.

4. **`janusian_site_precision_split` is computable.** On the
   unaugmented fixture the `ambiguous` bucket has support=0 and the
   `unambiguous` bucket has support=5. The split itself must still
   compute (no exceptions, both buckets present in the result dict).

The fixture's exact QN format is determined by code-graph's
`pipeline.ProjectNameFromPath` sanitizer — fragile to assert against,
so this gate doesn't try; it asserts size PROPERTIES on whatever edges
the resolver emits. Edge-set equivalence is checked separately by the
existing oracle harness (compare.py).

Exit codes
----------
0 — every CALLS-family edge has candidate_set_size populated, sizes
    fall within expected min/max, method_set_ambiguity_index matches
    the ground-truth expectation
1 — at least one violation; details printed
2 — index_repository / DB access failed (binary error, fixture missing, etc.)

Usage
-----
  python bench/accuracy/check_go_minimal_candidate_set.py
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
BINARY = REPO_ROOT / "bin" / "codebase-memory-mcp.exe"

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
    # force=True ensures the indexer ignores cached file hashes — needed
    # so the gate exercises the post-PR resolver against current source
    # rather than re-using a pre-PR DB that lacks candidate_set_size.
    args = json.dumps({"repo_path": str(FIXTURE_ROOT.resolve()), "mode": "full", "force": True})
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
    return home / ".cache" / "codebase-memory-mcp" / f"{project}.db"


def fetch_calls_with_candidate_set(project: str) -> list[dict]:
    """Read every CALLS-family edge in the project DB with candidate set size."""
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
                   json_extract(e.properties, '$.candidate_set_size') AS candidate_set_size
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


def check_ambiguity_metrics(rows: list[dict], project: str, expected_index: float) -> tuple[bool, str]:
    """Run compute_ambiguity_metrics on the fixture and verify the index."""
    sys.path.insert(0, str(REPO_ROOT / "bench" / "accuracy"))
    try:
        from compare import compute_ambiguity_metrics  # type: ignore[import-not-found]  # noqa: E402
    except Exception as exc:
        return False, f"failed to import compare.compute_ambiguity_metrics: {exc}"

    # Build inputs. For the fixture all emitted edges count as TPs (we're
    # not running the oracle here; that's compare.py's job). We just need
    # to confirm the metrics computation runs and returns the expected
    # ambiguity index.
    tp_scoped: set = set()
    fp_scoped: set = set()
    candidate_sets: dict = {}
    for r in rows:
        key = (r["from_qn"], r["to_qn"], r["edge_type"])
        tp_scoped.add(key)
        size = r.get("candidate_set_size")
        if size is None:
            continue
        try:
            candidate_sets[key] = int(size)
        except (TypeError, ValueError):
            continue

    out = compute_ambiguity_metrics(
        tp_scoped, fp_scoped, candidate_sets, [project]
    )
    if "ambiguity_note" in out:
        return False, f"ambiguity metrics returned skip note: {out['ambiguity_note']}"

    mai = out.get("method_set_ambiguity_index") or {}
    if project not in mai:
        return False, f"method_set_ambiguity_index missing project {project!r}; got {mai!r}"
    cell = mai[project]
    actual_index = cell.get("value")
    if actual_index is None:
        return False, f"method_set_ambiguity_index[{project!r}] has no value field: {cell!r}"
    if abs(actual_index - expected_index) > 1e-9:
        return False, (
            f"method_set_ambiguity_index[{project!r}] = {actual_index!r}, "
            f"expected {expected_index!r}"
        )

    split = out.get("janusian_site_precision_split") or {}
    if "ambiguous" not in split or "unambiguous" not in split:
        return False, f"janusian_site_precision_split missing buckets: {split!r}"

    return True, (
        f"method_set_ambiguity_index[{project!r}] = {actual_index} "
        f"({cell.get('ambiguous_sites', 0)} ambiguous of "
        f"{cell.get('total_sites', 0)} sites); "
        f"split.ambiguous.support={split['ambiguous'].get('support', 0)}, "
        f"split.unambiguous.support={split['unambiguous'].get('support', 0)}"
    )


def main() -> int:
    if not BINARY.exists():
        sys.stderr.write(
            f"Binary not built: {BINARY}\n"
            "Run: CGO_ENABLED=1 go build -o bin/codebase-memory-mcp.exe ./cmd/codebase-memory-mcp/\n"
        )
        return 2

    if not GROUND_TRUTH.exists():
        sys.stderr.write(f"Ground truth missing: {GROUND_TRUTH}\n")
        return 2

    with GROUND_TRUTH.open("r", encoding="utf-8") as f:
        gt = json.load(f)
    inv = gt.get("expected_candidate_set_invariants") or {}
    expected_index = float(inv.get("method_set_ambiguity_index_expected", 0.0))
    min_size = int(inv.get("min_candidate_set_size", 1))
    max_size = int(inv.get("max_candidate_set_size", 1))

    print(f"[gate] indexing {FIXTURE_ROOT}")
    index_fixture()

    project = project_for_path(FIXTURE_ROOT)
    print(f"[gate] project name: {project}")

    rows = fetch_calls_with_candidate_set(project)
    print(f"[gate] {len(rows)} CALLS-family edges emitted")

    if not rows:
        print("[gate] FAIL: no CALLS-family edges emitted on a fixture that should produce 5")
        return 1

    failures: list[str] = []

    # 1. Every emitted CALLS-family edge must have non-NULL candidate_set_size.
    missing_size = [r for r in rows if r.get("candidate_set_size") is None]
    for r in missing_size:
        failures.append(
            f"NULL candidate_set_size: ({r['edge_type']}) "
            f"{r['from_qn'][-60:]} -> {r['to_qn'][-60:]}"
        )

    # 2. Every size must fall within expected min/max for this fixture.
    for r in rows:
        size = r.get("candidate_set_size")
        if size is None:
            continue  # already counted above
        try:
            size_int = int(size)
        except (TypeError, ValueError):
            failures.append(
                f"non-int candidate_set_size: ({r['edge_type']}) "
                f"{r['from_qn'][-60:]} -> {r['to_qn'][-60:]} "
                f"size={size!r}"
            )
            continue
        if size_int < min_size or size_int > max_size:
            failures.append(
                f"candidate_set_size out of range [{min_size},{max_size}]: "
                f"({r['edge_type']}) {r['from_qn'][-60:]} -> "
                f"{r['to_qn'][-60:]} size={size_int}"
            )

    # 3. method_set_ambiguity_index must match the expected value.
    ok, msg = check_ambiguity_metrics(rows, project, expected_index)
    if not ok:
        failures.append(f"ambiguity_metrics check failed: {msg}")
    else:
        print(f"[gate] {msg}")

    if failures:
        print(f"\n[gate] FAIL: {len(failures)} issue(s)")
        for f in failures:
            print(f"  - {f}")
        return 1

    sizes_present = sorted({int(r["candidate_set_size"]) for r in rows if r.get("candidate_set_size") is not None})
    print(
        f"\n[gate] PASS — {len(rows)} CALLS-family edges, every edge has "
        f"candidate_set_size, sizes_present={sizes_present}, "
        f"method_set_ambiguity_index={expected_index}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
