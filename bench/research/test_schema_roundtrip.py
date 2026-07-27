"""Get-well Phase 1.4 (2026-05-06): round-trip golden fixture for the
shared harness schema.

Pins:
  1. PerCaseRecord round-trips losslessly through to_dict / from_dict.
  2. BatchSummaryRecord round-trips losslessly.
  3. from_dict raises KeyError on missing required fields (the
     contract enforcement that makes writer/reader drift loud).
  4. AgentEnvelope.from_dict raises when agent_ran=True but envelope
     is malformed (the silent-failure pattern Phase A hit).
  5. Real Phase A per-case JSON parses without error (regression
     test for the existing on-disk corpus).
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import schema as S


def _make_record(file_hit: bool = True) -> S.PerCaseRecord:
    """Hand-construct a representative PerCaseRecord for round-trip tests."""
    return S.PerCaseRecord(
        instance_id="example__example-1",
        repo="example/example",
        category="Bug Report",
        ground_truth=["src/example.py:do_thing"],
        indexed=True,
        agent_ran=True,
        file_hit=file_hit,
        class_hit=False,
        func_hit=file_hit,
        file_correct=file_hit,
        class_correct=False,
        func_correct=file_hit,
        turns=12,
        input_tokens=10000,
        output_tokens=500,
        cost_estimate_usd=0.05,
        duration_s=120.5,
        latency_s={
            "clone": 1.0,
            "index": 20.0,
            "marginal_query": 99.0,
            "total": 120.5,
        },
        note="",
        agent_envelope=S.AgentEnvelope(
            code_localize_agent=S.CodeLocalizeAgentResult(
                entities=[
                    S.LocalizedEntity(
                        file_path="src/example.py",
                        qualified_name="example.do_thing",
                        label="Function",
                    )
                ],
                iterations=[
                    [S.LocalizedEntity(file_path="src/example.py", qualified_name="", label="")],
                    [S.LocalizedEntity(file_path="src/example.py", qualified_name="", label="")],
                ],
                transcript=[],
                turns=12,
                stop_reason="finalized",
                input_tokens=10000,
                output_tokens=500,
                note="",
            )
        ),
    )


def test_per_case_record_roundtrip():
    """to_dict → from_dict must be lossless on a fully-populated record."""
    rec = _make_record()
    j = rec.to_dict()
    rec2 = S.PerCaseRecord.from_dict(j)
    assert rec2 == rec, "round-trip changed the record"


def test_per_case_record_json_roundtrip():
    """JSON-encoding the to_dict output and parsing back must also work."""
    rec = _make_record()
    blob = json.dumps(rec.to_dict())
    parsed = json.loads(blob)
    rec2 = S.PerCaseRecord.from_dict(parsed)
    assert rec2 == rec


def test_missing_required_field_raises():
    """The contract enforcement: from_dict on a dict missing a required
    field must raise KeyError, not silently return a default-populated
    record."""
    rec = _make_record()
    j = rec.to_dict()
    # Remove `agent_ran` (required). Pre-Phase-1, the chain-of-.get()
    # pattern would have silently treated this as False and proceeded.
    del j["agent_ran"]
    try:
        S.PerCaseRecord.from_dict(j)
    except KeyError as exc:
        assert "agent_ran" in str(exc)
        return
    raise AssertionError("from_dict accepted a record missing 'agent_ran'")


def test_agent_envelope_missing_when_agent_ran_raises():
    """If agent_ran=True but the envelope or its code_localize_agent is
    absent, from_dict must raise. This is the Phase A silent-failure
    pattern: 4 cases had agent_ran=True with no usable agent output,
    and the audit harness treated them as having empty predictions."""
    rec = _make_record()
    j = rec.to_dict()
    j["agent_envelope"] = {}  # agent_ran is True but no inner agent result
    try:
        S.PerCaseRecord.from_dict(j)
    except KeyError as exc:
        assert "code_localize_agent" in str(exc)
        return
    raise AssertionError(
        "from_dict accepted agent_ran=True with empty agent_envelope"
    )


def test_agent_envelope_absent_when_agent_didnt_run_ok():
    """Conversely, agent_ran=False with no envelope is valid (the case
    failed before the agent loop). from_dict must NOT raise here —
    that combination is legitimate."""
    rec = _make_record()
    object.__setattr__(rec, "_dummy", None)  # noqa: just to confirm rec is editable
    # Build a "didn't run" record from scratch.
    j = {
        "instance_id": "x",
        "repo": "x/y",
        "category": "Bug Report",
        "ground_truth": [],
        "indexed": False,
        "agent_ran": False,
        "file_hit": False,
        "class_hit": False,
        "func_hit": False,
        "turns": 0,
        "input_tokens": 0,
        "output_tokens": 0,
        "cost_estimate_usd": 0.0,
        "duration_s": 0.0,
        "note": "clone failed",
    }
    parsed = S.PerCaseRecord.from_dict(j)
    assert parsed.indexed is False
    assert parsed.agent_ran is False
    assert parsed.predicted_files == []


def test_batch_summary_roundtrip():
    """BatchSummaryRecord round-trips losslessly."""
    rec_a = _make_record(file_hit=True)
    rec_b = _make_record(file_hit=False)
    # Mutate the second record's id so equality works.
    j_b = rec_b.to_dict()
    j_b["instance_id"] = "example__example-2"
    rec_b = S.PerCaseRecord.from_dict(j_b)

    summary = S.BatchSummaryRecord(
        schema_version=S.SCHEMA_VERSION,
        generated_at="2026-05-06T18:00:00Z",
        n_total=2,
        n_indexed=2,
        n_agent_ran=2,
        n_file_hit=1,
        n_class_hit=0,
        n_func_hit=1,
        aborted_reason="",
        cases=[rec_a, rec_b],
    )
    blob = json.dumps(summary.to_dict())
    parsed = S.BatchSummaryRecord.from_dict(json.loads(blob))
    assert parsed == summary


def test_batch_missing_required_raises():
    """A truncated BatchSummary (missing n_total) must raise."""
    j = {
        # n_total absent
        "n_indexed": 1,
        "n_agent_ran": 1,
        "n_file_hit": 1,
        "n_class_hit": 0,
        "n_func_hit": 1,
        "cases": [],
    }
    try:
        S.BatchSummaryRecord.from_dict(j)
    except KeyError as exc:
        assert "n_total" in str(exc)
        return
    raise AssertionError("BatchSummaryRecord.from_dict accepted missing n_total")


def test_predicted_files_handles_missing_envelope():
    """Helper accessor must safely return [] when no envelope present —
    the schema raises only on the writer/reader CONTRACT path, not on
    the convenience accessor used by readers."""
    j = {
        "instance_id": "x",
        "repo": "x/y",
        "category": "Bug Report",
        "ground_truth": [],
        "indexed": False,
        "agent_ran": False,
        "file_hit": False,
        "class_hit": False,
        "func_hit": False,
        "turns": 0,
        "input_tokens": 0,
        "output_tokens": 0,
        "cost_estimate_usd": 0.0,
        "duration_s": 0.0,
    }
    rec = S.PerCaseRecord.from_dict(j)
    assert rec.predicted_files == []
    assert rec.iterations_files == []
    assert rec.stop_reason == ""


def test_real_phase_a_json_parses():
    """Regression: the on-disk Phase A serial JSON must parse via the
    schema. If this fails, the schema and the existing artifact have
    drifted and the audit harness will break."""
    p = Path("bench/research/baselines/2026-05-06-loc-bench-n50-serial.json")
    if not p.exists():
        # Repo state may not include the baseline (e.g., shallow clone);
        # skip rather than fail.
        return
    raw = json.loads(p.read_text(encoding="utf-8"))
    parsed = S.BatchSummaryRecord.from_dict(raw)
    assert parsed.n_total == 50
    assert len(parsed.cases) == 50
    # At least one case should have ran the agent successfully.
    ran = [c for c in parsed.cases if c.agent_ran and c.indexed]
    assert len(ran) > 0
    # Sanity: predicted_files works on real data.
    for c in ran[:5]:
        # No assertion on content; just verify access doesn't raise.
        _ = c.predicted_files


def test_real_phase_a_partial_json_parses():
    """Regression: the n=10 parallel JSON (PR #228 output) must also parse."""
    p = Path("bench/research/baselines/2026-05-06-loc-bench-n10-parallel-T2.json")
    if not p.exists():
        return
    raw = json.loads(p.read_text(encoding="utf-8"))
    parsed = S.BatchSummaryRecord.from_dict(raw)
    assert parsed.n_total == 10


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
