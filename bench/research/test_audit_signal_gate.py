"""Roundtable T3 (2026-05-06): test the minimum-effective-signal gate
in locbench_failure_audit.analyze_classified.

The Plan 5 Phase A failure mode this test pins:
  - 19 total misses
  - 15 oracle_gap (clone failures, benchmark-data issue)
  - 4 embedding_recall_miss (real signal but n=4 too small)
  - The pre-T3 harness emitted "oracle_gap dominates at 78.9%; recommend
    Loc-Bench fixture update upstream" — a confidently-shaped
    recommendation from inadequate signal that the human author had to
    override in the outcomes doc.

After T3, the same input must produce INSUFFICIENT_SIGNAL.
"""
from __future__ import annotations

import sys
from collections import Counter
from pathlib import Path

# Make the audit module importable.
sys.path.insert(0, str(Path(__file__).resolve().parent))
import locbench_failure_audit as audit


def test_phase_a_pattern_yields_insufficient_signal():
    """The Plan 5 Phase A bucket distribution must NOT trigger DOMINANT
    under the T3 gate. Pre-T3 it did; this test pins the fix."""
    buckets = Counter({"oracle_gap": 15, "embedding_recall_miss": 4})
    actionable = 4  # 19 total minus 15 oracle = 4
    classified = 19
    verdict, dominant, _ = audit._verdict_under_signal_gate(
        buckets, actionable, classified
    )
    assert verdict == "INSUFFICIENT_SIGNAL", (
        f"expected INSUFFICIENT_SIGNAL on Phase A pattern, got {verdict!r}"
    )
    assert dominant is None


def test_oracle_dominance_does_not_emit_recommendation():
    """Even when oracle_gap is 100% of misses, the gate must not emit
    a "Loc-Bench fixture update" recommendation if the actionable
    count is below the threshold. The benchmark-data signal isn't a
    capability signal."""
    buckets = Counter({"oracle_gap": 50})
    verdict, _, rationale = audit._verdict_under_signal_gate(
        buckets, actionable_total=0, classified_total=50
    )
    assert verdict == "INSUFFICIENT_SIGNAL"
    assert "Threshold" in rationale or "actionable" in rationale.lower()


def test_dominant_above_threshold_emits_recommendation():
    """Sanity check: when actionable count is high enough AND a non-oracle
    bucket dominates, the harness still emits the dominant verdict."""
    buckets = Counter({
        "oracle_gap": 5,
        "embedding_recall_miss": 12,
        "scope_collision": 2,
    })
    actionable = 14  # 19 total minus 5 oracle
    verdict, bucket, rationale = audit._verdict_under_signal_gate(
        buckets, actionable_total=actionable, classified_total=19
    )
    assert verdict == "DOMINANT"
    assert bucket == "embedding_recall_miss"
    assert "12" in rationale or "85.7" in rationale


def test_no_dominant_when_actionable_split():
    """When actionable count meets the threshold but no bucket reaches
    60%, return NO_DOMINANT (not INSUFFICIENT_SIGNAL)."""
    buckets = Counter({
        "embedding_recall_miss": 5,
        "scope_collision": 5,
        "import_resolution_miss": 3,
    })
    verdict, bucket, _ = audit._verdict_under_signal_gate(
        buckets, actionable_total=13, classified_total=13
    )
    assert verdict == "NO_DOMINANT"
    assert bucket is None


def test_threshold_boundary_inclusive():
    """At exactly MIN_ACTIONABLE_MISSES the gate should pass (>=, not >)."""
    buckets = Counter({"embedding_recall_miss": 10})
    verdict, _, _ = audit._verdict_under_signal_gate(
        buckets,
        actionable_total=audit.MIN_ACTIONABLE_MISSES,
        classified_total=audit.MIN_ACTIONABLE_MISSES,
    )
    assert verdict == "DOMINANT", (
        f"at boundary actionable=={audit.MIN_ACTIONABLE_MISSES}, expected DOMINANT, got {verdict}"
    )


def test_threshold_boundary_just_under_fails():
    """One below MIN_ACTIONABLE_MISSES must trigger INSUFFICIENT_SIGNAL."""
    buckets = Counter({"embedding_recall_miss": 9})
    verdict, _, _ = audit._verdict_under_signal_gate(
        buckets,
        actionable_total=audit.MIN_ACTIONABLE_MISSES - 1,
        classified_total=audit.MIN_ACTIONABLE_MISSES - 1,
    )
    assert verdict == "INSUFFICIENT_SIGNAL"


def test_oracle_buckets_subtracted_from_actionable_denominator():
    """The actionable denominator must exclude oracle_gap. Pre-T3 this
    was the bug — using `classified` instead, oracle_gap could show
    78.9% dominance from benchmark-data issues alone."""
    buckets = Counter({"oracle_gap": 15, "embedding_recall_miss": 12})
    # actionable = 27 - 15 = 12; embedding_recall_miss = 12/12 = 100%.
    verdict, bucket, _ = audit._verdict_under_signal_gate(
        buckets, actionable_total=12, classified_total=27
    )
    assert verdict == "DOMINANT"
    assert bucket == "embedding_recall_miss"


if __name__ == "__main__":
    # Allow running directly.
    import traceback

    fns = [v for k, v in globals().items() if k.startswith("test_") and callable(v)]
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
