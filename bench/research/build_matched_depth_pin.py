#!/usr/bin/env python3
"""Reconstruct the recorded LocBench n=200 pin for retrieval-only vs graph."""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_REPORT = (
    REPO_ROOT
    / "bench"
    / "accuracy"
    / "baselines"
    / "2026-06-12-loc-bench-n200-rebaseline.md"
)
DEFAULT_BUNDLE = REPO_ROOT / "bench" / "research" / "locbench_v1_instances.json"
DEFAULT_OUTPUT = (
    REPO_ROOT
    / "bench"
    / "accuracy"
    / "baselines"
    / "data"
    / "2026-06-12-matched-depth-n200"
    / "locbench-n200-pin.json"
)
EXPECTED_N = 200
TABLE_MARKER = "## Per-instance details (200 rows)"
HF_REVISION = "c44cf3b74e07ca642cec841b471a9939907c12a7"
PARQUET_PATH = "data/test-00000-of-00001.parquet"
PARQUET_SHA256 = "8df0833c2c1276c5837aab923d489ab97d7654529abe759d0f59242c4978a662"
PARQUET_SIZE_BYTES = 3_084_430
CODE_SEARCH_TAG = "v0.2.1"
CODE_SEARCH_ARTIFACT_SHA256 = (
    "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
)


class PinError(ValueError):
    """The durable record cannot be reconstructed without ambiguity."""


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def repository_path(path: Path) -> str:
    try:
        return path.resolve().relative_to(REPO_ROOT).as_posix()
    except ValueError:
        return str(path.resolve())


def recorded_ids(report_text: str) -> list[str]:
    if TABLE_MARKER not in report_text:
        raise PinError(f"recorded table marker missing: {TABLE_MARKER}")
    table = report_text.split(TABLE_MARKER, 1)[1]
    ids: list[str] = []
    for line in table.splitlines():
        if line.startswith("## ") and ids:
            break
        if not line.startswith("|"):
            continue
        cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
        if len(cells) < 6 or cells[0] in {"instance", "---"}:
            continue
        instance_id = cells[0]
        if "__" not in instance_id:
            continue
        ids.append(instance_id)
    if len(ids) != EXPECTED_N:
        raise PinError(f"recorded table has {len(ids)} IDs, expected {EXPECTED_N}")
    duplicates = sorted({instance_id for instance_id in ids if ids.count(instance_id) > 1})
    if duplicates:
        raise PinError(f"recorded table has duplicate IDs: {duplicates}")
    return ids


def build_pin(report: Path, bundle: Path) -> dict:
    report_bytes = report.read_bytes()
    bundle_bytes = bundle.read_bytes()
    ids = recorded_ids(report_bytes.decode("utf-8"))
    instances = json.loads(bundle_bytes)
    if not isinstance(instances, list):
        raise PinError("LocBench metadata bundle must be a JSON list")
    by_id: dict[str, dict] = {}
    for instance in instances:
        instance_id = instance.get("instance_id") if isinstance(instance, dict) else None
        if not instance_id:
            raise PinError("LocBench metadata bundle contains an entry without instance_id")
        if instance_id in by_id:
            raise PinError(f"LocBench metadata bundle has duplicate ID: {instance_id}")
        by_id[instance_id] = instance
    missing = [instance_id for instance_id in ids if instance_id not in by_id]
    if missing:
        raise PinError(f"{len(missing)} recorded IDs missing from metadata bundle: {missing}")

    cases = [
        {
            "instance_id": instance_id,
            "repo": by_id[instance_id]["repo"],
            "base_commit": by_id[instance_id]["base_commit"],
            "category": by_id[instance_id]["category"],
        }
        for instance_id in ids
    ]
    return {
        "schema_version": 1,
        "purpose": "retrieval-only-vs-graph matched-depth LocBench n=200",
        "n": EXPECTED_N,
        "score_depth": 10,
        "query_rule": "problem_statement first paragraph, stripped",
        "recorded_source": {
            "path": repository_path(report),
            "sha256": sha256_bytes(report_bytes),
            "table_marker": TABLE_MARKER,
        },
        "dataset": {
            "metadata_bundle_path": repository_path(bundle),
            "metadata_bundle_sha256": sha256_bytes(bundle_bytes),
            "metadata_bundle_n": len(instances),
            "repository": "czlll/Loc-Bench_V1",
            "revision": HF_REVISION,
            "parquet_path": PARQUET_PATH,
            "parquet_sha256": PARQUET_SHA256,
            "parquet_size_bytes": PARQUET_SIZE_BYTES,
        },
        "component_pins": {
            "code_search": {
                "tag": CODE_SEARCH_TAG,
                "artifact_sha256": CODE_SEARCH_ARTIFACT_SHA256,
            }
        },
        "recorded_order_sha256": sha256_bytes(
            ("\n".join(ids) + "\n").encode("utf-8")
        ),
        "pinned_instance_ids": ids,
        "cases": cases,
    }


def encoded_pin(pin: dict) -> bytes:
    return (json.dumps(pin, indent=2, ensure_ascii=False) + "\n").encode("utf-8")


def sidecar_bytes(output: Path, payload: bytes) -> bytes:
    return f"{sha256_bytes(payload)}  {output.name}\n".encode("utf-8")


def write_pin(output: Path, payload: bytes) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(payload)
    Path(str(output) + ".sha256").write_bytes(sidecar_bytes(output, payload))


def check_pin(output: Path, payload: bytes) -> None:
    sidecar = Path(str(output) + ".sha256")
    if not output.is_file() or output.read_bytes() != payload:
        raise PinError(f"canonical pin is stale or missing: {output}")
    expected_sidecar = sidecar_bytes(output, payload)
    if not sidecar.is_file() or sidecar.read_bytes() != expected_sidecar:
        raise PinError(f"canonical pin checksum is stale or missing: {sidecar}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path, default=DEFAULT_REPORT)
    parser.add_argument("--bundle", type=Path, default=DEFAULT_BUNDLE)
    parser.add_argument("--output", type=Path, default=DEFAULT_OUTPUT)
    parser.add_argument(
        "--check",
        action="store_true",
        help="fail unless the committed pin and checksum exactly match reconstruction",
    )
    args = parser.parse_args()
    try:
        payload = encoded_pin(build_pin(args.report, args.bundle))
        if args.check:
            check_pin(args.output, payload)
        else:
            write_pin(args.output, payload)
    except (OSError, json.JSONDecodeError, PinError, KeyError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    print(
        f"{'verified' if args.check else 'wrote'} {args.output}: "
        f"n={EXPECTED_N} sha256={sha256_bytes(payload)}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
