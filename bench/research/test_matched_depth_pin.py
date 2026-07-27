from __future__ import annotations

import hashlib
import json
import subprocess
import sys
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
BUILDER = REPO_ROOT / "bench" / "research" / "build_matched_depth_pin.py"
REPORT = (
    REPO_ROOT
    / "bench"
    / "accuracy"
    / "baselines"
    / "2026-06-12-loc-bench-n200-rebaseline.md"
)
BUNDLE = REPO_ROOT / "bench" / "research" / "locbench_v1_instances.json"


def test_builder_reconstructs_recorded_n200_pin_against_bundle(tmp_path: Path) -> None:
    output = tmp_path / "locbench-n200.json"
    completed = subprocess.run(
        [
            sys.executable,
            str(BUILDER),
            "--report",
            str(REPORT),
            "--bundle",
            str(BUNDLE),
            "--output",
            str(output),
        ],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
    )

    assert completed.returncode == 0, completed.stderr
    pin = json.loads(output.read_text(encoding="utf-8"))
    assert pin["purpose"] == "retrieval-only-vs-graph matched-depth LocBench n=200"
    assert pin["n"] == 200
    assert len(pin["pinned_instance_ids"]) == 200
    assert len(set(pin["pinned_instance_ids"])) == 200
    assert [case["instance_id"] for case in pin["cases"]] == pin["pinned_instance_ids"]
    assert pin["dataset"]["metadata_bundle_sha256"] == hashlib.sha256(
        BUNDLE.read_bytes()
    ).hexdigest()
    assert pin["dataset"]["parquet_sha256"] == (
        "8df0833c2c1276c5837aab923d489ab97d7654529abe759d0f59242c4978a662"
    )
    assert pin["component_pins"]["code_search"] == {
        "tag": "v0.2.1",
        "artifact_sha256": (
            "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
        ),
    }

    sidecar = output.with_suffix(output.suffix + ".sha256")
    digest, filename = sidecar.read_text(encoding="utf-8").strip().split("  ", 1)
    assert digest == hashlib.sha256(output.read_bytes()).hexdigest()
    assert filename == output.name
