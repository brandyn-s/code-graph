"""Mechanical refusal gate tests (Family C — report invariants).

Family C is the second leg of the three-leg measurement-discipline
stool. The gate refuses to publish a benchmark report when:

1. Any mode's hierarchy is non-monotone (file < class or class < func)
   beyond a 5pp tolerance — the shape that surfaced ACC-012.
2. Any granularity column has a category holding ≥30% of misses
   without explanation — the shape that surfaced incidents 1 and 5/6/7.
3. An external comparator is referenced without a metric-equivalence
   note — the shape that surfaced incident 4's reporting framing.

Override flags allow accepting violations with a reason, but the
violations are always recorded in the report header.

Run via: `python -m pytest bench/research/test_report_invariants.py -v`
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))

from eval_locbench_compare import (  # noqa: E402
    InstanceResult,
    ModeResult,
    _check_report_invariants,
)


def _make_result(
    instance_id: str,
    category: str,
    indexed: bool,
    file_h: bool,
    class_h: bool,
    func_h: bool,
    mode: str = "hybrid-agent",
) -> InstanceResult:
    """Construct an InstanceResult with one ModeResult."""
    mr = ModeResult(
        mode=mode,
        file_hit=file_h,
        class_hit=class_h,
        func_hit=func_h,
    )
    return InstanceResult(
        instance_id=instance_id,
        repo="example/repo",
        category=category,
        base_commit="abc123",
        ground_truth=["fake/path.py:func"],
        indexed=indexed,
        mode_results=[mr],
        repo_size_mb=10.0,
    )


def _make_clean_run(n: int = 20) -> list[InstanceResult]:
    """Make N indexed instances with monotone results: file>=class>=func.
    Categories cycle deterministically so any misses spread evenly
    across categories (each category holds ~25% of misses, below the
    30% dominant-cell threshold).
    """
    cats = ["Bug Report", "Feature Request", "Performance Issue", "Security Vulnerability"]
    out = []
    # Ensure n is a multiple of len(cats) for even distribution
    n = max(n, len(cats) * 5)  # at least 5 per category
    n -= n % len(cats)
    for i in range(n):
        cat = cats[i % len(cats)]
        # Miss patterns chosen so misses also cycle through categories
        # 1 in 4 instances misses file (one per category)
        # 1 in 4 instances misses class (one per category)
        # 1 in 4 instances misses func (one per category)
        # Different offsets so the same category hits all three columns
        # Use mod-5 patterns so misses spread across all 4 categories
        # (mod-4 would concentrate all misses in one category)
        miss_file = (i % 5 == 0)  # 20% file miss rate, 1-2 per cat
        miss_class = (i % 5 == 1)  # 20% class miss rate
        miss_func = (i % 5 == 2)   # 20% func miss rate
        out.append(_make_result(
            f"repo-{i}", cat, True,
            file_h=not miss_file,
            class_h=not miss_class,
            func_h=not miss_func,
        ))
    return out


# ----------------------------------------------------------------------
# Gate 1: monotonicity
# ----------------------------------------------------------------------

class TestMonotonicityGate:
    def test_clean_monotone_passes(self):
        """file >= class >= func (within 5pp): no violations."""
        results = _make_clean_run(20)
        violations = _check_report_invariants(results, ["hybrid-agent"])
        non_monotone = [v for v in violations if "non-monotone" in v]
        assert non_monotone == []

    def test_class_exceeds_file_refuses(self):
        """class > file: REFUSE."""
        # 10 file misses, 0 class misses, 5 func misses → class > file
        results = []
        for i in range(20):
            results.append(_make_result(
                f"r-{i}", "Bug Report", True,
                file_h=(i >= 10),  # 10/20 file hits = 50%
                class_h=True,       # 20/20 class hits = 100%
                func_h=(i >= 5),    # 15/20 func hits = 75%
            ))
        violations = _check_report_invariants(results, ["hybrid-agent"])
        refusals = [v for v in violations if v.startswith("REFUSE:") and "class=" in v]
        assert len(refusals) >= 1, f"Expected REFUSE for class>file; got {violations}"

    def test_func_exceeds_class_refuses_acc012_shape(self):
        """func > class: REFUSE — this is exactly the ACC-012 shape."""
        # All file hits, no class hits, all func hits (the ACC-012 pre-fix
        # shape — class_hit forced False but func_hit could still be True)
        results = []
        for i in range(20):
            results.append(_make_result(
                f"r-{i}", "Bug Report", True,
                file_h=True,
                class_h=False,
                func_h=True,
            ))
        violations = _check_report_invariants(results, ["hybrid-agent"])
        refusals = [v for v in violations if v.startswith("REFUSE:") and "ACC-012" in v]
        assert len(refusals) >= 1, (
            f"Expected REFUSE referencing ACC-012 shape; got {violations}"
        )

    def test_accept_non_monotone_override(self):
        """--accept-non-monotone REASON converts REFUSE to ACCEPTED."""
        results = []
        for i in range(20):
            results.append(_make_result(
                f"r-{i}", "Bug Report", True,
                file_h=True, class_h=False, func_h=True,
            ))
        violations = _check_report_invariants(
            results, ["hybrid-agent"],
            accept_non_monotone="testing the override flag",
            allow_unexplained_cells=True,  # silence gate 2 for this test
        )
        # No REFUSE entries
        refusals = [v for v in violations if v.startswith("REFUSE:")]
        assert refusals == [], f"Override should suppress REFUSE; got {refusals}"
        # But the violation IS recorded as ACCEPTED
        accepted = [v for v in violations if v.startswith("[ACCEPTED")]
        assert len(accepted) >= 1
        assert "testing the override flag" in accepted[0]

    def test_within_tolerance_passes(self):
        """class > file by < 5pp: no violation (tolerance for tied cases)."""
        # 19/20 file (95%), 20/20 class (100%) → class > file by 5pp exactly
        # Should be within tolerance
        results = []
        for i in range(20):
            results.append(_make_result(
                f"r-{i}", "Bug Report", True,
                file_h=(i != 0),  # 19/20 = 95%
                class_h=True,      # 20/20 = 100%
                func_h=True,
            ))
        violations = _check_report_invariants(
            results, ["hybrid-agent"],
            allow_unexplained_cells=True,
        )
        refusals = [v for v in violations if v.startswith("REFUSE:") and "non-monotone" in v]
        assert refusals == [], (
            f"Within 5pp tolerance should pass; got {refusals}"
        )


# ----------------------------------------------------------------------
# Gate 2: cell-mass dominance
# ----------------------------------------------------------------------

class TestCellMassGate:
    def test_no_dominant_cell_passes(self):
        """Misses spread across categories: no single cell ≥30%."""
        results = []
        # 5 misses each in 4 different categories = even distribution
        for cat in ["Bug Report", "Feature Request", "Performance Issue", "Security Vulnerability"]:
            for i in range(5):
                results.append(_make_result(
                    f"{cat}-{i}", cat, True,
                    file_h=False,  # all miss file
                    class_h=False, func_h=False,
                ))
        # Add hits to balance — gate fires only when total_misses ≥ 5
        for i in range(20):
            results.append(_make_result(
                f"hit-{i}", "Bug Report", True,
                file_h=True, class_h=True, func_h=True,
            ))
        violations = _check_report_invariants(results, ["hybrid-agent"])
        cell_violations = [v for v in violations if "dominant-cell" in v]
        # Each category has 5/20 = 25% of misses, < 30% threshold
        assert cell_violations == [], (
            f"Even distribution shouldn't fire cell-mass; got {cell_violations}"
        )

    def test_dominant_cell_refuses(self):
        """One category holds ≥30% of misses: REFUSE."""
        results = []
        # 8 misses in "Bug Report", 2 each in 3 other categories = 8/14 = 57%
        for i in range(8):
            results.append(_make_result(
                f"bug-{i}", "Bug Report", True,
                file_h=False, class_h=False, func_h=False,
            ))
        for cat in ["Feature Request", "Performance Issue", "Security Vulnerability"]:
            for i in range(2):
                results.append(_make_result(
                    f"{cat}-{i}", cat, True,
                    file_h=False, class_h=False, func_h=False,
                ))
        # Hits to make denominators sensible
        for i in range(10):
            results.append(_make_result(
                f"hit-{i}", "Bug Report", True,
                file_h=True, class_h=True, func_h=True,
            ))
        violations = _check_report_invariants(results, ["hybrid-agent"])
        refusals = [v for v in violations if v.startswith("REFUSE: dominant-cell")]
        assert len(refusals) >= 1, (
            f"Expected dominant-cell REFUSE; got {violations}"
        )

    def test_allow_unexplained_cells_override(self):
        """--allow-unexplained-cells suppresses cell-mass check."""
        results = []
        for i in range(8):
            results.append(_make_result(
                f"bug-{i}", "Bug Report", True,
                file_h=False, class_h=False, func_h=False,
            ))
        for cat in ["Feature Request", "Performance Issue", "Security Vulnerability"]:
            for i in range(2):
                results.append(_make_result(
                    f"{cat}-{i}", cat, True,
                    file_h=False, class_h=False, func_h=False,
                ))
        for i in range(10):
            results.append(_make_result(
                f"hit-{i}", "Bug Report", True,
                file_h=True, class_h=True, func_h=True,
            ))
        violations = _check_report_invariants(
            results, ["hybrid-agent"],
            allow_unexplained_cells=True,
        )
        cell_refusals = [v for v in violations if v.startswith("REFUSE: dominant-cell")]
        assert cell_refusals == []

    def test_too_few_misses_skipped(self):
        """Fewer than 5 misses in a column: gate doesn't fire (too noisy)."""
        results = []
        # Just 3 misses total (< 5 threshold)
        for i in range(3):
            results.append(_make_result(
                f"r-{i}", "Bug Report", True,
                file_h=False, class_h=False, func_h=False,
            ))
        for i in range(20):
            results.append(_make_result(
                f"hit-{i}", "Feature Request", True,
                file_h=True, class_h=True, func_h=True,
            ))
        violations = _check_report_invariants(results, ["hybrid-agent"])
        cell_refusals = [v for v in violations if v.startswith("REFUSE: dominant-cell")]
        assert cell_refusals == []


# ----------------------------------------------------------------------
# Gate 3: external-comparator equivalence
# ----------------------------------------------------------------------

class TestExternalComparatorGate:
    def test_external_comparator_without_note_refuses(self):
        """--external-comparator set without --metric-equivalence-note: REFUSE."""
        results = _make_clean_run(20)
        violations = _check_report_invariants(
            results, ["hybrid-agent"],
            external_comparator="locagent",
        )
        refusals = [v for v in violations if "external-comparator" in v.lower() or "external_comparator" in v.lower() or "metric-equivalence-note" in v.lower()]
        assert len(refusals) >= 1, (
            f"Expected REFUSE for missing equivalence note; got {violations}"
        )

    def test_external_comparator_with_valid_note_passes(self, tmp_path):
        """--external-comparator + valid --metric-equivalence-note: passes."""
        note = tmp_path / "equiv-note.md"
        note.write_text("Our class metric matches LocAgent's module metric per X, Y, Z.")
        results = _make_clean_run(20)
        violations = _check_report_invariants(
            results, ["hybrid-agent"],
            external_comparator="locagent",
            metric_equivalence_note=note,
        )
        ext_refusals = [v for v in violations if "external" in v.lower() or "equivalence" in v.lower()]
        assert ext_refusals == [], (
            f"Valid pair shouldn't refuse; got {ext_refusals}"
        )

    def test_metric_equivalence_note_path_must_exist(self, tmp_path):
        """--metric-equivalence-note path that doesn't exist: REFUSE."""
        bad_path = tmp_path / "nonexistent.md"
        results = _make_clean_run(20)
        violations = _check_report_invariants(
            results, ["hybrid-agent"],
            external_comparator="locagent",
            metric_equivalence_note=bad_path,
        )
        path_refusals = [v for v in violations if "does not exist" in v]
        assert len(path_refusals) >= 1


# ----------------------------------------------------------------------
# Edge cases
# ----------------------------------------------------------------------

class TestEdgeCases:
    def test_no_indexed_results_skips_all_gates(self):
        """Empty / all-clone-failed run: no violations (early checkpoint)."""
        results = []
        for i in range(5):
            results.append(_make_result(
                f"r-{i}", "Bug Report", indexed=False,
                file_h=False, class_h=False, func_h=False,
            ))
        violations = _check_report_invariants(results, ["hybrid-agent"])
        assert violations == [], (
            f"Empty run should skip gates; got {violations}"
        )

    def test_clean_run_returns_empty(self):
        """Clean run with monotone hierarchy and even cells: no violations."""
        results = _make_clean_run(20)
        violations = _check_report_invariants(results, ["hybrid-agent"])
        assert violations == [], f"Expected clean run; got {violations}"
