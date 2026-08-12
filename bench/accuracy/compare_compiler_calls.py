#!/usr/bin/env python3
"""Compare artifact-bound compiler CALLS edges to an independent oracle."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sqlite3
from pathlib import Path


SHA256 = re.compile(r"[0-9a-f]{64}")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load_oracle(
    path: Path,
    *,
    include_dynamic: bool = False,
) -> set[tuple[str, int, str, int]]:
    document = json.loads(path.read_text(encoding="utf-8"))
    if (
        document.get("schema_version") != 1
        or document.get("oracle") != "go-ssa-rta-all-source-roots-v1"
        or not isinstance(document.get("edges"), list)
    ):
        raise ValueError("oracle has an unsupported contract")
    edges: set[tuple[str, int, str, int]] = set()
    for entry in document["edges"]:
        if entry.get("dynamic") is True and not include_dynamic:
            continue
        caller = entry.get("caller") if isinstance(entry, dict) else None
        callee = entry.get("callee") if isinstance(entry, dict) else None
        if not isinstance(caller, dict) or not isinstance(callee, dict):
            raise ValueError("oracle edge lacks coordinates")
        key = (
            caller.get("file"),
            caller.get("line"),
            callee.get("file"),
            callee.get("line"),
        )
        if (
            not isinstance(key[0], str)
            or not key[0]
            or not isinstance(key[1], int)
            or isinstance(key[1], bool)
            or key[1] <= 0
            or not isinstance(key[2], str)
            or not key[2]
            or not isinstance(key[3], int)
            or isinstance(key[3], bool)
            or key[3] <= 0
        ):
            raise ValueError("oracle edge contains invalid coordinates")
        edges.add(key)
    return edges


def load_compiler_edges(
    database: Path,
    project: str,
    artifact_digest: str,
) -> set[tuple[str, int, str, int]]:
    if SHA256.fullmatch(artifact_digest) is None:
        raise ValueError("artifact digest must be lowercase SHA-256")
    connection = sqlite3.connect(f"file:{database.resolve()}?mode=ro", uri=True)
    try:
        rows = connection.execute(
            """
            SELECT source.file_path, source.start_line,
                   target.file_path, target.start_line,
                   json_extract(edge.properties, '$.resolver_rule'),
                   json_extract(edge.properties, '$.resolution_artifact_sha256')
            FROM edges AS edge
            JOIN nodes AS source ON source.id = edge.source_id
            JOIN nodes AS target ON target.id = edge.target_id
            WHERE edge.project = ? AND edge.type = 'CALLS'
              AND json_extract(edge.properties, '$.resolver_rule') = 'scip-ingest'
            """,
            (project,),
        ).fetchall()
    finally:
        connection.close()
    edges: set[tuple[str, int, str, int]] = set()
    for source_file, source_line, target_file, target_line, source, digest in rows:
        if source != "scip-ingest" or digest != artifact_digest:
            raise ValueError("compiler edge is not bound to the requested artifact")
        edges.add((source_file, source_line, target_file, target_line))
    if not edges:
        raise ValueError("database contains no matching artifact-bound compiler edges")
    return edges


def metrics(
    oracle: set[tuple[str, int, str, int]],
    observed: set[tuple[str, int, str, int]],
) -> dict[str, float | int]:
    true_positive = len(oracle & observed)
    false_positive = len(observed - oracle)
    false_negative = len(oracle - observed)
    precision = true_positive / max(true_positive + false_positive, 1)
    recall = true_positive / max(true_positive + false_negative, 1)
    f1 = 2 * precision * recall / max(precision + recall, 1e-15)
    return {
        "true_positive": true_positive,
        "false_positive": false_positive,
        "false_negative": false_negative,
        "precision": precision,
        "recall": recall,
        "f1": f1,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--oracle", required=True, type=Path)
    parser.add_argument("--database", required=True, type=Path)
    parser.add_argument("--project", required=True)
    parser.add_argument("--scip-index", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    artifact_digest = sha256_file(args.scip_index)
    all_oracle = load_oracle(args.oracle, include_dynamic=True)
    oracle = load_oracle(args.oracle)
    observed = load_compiler_edges(args.database, args.project, artifact_digest)
    result = {
        "schema_version": 1,
        "oracle": "go-ssa-rta-all-source-roots-v1",
        "oracle_scope": "static_source_calls",
        "dynamic_oracle_edges_excluded": len(all_oracle - oracle),
        "observed_tier": "scip-ingest",
        "resolution_artifact_sha256": artifact_digest,
        "oracle_edges": len(oracle),
        "observed_edges": len(observed),
        **metrics(oracle, observed),
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
