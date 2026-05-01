"""Synthetic-fixture gate for resolver_rule on go-minimal.

Validates that the resolver populates `resolver_rule` on every emitted
CALLS-family edge in `bench/accuracy/synthetic/go-minimal/`, and that
the rules match the hand-enumerable shape of the fixture.

Step 4 of the 2026-05-02 plateau-2 plan. Acts as the prove-the-instrument
gate per `rules/verify-effectiveness.md` — if the indexed fixture
produces an edge with NULL or unexpected resolver_rule, this script
exits 1 and the resolver change is not safe to ship to a real-fixture
baseline.

What this gate checks
---------------------

1. **Every CALLS-family edge emitted has a non-empty `resolver_rule`.**
   NULL means the resolver bypassed the property-population path and
   the instrument is leaky.

2. **No `forbidden_rules` appear on this clean fixture.** The fixture
   has no methods, no init, no closures — `fuzzy-resolve`,
   `unresolved-emitted`, `unknown`, `modal-pseudo`, and
   `package-block-fallback` should never fire on this input.

3. **At least one rule from `expected_rules_subset` is present.**
   This protects against the resolver populating the property with
   only `unknown` (which trips check #2 anyway, but assert positively).

4. **modality_mix_gini computes a finite value.** Imports and runs
   compute_modality_metrics on the fixture's edges to confirm the
   per-project Gini metric produces a real number, not NaN or error.

The fixture's exact QN format is determined by code-graph's
`pipeline.ProjectNameFromPath` sanitizer — fragile to assert against,
so this gate doesn't try; it asserts rule PROPERTIES on whatever
edges the resolver emits. Edge-set equivalence is checked separately
by the existing oracle harness (compare.py).

Exit codes
----------
0 — all CALLS-family edges have rule populated, no forbidden rules
    fire, modality_mix_gini computes finite
1 — at least one violation; details printed
2 — index_repository / DB access failed (binary error, fixture missing, etc.)

Usage
-----
  python bench/accuracy/check_go_minimal_resolver_rule.py
"""
from __future__ import annotations

import json
import math
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
    # rather than re-using a pre-PR DB that lacks resolver_rule.
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


def fetch_calls_with_rule(project: str) -> list[dict]:
    """Read every CALLS-family edge in the project DB with resolver rule."""
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
                   json_extract(e.properties, '$.resolver_rule') AS resolver_rule,
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


def check_modality_gini(rows: list[dict], project: str) -> tuple[bool, str]:
    """Run compute_modality_metrics on the fixture and verify Gini is finite."""
    sys.path.insert(0, str(REPO_ROOT / "bench" / "accuracy"))
    try:
        from compare import compute_modality_metrics  # type: ignore[import-not-found]  # noqa: E402
    except Exception as exc:
        return False, f"failed to import compare.compute_modality_metrics: {exc}"

    # Build the inputs compute_modality_metrics expects. For the
    # fixture all edges count as TPs (we're not running the oracle
    # here — that's compare.py's job). We just need to confirm the
    # Gini computation runs and returns a finite value.
    tp_scoped = set()
    fp_scoped = set()
    resolver_rules: dict = {}
    caller_kinds: dict = {}
    for r in rows:
        key = (r["from_qn"], r["to_qn"], r["edge_type"])
        tp_scoped.add(key)
        if r.get("resolver_rule"):
            resolver_rules[key] = r["resolver_rule"]
        if r.get("caller_node_kind"):
            caller_kinds[key] = r["caller_node_kind"]

    out = compute_modality_metrics(
        tp_scoped, fp_scoped, resolver_rules, caller_kinds, [project]
    )
    gini_map = out.get("modality_mix_gini") or {}
    if project not in gini_map:
        return False, f"modality_mix_gini missing project {project!r}; got {gini_map!r}"
    g = gini_map[project]
    if not isinstance(g, (int, float)):
        return False, f"modality_mix_gini[{project!r}] not numeric: {g!r}"
    if not math.isfinite(g):
        return False, f"modality_mix_gini[{project!r}] not finite: {g!r}"
    return True, f"modality_mix_gini[{project!r}] = {g}"


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
    inv = gt.get("expected_resolver_rule_invariants") or {}
    forbidden = set(inv.get("forbidden_rules") or [])
    expected_subset = set(inv.get("expected_rules_subset") or [])

    print(f"[gate] indexing {FIXTURE_ROOT}")
    index_fixture()

    project = project_for_path(FIXTURE_ROOT)
    print(f"[gate] project name: {project}")

    rows = fetch_calls_with_rule(project)
    print(f"[gate] {len(rows)} CALLS-family edges emitted")

    if not rows:
        print("[gate] FAIL: no CALLS-family edges emitted on a fixture that should produce 5")
        return 1

    failures: list[str] = []

    # 1. Every emitted CALLS-family edge must have a non-empty resolver_rule.
    missing_rule = [r for r in rows if not r.get("resolver_rule")]
    for r in missing_rule:
        failures.append(
            f"NULL resolver_rule: ({r['edge_type']}) "
            f"{r['from_qn'][-60:]} -> {r['to_qn'][-60:]}"
        )

    # 2. No forbidden_rules on this clean fixture.
    forbidden_hits = [r for r in rows if r.get("resolver_rule") in forbidden]
    for r in forbidden_hits:
        failures.append(
            f"forbidden rule on clean fixture: ({r['edge_type']}) "
            f"{r['from_qn'][-60:]} -> {r['to_qn'][-60:]} "
            f"rule={r['resolver_rule']}"
        )

    # 3. At least one rule from expected_subset must be present.
    rules_present = {r["resolver_rule"] for r in rows if r.get("resolver_rule")}
    if expected_subset and not (rules_present & expected_subset):
        failures.append(
            f"no rule from expected subset present; rules_present={sorted(rules_present)} "
            f"expected_subset={sorted(expected_subset)}"
        )

    # 4. modality_mix_gini must compute a finite value.
    ok, msg = check_modality_gini(rows, project)
    if not ok:
        failures.append(f"modality_mix_gini check failed: {msg}")
    else:
        print(f"[gate] {msg}")

    if failures:
        print(f"\n[gate] FAIL: {len(failures)} issue(s)")
        for f in failures:
            print(f"  - {f}")
        return 1

    print(
        f"\n[gate] PASS — {len(rows)} CALLS-family edges, every edge has resolver_rule, "
        f"no forbidden rules, rules_present={sorted(rules_present)}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
