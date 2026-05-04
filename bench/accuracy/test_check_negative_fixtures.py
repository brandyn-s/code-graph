"""Unit tests for check_negative_fixtures.py helpers.

Specifically guards the QN-rewrite logic that lets ground-truth
fixtures move across machines (developer ↔ CI runner ↔ other dev)
without false-negative recall loss.

Run via:
    python -m pytest bench/accuracy/test_check_negative_fixtures.py -v
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))

from check_negative_fixtures import (  # noqa: E402
    project_for_path,
    rewrite_qn_to_current_project,
)


# ----------------------------------------------------------------------
# rewrite_qn_to_current_project
# ----------------------------------------------------------------------

class TestRewriteQN:
    """Ground-truth files store QNs with the prefix from the
    machine where they were authored. Cross-machine runs must rewrite
    that prefix to the runtime project name or every lookup misses.
    Pre-fix: 100% recall loss on CI (POSITIVE-CONTROL MISS 9/9 etc.)."""

    def test_dev_machine_qn_to_ci_runner(self):
        """Common case: GT authored on dev machine, run on CI."""
        old = "c-Users-user-Documents-GitHub-code-graph-bench-accuracy-synthetic-rust-actix-data-negative.src.main.entry"
        current = "home-runner-work-code-graph-code-graph-bench-accuracy-synthetic-rust-actix-data-negative"
        expected = current + ".src.main.entry"
        assert rewrite_qn_to_current_project(old, current) == expected

    def test_method_qn_with_class_intermediate(self):
        old = "c-Users-Brandyn.src.metrics.MetricsCollector.record"
        current = "home-runner-fixture"
        assert rewrite_qn_to_current_project(old, current) == "home-runner-fixture.src.metrics.MetricsCollector.record"

    def test_qn_with_no_dots_unchanged(self):
        """Defensive: QN with no dots can't be rewritten meaningfully."""
        assert rewrite_qn_to_current_project("no_dots", "current") == "no_dots"

    def test_already_current_project_idempotent(self):
        """Calling rewrite when QN already has the current project: no-op-ish."""
        current = "current-project"
        old = current + ".src.main.entry"
        # Strips the prefix and adds it back — same result.
        assert rewrite_qn_to_current_project(old, current) == old

    def test_strips_only_first_dot(self):
        """Multi-segment paths split only at the first dot."""
        old = "old.proj.with.many.dots.src.foo"
        current = "new"
        # First-dot split: prefix = "old", tail = "proj.with.many.dots.src.foo"
        assert rewrite_qn_to_current_project(old, current) == "new.proj.with.many.dots.src.foo"


# ----------------------------------------------------------------------
# project_for_path (existing function, not changed by this PR — guard
# against drift since it's tightly coupled to the rewrite helper above)
# ----------------------------------------------------------------------

class TestProjectForPath:
    def test_windows_path_lowercases_drive(self):
        # path_for_path's behavior: C:\Users\... → c-Users-...
        p = Path("C:/Users/dev/code-graph")
        result = project_for_path(p)
        assert result.startswith("c-Users-")

    def test_unix_path_no_drive_letter(self):
        p = Path("/home/runner/work/code-graph")
        result = project_for_path(p)
        # Expect "home-runner-work-code-graph" (leading slash stripped)
        assert "home-runner-work-code-graph" in result
        assert not result.startswith("-")

    def test_collapses_multiple_separators(self):
        p = Path("/a//b///c")
        result = project_for_path(p)
        assert "--" not in result


# ----------------------------------------------------------------------
# Integration: rewrite + project_for_path produce a usable runtime QN
# ----------------------------------------------------------------------

class TestEndToEnd:
    def test_rewrite_with_real_runtime_project(self):
        """Simulate the actual ground_truth.json → runtime lookup chain."""
        gt_qn = "c-Users-Brandyn-Documents-GitHub-code-graph-bench-accuracy-synthetic-rust-actix-data-negative.src.main.controls"
        runtime_path = Path("/home/runner/work/code-graph/code-graph/bench/accuracy/synthetic/rust-actix-data-negative")
        runtime_project = project_for_path(runtime_path)
        rewritten = rewrite_qn_to_current_project(gt_qn, runtime_project)
        # Result must end with the same tail as the GT-stored QN
        assert rewritten.endswith(".src.main.controls")
        # And must start with the runtime project prefix
        assert rewritten.startswith(runtime_project + ".")
