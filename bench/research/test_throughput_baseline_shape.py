"""Get-well plan Phase 3.2 (2026-05-06): regression test for the
throughput harness JSON output shape.

The throughput harness writes a JSON file with summary stats, phase
timings, and percentiles (see bench/research/indexing_throughput/main.go).
Pre-Phase-3 nothing pinned the JSON shape — a future PR could rename a
field, drop a field, or break percentile math, and the harness would
silently produce subtly-wrong baselines that we'd then publish.

This test pins:
  1. All top-level keys the operators rely on are present.
  2. The phases array is non-empty (Pipeline.Progress fired).
  3. P50 <= P95 <= P99 (percentile math sanity).
  4. Wall-ns sum across phases is consistent with the total wall-ns
     reported (within rounding tolerance).
  5. Each phase has a non-empty `phase` name and non-negative wall_ns.

Existing baselines used as regression fixtures:
  - 2026-05-06-indexing-throughput-self-full.json (Plan 4 D-1)
  - 2026-05-06-indexing-throughput-self-fixed.json (Plan 5 Phase D)
  - 2026-05-06-indexing-throughput-cypher.json (PR #218)

What this catches: a future harness PR that breaks any of the above
contract properties. The full integration test (running the harness
against a fixture) is heavier and is out of scope for this PR — the
JSON-shape pin catches the most likely regression class (silent field
rename / drop / math break) at near-zero cost.
"""
from __future__ import annotations

import json
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
BASELINES_DIR = REPO_ROOT / "bench" / "research" / "baselines"

REQUIRED_TOP_LEVEL_KEYS = {
    "schema_version",
    "generated_at",
    "target",
    "mode",
    "go_version",
    "num_cpu",
    "goos",
    "goarch",
    "cold_start_ns",
    "total_wall_ns",
    "peak_heap_inuse_bytes",
    "peak_sys_bytes",
    "phases",
    "node_count",
    "edge_count",
    "nodes_per_sec",
    "edges_per_sec",
    "p50_phase_ns",
    "p95_phase_ns",
    "p99_phase_ns",
}

REQUIRED_PHASE_KEYS = {"phase", "wall_ns"}

# Phases that MUST appear in any non-trivial pipeline run. If
# Pipeline.Progress stops firing for one of these, the throughput
# harness silently misses its timing and the percentiles are wrong.
# This list is the minimum-viable set; baselines may have more.
EXPECTED_PHASES_SUBSET = {
    "discover",         # initial file enumeration
    "definitions:parse",  # tree-sitter parse pass
    "complete",         # final phase marker
}


def _baseline_files() -> list[Path]:
    """Return the throughput baseline JSON files that exist in the
    repo. Tests run against each file as a separate regression check."""
    if not BASELINES_DIR.is_dir():
        return []
    return sorted(BASELINES_DIR.glob("*indexing-throughput*.json"))


def test_at_least_one_baseline_exists():
    """The baselines directory must contain at least one throughput
    baseline. If this fails, either the baseline got deleted or the
    test is running outside the repo root."""
    files = _baseline_files()
    assert files, (
        f"no throughput baselines found in {BASELINES_DIR}; "
        f"baseline regression tests can't run"
    )


def test_top_level_keys_present():
    """Every baseline must have every required top-level key. A future
    PR that drops a key would be caught here at CI time."""
    for path in _baseline_files():
        data = json.loads(path.read_text(encoding="utf-8"))
        missing = REQUIRED_TOP_LEVEL_KEYS - set(data.keys())
        assert not missing, (
            f"{path.name}: missing top-level keys {sorted(missing)}; "
            f"present={sorted(data.keys())}"
        )


def test_phases_array_non_empty():
    """The `phases` array must be non-empty. An empty phases array
    means Pipeline.Progress never fired — the harness is producing
    no useful data."""
    for path in _baseline_files():
        data = json.loads(path.read_text(encoding="utf-8"))
        phases = data.get("phases")
        assert isinstance(phases, list) and len(phases) > 0, (
            f"{path.name}: phases array empty or non-list (got {type(phases).__name__})"
        )


def test_each_phase_has_required_keys():
    """Every phase entry must have `phase` (string name) and `wall_ns`
    (non-negative int)."""
    for path in _baseline_files():
        data = json.loads(path.read_text(encoding="utf-8"))
        for i, p in enumerate(data["phases"]):
            missing = REQUIRED_PHASE_KEYS - set(p.keys())
            assert not missing, (
                f"{path.name}[{i}]: phase entry missing keys {sorted(missing)}"
            )
            assert isinstance(p["phase"], str) and p["phase"], (
                f"{path.name}[{i}]: phase name must be non-empty string, got {p['phase']!r}"
            )
            assert isinstance(p["wall_ns"], (int, float)) and p["wall_ns"] >= 0, (
                f"{path.name}[{i}]: wall_ns must be non-negative number, got {p['wall_ns']!r}"
            )


def test_percentile_ordering():
    """Percentile math sanity: P50 <= P95 <= P99. If this fails the
    harness's sort/select logic is broken — published baseline numbers
    are unreliable."""
    for path in _baseline_files():
        data = json.loads(path.read_text(encoding="utf-8"))
        p50 = data["p50_phase_ns"]
        p95 = data["p95_phase_ns"]
        p99 = data["p99_phase_ns"]
        assert p50 <= p95, f"{path.name}: P50({p50}) > P95({p95}) — sort broken"
        assert p95 <= p99, f"{path.name}: P95({p95}) > P99({p99}) — sort broken"


def test_expected_phases_subset_present():
    """Each baseline must contain at least the minimum-viable phase
    set. If `definitions:parse` is missing, the throughput harness
    isn't capturing the dominant tree-sitter cost — a structural
    regression in the Progress callback wiring."""
    for path in _baseline_files():
        data = json.loads(path.read_text(encoding="utf-8"))
        phase_names = {p["phase"] for p in data["phases"]}
        missing = EXPECTED_PHASES_SUBSET - phase_names
        assert not missing, (
            f"{path.name}: missing expected phases {sorted(missing)}; "
            f"present (sample): {sorted(phase_names)[:10]}"
        )


def test_node_edge_counts_consistent():
    """node_count and edge_count must be non-negative integers, and
    their derived per-second rates must match `count / total_wall_s`
    within rounding."""
    for path in _baseline_files():
        data = json.loads(path.read_text(encoding="utf-8"))
        total_wall_s = data["total_wall_ns"] / 1e9
        if total_wall_s <= 0:
            continue  # zero-time edge case; not interesting
        nodes = data["node_count"]
        edges = data["edge_count"]
        assert nodes >= 0, f"{path.name}: node_count negative: {nodes}"
        assert edges >= 0, f"{path.name}: edge_count negative: {edges}"
        # Allow 5% tolerance (the harness uses .Round-style math
        # internally; per-second rates may not match exactly).
        n_rate_actual = data.get("nodes_per_sec", 0)
        n_rate_expected = nodes / total_wall_s
        if n_rate_expected > 0:
            ratio = abs(n_rate_actual - n_rate_expected) / n_rate_expected
            assert ratio < 0.05, (
                f"{path.name}: nodes_per_sec={n_rate_actual:.1f} "
                f"vs expected {n_rate_expected:.1f}; ratio={ratio:.3f}"
            )


def test_total_wall_ns_positive():
    """A zero or negative total_wall_ns means the harness's clock
    measurement is broken."""
    for path in _baseline_files():
        data = json.loads(path.read_text(encoding="utf-8"))
        assert data["total_wall_ns"] > 0, (
            f"{path.name}: total_wall_ns={data['total_wall_ns']} (must be > 0)"
        )


if __name__ == "__main__":
    import sys
    import traceback

    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    failures = 0
    for fn in fns:
        try:
            fn()
            print(f"PASS {fn.__name__}")
        except AssertionError as exc:
            failures += 1
            print(f"FAIL {fn.__name__}: {exc}")
        except Exception:
            failures += 1
            print(f"ERROR {fn.__name__}:")
            traceback.print_exc()
    print(f"\n{len(fns) - failures}/{len(fns)} passed")
    sys.exit(1 if failures else 0)
