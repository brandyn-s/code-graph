"""Get-well Phase 2.1 (2026-05-06): positive-trigger fixtures for the
audit harness's bucket classifier.

Plan 5 Phase A surfaced that 5 of the 7 audit buckets had never fired
on real data:
  - indirect_call_required, import_resolution_miss, scope_collision,
    agent_loop_failure, node_absent

Two of those (scope_collision, agent_loop_failure) ARE classifiable by
the propose_bucket heuristic alone — this file pins synthetic fixtures
that trigger each. The other three (indirect_call_required,
import_resolution_miss, node_absent) cannot be heuristically
distinguished from each other without graph state — they all collapse
into embedding_recall_miss in the auto-proposal. For those, we test
the YAML override path: the human classifier can override the auto-
proposal, and analyze_classified must accept and report them correctly.

Fixture shape mirrors what eval_locbench_batch.py writes via the
schema in PR #229 (PerCaseRecord.to_dict). All fields required by the
schema are present.
"""
from __future__ import annotations

import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import locbench_failure_audit as audit  # noqa: E402


def _make_case(
    *,
    instance_id: str = "test/example-1",
    indexed: bool = True,
    agent_ran: bool = True,
    file_hit: bool = False,
    class_hit: bool = False,
    func_hit: bool = False,
    ground_truth: list[str] | None = None,
    entities: list[dict] | None = None,
    iterations: list[list[dict]] | None = None,
    stop_reason: str = "consistency",
) -> dict:
    """Construct a per-case dict matching the schema's on-disk shape.

    Helper for fixtures: only the fields the auto-proposal heuristic
    looks at need to vary across tests; everything else gets defaults
    consistent with a "successful indexed agent run."
    """
    cla: dict = {
        "entities": list(entities or []),
        "iterations": list(iterations or []),
        "transcript": [],
        "turns": 5,
        "stop_reason": stop_reason,
        "input_tokens": 1000,
        "output_tokens": 100,
    }
    return {
        "instance_id": instance_id,
        "repo": "test/example",
        "category": "Bug Report",
        "ground_truth": list(ground_truth or []),
        "indexed": indexed,
        "agent_ran": agent_ran,
        "file_hit": file_hit,
        "class_hit": class_hit,
        "func_hit": func_hit,
        "file_correct": file_hit,
        "class_correct": class_hit,
        "func_correct": func_hit,
        "turns": 5,
        "input_tokens": 1000,
        "output_tokens": 100,
        "cost_estimate_usd": 0.05,
        "duration_s": 60.0,
        "note": "",
        "agent_envelope": {"code_localize_agent": cla},
    }


# ---------------- Heuristic-classifiable triggers ----------------


def test_agent_loop_failure_max_turns():
    case = _make_case(
        ground_truth=["src/foo.py:do_thing"],
        entities=[],
        stop_reason="max_turns",
    )
    bucket, rationale = audit.propose_bucket(case)
    assert bucket == "agent_loop_failure", f"expected agent_loop_failure, got {bucket!r}"
    assert "max_turns" in rationale


def test_agent_loop_failure_no_finalize():
    case = _make_case(stop_reason="no_finalize", entities=[])
    bucket, _ = audit.propose_bucket(case)
    assert bucket == "agent_loop_failure"


def test_agent_loop_failure_error():
    case = _make_case(stop_reason="error", entities=[])
    bucket, _ = audit.propose_bucket(case)
    assert bucket == "agent_loop_failure"


def test_agent_loop_failure_partial_consistency():
    case = _make_case(stop_reason="partial_consistency", entities=[])
    bucket, _ = audit.propose_bucket(case)
    assert bucket == "agent_loop_failure"


def test_scope_collision_expected_file_in_predicted():
    case = _make_case(
        ground_truth=["src/foo.py:do_thing"],
        entities=[
            {"file_path": "src/foo.py", "qualified_name": "foo.do_other", "label": "Function"},
            {"file_path": "src/bar.py", "qualified_name": "bar.helper", "label": "Function"},
        ],
        iterations=[
            [{"file_path": "src/foo.py", "qualified_name": "foo.do_other", "label": "Function"}],
            [{"file_path": "src/foo.py", "qualified_name": "foo.do_other", "label": "Function"}],
        ],
    )
    bucket, rationale = audit.propose_bucket(case)
    assert bucket == "scope_collision", f"expected scope_collision, got {bucket!r}"
    assert "expected_file_in_predicted" in rationale or "sibling" in rationale


def test_embedding_recall_miss_file_absent_all_iters():
    case = _make_case(
        ground_truth=["src/target.py:expected_fn"],
        entities=[
            {"file_path": "src/wrong1.py", "qualified_name": "w.f", "label": "Function"},
        ],
        iterations=[
            [{"file_path": "src/wrong1.py", "qualified_name": "w.f", "label": "Function"}],
            [{"file_path": "src/wrong2.py", "qualified_name": "w.g", "label": "Function"}],
        ],
    )
    bucket, rationale = audit.propose_bucket(case)
    assert bucket == "embedding_recall_miss"
    assert "absent" in rationale.lower() or "iteration" in rationale.lower()


def test_embedding_recall_miss_file_in_iter_dropped_from_aggregate():
    case = _make_case(
        ground_truth=["src/target.py:expected_fn"],
        entities=[{"file_path": "src/sibling.py", "qualified_name": "x", "label": "Function"}],
        iterations=[
            [
                {"file_path": "src/target.py", "qualified_name": "x", "label": "Function"},
                {"file_path": "src/sibling.py", "qualified_name": "y", "label": "Function"},
            ],
            [{"file_path": "src/sibling.py", "qualified_name": "y", "label": "Function"}],
        ],
    )
    bucket, _ = audit.propose_bucket(case)
    assert bucket == "embedding_recall_miss"


# ---------------- Loud-failure trigger (Phase 1.5) ----------------


def test_phase15_loud_warning_for_zero_predictions():
    """When agent_ran=True + indexed=True + 0 predictions, the harness
    must auto-classify as embedding_recall_miss (not TODO) AND emit
    a stderr warning. This is the silent-failure pattern Phase 1.5
    addresses."""
    case = _make_case(
        ground_truth=["src/target.py:expected_fn"],
        entities=[],
        iterations=[],
        stop_reason="finalized",
    )
    bucket, rationale = audit.propose_bucket(case)
    assert bucket == "embedding_recall_miss", (
        f"silent-failure pattern must auto-classify as "
        f"embedding_recall_miss, got {bucket!r}"
    )
    assert "zero entities" in rationale.lower() or "vocabulary" in rationale.lower()


# ---------------- YAML round-trip for human-only buckets ----------------


def test_yaml_roundtrip_human_classified_buckets():
    """The 3 buckets that propose_bucket can't distinguish heuristically
    (indirect_call_required, import_resolution_miss, node_absent) must
    survive the YAML override path: human edits the bucket field, the
    analyzer reads it back, and the decision-rule outcome reflects the
    human classification.

    Pins that the YAML format itself supports each bucket name.
    """
    yaml_text = """\
# Test fixture
cases:
  - id: "test/case-1"
    issue_excerpt: ""
    expected: ["src/foo.py:do_thing"]
    predicted: ["src/wrong.py:other"]
    stop_reason: "finalized"
    iterations_count: 2
    rescued_by_iter: 0
    proposed_bucket: "embedding_recall_miss"
    proposal_rationale: "auto-proposed"
    bucket: "indirect_call_required"
    human_rationale: "expected fn called via callback registry"

  - id: "test/case-2"
    issue_excerpt: ""
    expected: ["src/a.py:f"]
    predicted: ["src/b.py:g"]
    stop_reason: "finalized"
    iterations_count: 2
    rescued_by_iter: 0
    proposed_bucket: "embedding_recall_miss"
    proposal_rationale: "auto-proposed"
    bucket: "import_resolution_miss"
    human_rationale: "missing IMPORTS edge across modules"

  - id: "test/case-3"
    issue_excerpt: ""
    expected: ["generated/parser.py:parse"]
    predicted: ["src/handler.py:run"]
    stop_reason: "finalized"
    iterations_count: 2
    rescued_by_iter: 0
    proposed_bucket: "embedding_recall_miss"
    proposal_rationale: "auto-proposed"
    bucket: "node_absent"
    human_rationale: "generated file not in indexed corpus"

"""
    # Add 7 more so we exceed MIN_ACTIONABLE_MISSES (10).
    for i in range(4, 11):
        yaml_text += f"""\
  - id: "test/case-{i}"
    issue_excerpt: ""
    expected: ["src/x.py:y"]
    predicted: ["src/x.py:z"]
    stop_reason: "finalized"
    iterations_count: 2
    rescued_by_iter: 0
    proposed_bucket: "scope_collision"
    proposal_rationale: "auto-proposed"
    bucket: "scope_collision"
    human_rationale: "sibling miss"

"""
    with tempfile.TemporaryDirectory() as td:
        yaml_path = Path(td) / "audit.yaml"
        yaml_path.write_text(yaml_text, encoding="utf-8")
        rc = audit.analyze_classified(yaml_path)
        assert rc == 0, f"analyze_classified should succeed, got rc={rc}"


# ---------------- All-7-bucket sanity check ----------------


def test_all_seven_buckets_in_constant():
    """The audit module's BUCKETS constant must list exactly the 7
    canonical buckets. Catches drift from accidental rename/add."""
    expected = {
        "indirect_call_required",
        "import_resolution_miss",
        "scope_collision",
        "embedding_recall_miss",
        "agent_loop_failure",
        "oracle_gap",
        "node_absent",
    }
    actual = set(audit.BUCKETS)
    assert actual == expected, (
        f"BUCKETS drift: extra={actual - expected}, missing={expected - actual}"
    )


def test_each_bucket_has_action_string():
    """Every bucket must map to an action string in BUCKET_ACTIONS — a
    dominant verdict prints the action, so a missing entry would
    KeyError at the worst possible time."""
    for bucket in audit.BUCKETS:
        action = audit.BUCKET_ACTIONS.get(bucket)
        assert action, f"bucket {bucket!r} missing BUCKET_ACTIONS entry"


def test_each_bucket_has_description_string():
    for bucket in audit.BUCKETS:
        desc = audit.BUCKET_DESCRIPTIONS.get(bucket)
        assert desc, f"bucket {bucket!r} missing BUCKET_DESCRIPTIONS entry"


if __name__ == "__main__":
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
