"""Roundtable convergent finding #2 (2026-05-06): regression test for
the _to_record fallback quarantine.

Pre-roundtable: any future from_dict bug (KeyError, TypeError,
ValueError) silently routed to the legacy chain-of-.get() path.
Stderr warning was the only signal — invisible in CI.

Post-roundtable: default behavior is fail-loud (raises SchemaParseError).
The legacy path is opt-in via ALLOW_LEGACY_FALLBACK.

This file pins:
  - Default mode: malformed case raises SchemaParseError.
  - Legacy mode: malformed case falls back without raising.
  - Real Phase A JSON parses cleanly in default mode (no regression).
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import locbench_failure_audit as audit  # noqa: E402


def _save_default_mode():
    """Save and restore module state so tests don't leak."""
    return audit.ALLOW_LEGACY_FALLBACK


def _restore_default_mode(saved):
    audit.ALLOW_LEGACY_FALLBACK = saved


def test_default_mode_raises_on_malformed_case():
    """A case dict missing required schema fields must raise
    SchemaParseError in default mode. Pre-quarantine this silently
    fell back to the chain-of-.get() pattern."""
    saved = _save_default_mode()
    audit.ALLOW_LEGACY_FALLBACK = False
    try:
        # Missing "agent_ran" (required field per PerCaseRecord).
        malformed = {
            "instance_id": "test/example-1",
            "repo": "test/example",
            "category": "Bug Report",
            "ground_truth": [],
            "indexed": False,
            # agent_ran intentionally absent
            "file_hit": False,
            "class_hit": False,
            "func_hit": False,
        }
        try:
            audit._to_record(malformed)
        except audit.SchemaParseError as exc:
            # Expected: error mentions the field that drifted.
            assert "agent_ran" in str(exc) or "test/example-1" in str(exc)
            return
        raise AssertionError(
            "default mode silently accepted malformed case; "
            "fail-loud quarantine has regressed"
        )
    finally:
        _restore_default_mode(saved)


def test_legacy_mode_falls_back_without_raising():
    """Opt-in legacy mode preserves the old behavior — malformed cases
    log to stderr and return None instead of raising. Required for
    backward compat with truly-legacy fixture data."""
    saved = _save_default_mode()
    audit.ALLOW_LEGACY_FALLBACK = True
    try:
        malformed = {
            "instance_id": "test/example-2",
            "repo": "test/example",
        }
        rec = audit._to_record(malformed)
        assert rec is None, (
            f"legacy mode should return None on malformed case, got {rec!r}"
        )
    finally:
        _restore_default_mode(saved)


def test_default_mode_accepts_valid_record():
    """Sanity: a fully-shaped case dict passes parse in default mode
    without hitting the fail-loud path. If this regresses, the schema
    has tightened beyond what the eval writer produces."""
    saved = _save_default_mode()
    audit.ALLOW_LEGACY_FALLBACK = False
    try:
        valid = {
            "instance_id": "test/example-3",
            "repo": "test/example",
            "category": "Bug Report",
            "ground_truth": ["src/x.py:f"],
            "indexed": True,
            "agent_ran": True,
            "file_hit": True,
            "class_hit": False,
            "func_hit": True,
            "file_correct": True,
            "class_correct": False,
            "func_correct": True,
            "turns": 5,
            "input_tokens": 100,
            "output_tokens": 50,
            "cost_estimate_usd": 0.01,
            "duration_s": 10.0,
            "note": "",
            "agent_envelope": {
                "code_localize_agent": {
                    "entities": [
                        {"file_path": "src/x.py", "qualified_name": "f", "label": "Function"}
                    ],
                    "iterations": [],
                    "turns": 5,
                    "stop_reason": "finalized",
                    "transcript": [],
                    "input_tokens": 100,
                    "output_tokens": 50,
                }
            },
        }
        rec = audit._to_record(valid)
        assert rec is not None
        assert rec.instance_id == "test/example-3"
        assert rec.predicted_files == ["src/x.py"]
    finally:
        _restore_default_mode(saved)


def test_real_phase_a_json_parses_in_default_mode():
    """Regression catch: the on-disk Phase A JSON must parse without
    triggering the fail-loud path. If this fails, the schema changed
    out from under the existing baseline corpus."""
    saved = _save_default_mode()
    audit.ALLOW_LEGACY_FALLBACK = False
    try:
        phase_a_path = (
            Path(__file__).resolve().parents[2]
            / "bench" / "accuracy" / "baselines"
            / "2026-05-06-loc-bench-n50-serial.json"
        )
        if not phase_a_path.exists():
            return  # baseline not present in this checkout; skip
        import json

        data = json.loads(phase_a_path.read_text(encoding="utf-8"))
        # Parse every case via _to_record. Any failure surfaces here.
        for case in data["cases"]:
            try:
                audit._to_record(case)
            except audit.SchemaParseError as exc:
                raise AssertionError(
                    f"real Phase A case failed schema parse in default mode: {exc}"
                ) from exc
    finally:
        _restore_default_mode(saved)


def test_predicted_files_propagates_default_mode_failure():
    """Higher-level helpers (predicted_files) must propagate the
    fail-loud behavior — they shouldn't catch SchemaParseError and
    return [] silently. That would re-introduce the silent-failure
    pattern through the back door."""
    saved = _save_default_mode()
    audit.ALLOW_LEGACY_FALLBACK = False
    try:
        malformed = {"instance_id": "x", "repo": "y/z"}
        try:
            audit.predicted_files(malformed)
        except audit.SchemaParseError:
            return  # expected
        # If we got here, predicted_files ate the error silently.
        raise AssertionError(
            "predicted_files silently caught SchemaParseError; "
            "fail-loud doesn't propagate through helpers"
        )
    finally:
        _restore_default_mode(saved)


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
